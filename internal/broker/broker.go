// Package broker serves the shim's socket requests (spec §8.5–§8.7): it verifies trust on every request
// and schedules jobs one at a time per project.
package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/job"
	"actionbox/internal/limits"
	"actionbox/internal/protocol"
	"actionbox/internal/trust"
	"actionbox/internal/verify"
)

const (
	DefaultWait           = 30 * time.Second
	DefaultRequestTimeout = 30 * time.Second
)

// eventlog adds prev hash chain itself (§8.7 step 6)
type Events interface {
	Append(event any) error
}

type VerifyFunc func(ctx context.Context, env verify.Env, name string) (verify.Outcome, error)

type Options struct {
	Env            verify.Env
	Runner         job.Runner
	Events         Events
	Out            io.Writer
	Now            func() time.Time
	Verify         VerifyFunc
	Wait           time.Duration
	RequestTimeout time.Duration
	PeerUID        func(net.Conn) (int, error)
}

type Event struct {
	TS                time.Time         `json:"ts"`
	Kind              string            `json:"kind"`
	Project           string            `json:"project"`
	JobID             string            `json:"job_id,omitempty"`
	Action            string            `json:"action"`
	Params            map[string]string `json:"params"`
	DefinitionsCommit string            `json:"definitions_commit,omitempty"`
	State             protocol.JobState `json:"state"`
	ExitCode          *int              `json:"exit_code"`
	Detail            string            `json:"detail,omitempty"`
}

const (
	EventJobStart    = "job_start"
	EventJobEnd      = "job_end"
	EventJobRejected = "job_rejected"
)

type Broker struct {
	opts      Options
	caps      limits.Limits
	retention config.Retention

	outMu sync.Mutex

	mu      sync.Mutex
	ctx     context.Context
	active  map[string]*entry
	queues  map[string][]*entry
	busy    map[string]bool
	workers sync.WaitGroup
}

// prunes here: serve startup (R20)
func New(opts Options) *Broker {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Verify == nil {
		opts.Verify = verify.Project
	}
	if opts.Wait <= 0 {
		opts.Wait = DefaultWait
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if opts.PeerUID == nil {
		opts.PeerUID = peerUID
	}
	cfg := opts.Env.Config
	b := &Broker{
		opts:   opts,
		caps:   cfg.Caps.WithDefaults(config.DefaultCaps),
		active: map[string]*entry{},
		queues: map[string][]*entry{},
		busy:   map[string]bool{},
		retention: config.Retention{
			JobsPerProject: positiveOr(cfg.Retention.JobsPerProject, config.DefaultRetention.JobsPerProject),
			MaxAge:         positiveOr(cfg.Retention.MaxAge, config.DefaultRetention.MaxAge),
		},
	}
	for name := range cfg.Projects {
		b.prune(name)
	}
	return b
}

func positiveOr[T int | time.Duration](v, d T) T {
	if v <= 0 {
		return d
	}
	return v
}

func (b *Broker) Serve(ctx context.Context, l net.Listener) error {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()

	var conns sync.WaitGroup
	var err error
	for {
		conn, aerr := l.Accept()
		if aerr != nil {
			if ctx.Err() == nil {
				err = aerr
				l.Close()
			}
			break
		}
		conns.Go(func() { b.handleConn(ctx, conn) })
	}
	conns.Wait()
	b.workers.Wait()
	return err
}

func (b *Broker) handle(ctx context.Context, req protocol.Request) protocol.Response {
	switch req.Op {
	case protocol.OpListActions, protocol.OpRunAction, protocol.OpJobStatus:
	case protocol.OpRequestFetch, protocol.OpFetchStatus:
		return protocol.Response{Error: "not implemented yet (M4)"}
	default:
		return protocol.Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}

	name, project, ok := b.projectFor(req.Cwd)
	if !ok {
		return b.reject(req, "", "", "rejected: unregistered project")
	}
	vctx, cancel := context.WithTimeout(ctx, b.opts.RequestTimeout)
	defer cancel()
	outcome, err := b.opts.Verify(vctx, b.opts.Env, name)
	if err != nil {
		b.display("%s: verify failed: %v", name, err)
		var f *trust.Failure
		if errors.As(err, &f) {
			return b.reject(req, name, "", "rejected: untrusted definitions: "+f.Reason)
		}
		return b.reject(req, name, "", "rejected: definitions could not be verified")
	}

	switch req.Op {
	case protocol.OpListActions:
		b.display("%s: list_actions", name)
		return protocol.Response{Actions: outcome.Definitions.AgentListing()}
	case protocol.OpRunAction:
		return b.runAction(ctx, req, project, outcome)
	default:
		return b.jobStatus(name, req.JobID)
	}
}

// symlinked cwd must not pick a project by its link path
func (b *Broker) projectFor(cwd string) (string, config.Project, bool) {
	if !filepath.IsAbs(cwd) {
		return "", config.Project{}, false
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", config.Project{}, false
	}
	return b.opts.Env.Config.ProjectForDir(real)
}

func (b *Broker) reject(req protocol.Request, project, commit, msg string) protocol.Response {
	if req.Op != protocol.OpRunAction {
		b.display("%s: %s %s", orUnknown(project), req.Op, msg)
		return protocol.Response{Error: msg}
	}
	b.display("%s: run_action %q %s", orUnknown(project), req.Name, msg)
	b.appendEvent(Event{
		Kind:              EventJobRejected,
		Project:           project,
		Action:            req.Name,
		Params:            req.Params,
		DefinitionsCommit: commit,
		State:             protocol.JobRejected,
		Detail:            msg,
	})
	return protocol.Response{Error: msg, Job: &protocol.Job{State: protocol.JobRejected, Detail: msg}}
}

func orUnknown(project string) string {
	if project == "" {
		return "?"
	}
	return project
}

func (b *Broker) runAction(ctx context.Context, req protocol.Request, project config.Project, outcome verify.Outcome) protocol.Response {
	action, ok := outcome.Definitions.Actions[req.Name]
	if !ok || action.Invoker != definitions.InvokerAgent {
		return b.reject(req, project.Name, outcome.Commit, fmt.Sprintf("rejected: action %q not available", req.Name))
	}
	argv, err := action.Argv(req.Params)
	if err != nil {
		return b.reject(req, project.Name, outcome.Commit, "rejected: "+err.Error())
	}
	e, err := b.enqueue(project, outcome.Commit, action, req.Params, argv)
	if err != nil {
		b.display("%s: run_action %q failed: %v", project.Name, req.Name, err)
		return protocol.Response{Error: "job could not be created"}
	}

	timer := time.NewTimer(b.opts.Wait)
	defer timer.Stop()
	select {
	case <-e.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	return protocol.Response{Job: b.view(e.snapshot(&b.mu))}
}

func (b *Broker) appendEvent(ev Event) {
	ev.TS = b.opts.Now().UTC()
	if err := b.opts.Events.Append(ev); err != nil {
		b.display("event log: %v", err)
	}
}

// agent-controlled text reaches the owner's terminal: no control chars, no escape sequences
func (b *Broker) display(format string, args ...any) {
	line := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return '?'
		}
		return r
	}, fmt.Sprintf(format, args...))
	b.outMu.Lock()
	defer b.outMu.Unlock()
	fmt.Fprintf(b.opts.Out, "%s %s\n", b.opts.Now().Format("15:04:05"), line)
}
