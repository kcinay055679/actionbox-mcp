package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/job"
	"actionbox/internal/protocol"
)

const (
	idTimeLayout = "20060102-150405.000000"
	recordFile   = "job.json"
	logFile      = "log.txt"
	lockPoll     = 50 * time.Millisecond
)

// job IDs come from the agent in job_status and become path elements
var jobIDPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}\.[0-9]{6}-[0-9a-f]{8}$`)

func validJobID(id string) bool { return jobIDPattern.MatchString(id) }

type entry struct {
	rec  record
	spec job.Spec
	done chan struct{}
}

func (e *entry) snapshot(mu *sync.Mutex) record {
	mu.Lock()
	defer mu.Unlock()
	return e.rec
}

func (b *Broker) newID() string {
	var r [4]byte
	rand.Read(r[:])
	return b.opts.Now().UTC().Format(idTimeLayout) + "-" + hex.EncodeToString(r[:])
}

func (b *Broker) enqueue(project config.Project, commit string, action *definitions.Action, params map[string]string, argv []string) (*entry, error) {
	id := b.newID()
	dir := b.opts.Env.Paths.JobDir(project.Name, id)
	if err := createJobDir(dir); err != nil {
		return nil, err
	}
	lim := b.caps.Clamp(action.Limits.WithDefaults(b.caps))
	e := &entry{
		rec: record{
			ID:                id,
			Project:           project.Name,
			Action:            action.Name,
			Params:            params,
			Argv:              argv,
			DefinitionsCommit: commit,
			Limits:            lim,
			State:             protocol.JobQueued,
			Created:           b.opts.Now().UTC(),
		},
		spec: job.Spec{
			ID:         id,
			Project:    project.Name,
			RepoPath:   project.Path,
			Dir:        dir,
			Action:     action.Name,
			Argv:       argv,
			Writes:     action.Writes,
			Containers: action.Containers,
			Limits:     lim,
		},
		done: make(chan struct{}),
	}
	if err := writeRecord(filepath.Join(dir, recordFile), e.rec); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.active[activeKey(project.Name, id)] = e
	b.queues[project.Name] = append(b.queues[project.Name], e)
	if !b.busy[project.Name] {
		b.busy[project.Name] = true
		b.workers.Go(func() { b.work(project.Name) })
	}
	b.display("%s: %s %q queued %v", project.Name, id, action.Name, params)
	return e, nil
}

func activeKey(project, id string) string { return project + "/" + id }

func createJobDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	for _, sub := range []string{"xdg", "tmp", "home"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, logFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// one worker per project with queued jobs: FIFO within a project, projects in parallel
func (b *Broker) work(project string) {
	for {
		b.mu.Lock()
		queue := b.queues[project]
		if len(queue) == 0 {
			b.busy[project] = false
			b.mu.Unlock()
			return
		}
		e := queue[0]
		b.queues[project] = queue[1:]
		b.mu.Unlock()
		b.runJob(e)
	}
}

func (b *Broker) runJob(e *entry) {
	ctx := b.ctx
	if ctx.Err() != nil {
		b.finish(e, false, protocol.JobKilled, nil, "broker stopped before the job started")
		return
	}
	unlock, err := lockProject(ctx, b.opts.Env.Paths, e.spec.Project)
	if err != nil {
		if ctx.Err() != nil {
			b.finish(e, false, protocol.JobKilled, nil, "broker stopped before the job started")
		} else {
			b.finish(e, false, protocol.JobFailed, nil, "project lock: "+err.Error())
		}
		return
	}
	defer unlock()

	b.mu.Lock()
	started := b.opts.Now().UTC()
	e.rec.State, e.rec.Started = protocol.JobRunning, &started
	rec := e.rec
	b.mu.Unlock()
	if err := writeRecord(filepath.Join(e.spec.Dir, recordFile), rec); err != nil {
		b.finish(e, false, protocol.JobFailed, nil, "job record: "+err.Error())
		return
	}
	// audit trail must be complete: no job_start entry => job does not run
	if err := b.opts.Events.Append(b.jobEvent(EventJobStart, rec)); err != nil {
		b.finish(e, false, protocol.JobFailed, nil, "event log: "+err.Error())
		return
	}
	b.display("%s: %s %q started", rec.Project, rec.ID, rec.Action)

	state, exitCode, detail := b.execute(ctx, e.spec)
	b.finish(e, true, state, exitCode, detail)
}

func (b *Broker) execute(ctx context.Context, spec job.Spec) (protocol.JobState, *int, string) {
	out, err := os.OpenFile(filepath.Join(spec.Dir, logFile), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return protocol.JobFailed, nil, "open log: " + err.Error()
	}
	defer out.Close()
	res, err := b.opts.Runner.Run(ctx, spec, out)
	switch {
	case err != nil:
		return protocol.JobFailed, nil, err.Error()
	case res.TimedOut:
		return protocol.JobTimedOut, nil, ""
	case res.Killed:
		return protocol.JobKilled, nil, ""
	case res.ExitCode == 0:
		return protocol.JobSucceeded, &res.ExitCode, ""
	default:
		return protocol.JobFailed, &res.ExitCode, ""
	}
}

func (b *Broker) finish(e *entry, started bool, state protocol.JobState, exitCode *int, detail string) {
	b.mu.Lock()
	ended := b.opts.Now().UTC()
	e.rec.State, e.rec.ExitCode, e.rec.Detail, e.rec.Ended = state, exitCode, detail, &ended
	rec := e.rec
	b.mu.Unlock()

	if err := writeRecord(filepath.Join(e.spec.Dir, recordFile), rec); err != nil {
		b.display("%s: %s job record: %v", rec.Project, rec.ID, err)
	}
	if started {
		b.appendEvent(b.jobEvent(EventJobEnd, rec))
	}
	b.display("%s: %s %q %s%s%s", rec.Project, rec.ID, rec.Action, state, exitSuffix(exitCode), detailSuffix(detail))

	b.mu.Lock()
	delete(b.active, activeKey(rec.Project, rec.ID))
	b.mu.Unlock()
	close(e.done)
	b.prune(rec.Project)
}

func exitSuffix(code *int) string {
	if code == nil {
		return ""
	}
	return fmt.Sprintf(" (exit %d)", *code)
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

func (b *Broker) jobEvent(kind string, rec record) Event {
	return Event{
		TS:                b.opts.Now().UTC(),
		Kind:              kind,
		Project:           rec.Project,
		JobID:             rec.ID,
		Action:            rec.Action,
		Params:            rec.Params,
		DefinitionsCommit: rec.DefinitionsCommit,
		State:             rec.State,
		ExitCode:          rec.ExitCode,
		Detail:            rec.Detail,
	}
}

// lockProject polls: a blocking flock can't be cancelled when the owner stops serve
func lockProject(ctx context.Context, paths config.Paths, project string) (func(), error) {
	if err := os.MkdirAll(paths.ProjectDir(project), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(paths.Lock(project), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

func projectLockFree(paths config.Paths, project string) bool {
	f, err := os.OpenFile(paths.Lock(project), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer f.Close()
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

func (b *Broker) jobStatus(project, id string) protocol.Response {
	if !validJobID(id) {
		b.display("%s: job_status rejected: invalid job id %q", project, id)
		return protocol.Response{Error: "rejected: invalid job id"}
	}
	b.mu.Lock()
	e, ok := b.active[activeKey(project, id)]
	b.mu.Unlock()
	if ok {
		return protocol.Response{Job: b.view(e.snapshot(&b.mu))}
	}

	rec, err := readRecord(filepath.Join(b.opts.Env.Paths.JobDir(project, id), recordFile))
	if errors.Is(err, os.ErrNotExist) {
		return protocol.Response{Error: "rejected: unknown job"}
	}
	if err != nil {
		b.display("%s: job_status %s: %v", project, id, err)
		return protocol.Response{Error: "job record unreadable"}
	}
	// not ours and nobody holds the project lock => its broker died mid-job
	if !rec.State.Done() && projectLockFree(b.opts.Env.Paths, project) {
		rec.State, rec.ExitCode, rec.Detail = protocol.JobKilled, nil, "abandoned: no broker runs this job"
	}
	return protocol.Response{Job: b.view(rec)}
}

func (b *Broker) view(rec record) *protocol.Job {
	logPath := filepath.Join(b.opts.Env.Paths.JobDir(rec.Project, rec.ID), logFile)
	out, err := tail(logPath)
	if err != nil {
		b.display("%s: %s tail: %v", rec.Project, rec.ID, err)
	}
	return &protocol.Job{
		ID:         rec.ID,
		State:      rec.State,
		ExitCode:   rec.ExitCode,
		OutputTail: out,
		LogPath:    logPath,
		Detail:     rec.Detail,
	}
}
