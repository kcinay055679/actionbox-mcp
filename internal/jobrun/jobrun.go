// Package jobrun is the job lifecycle of spec §8.7, shared by serve's broker and actionbox run.
package jobrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/job"
	"actionbox/internal/limits"
	"actionbox/internal/protocol"
)

const (
	IDTimeLayout = "20060102-150405.000000"
	RecordFile   = "job.json"
	LogFile      = "log.txt"
	lockPoll     = 50 * time.Millisecond
	notStarted   = "cancelled before the job started"
)

// job IDs come from the agent in job_status and become path elements
var idPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}\.[0-9]{6}-[0-9a-f]{8}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }

func NewID(now time.Time) string {
	var r [4]byte
	rand.Read(r[:])
	return now.UTC().Format(IDTimeLayout) + "-" + hex.EncodeToString(r[:])
}

// eventlog adds prev hash chain itself (§8.7 step 6)
type Events interface {
	Append(event any) error
}

type Env struct {
	Paths  config.Paths
	Runner job.Runner
	Events Events
	Now    func() time.Time
}

type Job struct {
	Spec job.Spec

	mu  sync.Mutex
	rec Record
}

func Create(env Env, caps limits.Limits, invoker definitions.Invoker, project config.Project, commit string,
	action *definitions.Action, params map[string]string, argv []string) (*Job, error) {
	id := NewID(env.Now())
	dir := env.Paths.JobDir(project.Name, id)
	if err := createDir(dir); err != nil {
		return nil, err
	}
	lim := caps.Clamp(action.Limits.WithDefaults(caps))
	j := &Job{
		rec: Record{
			ID:                id,
			Project:           project.Name,
			Action:            action.Name,
			Invoker:           invoker,
			Params:            params,
			Argv:              argv,
			DefinitionsCommit: commit,
			Limits:            lim,
			State:             protocol.JobQueued,
			Created:           env.Now().UTC(),
		},
		Spec: job.Spec{
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
	}
	if err := WriteRecord(j.recordPath(), j.rec); err != nil {
		return nil, err
	}
	return j, nil
}

func createDir(dir string) error {
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
	f, err := os.OpenFile(filepath.Join(dir, LogFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (j *Job) Record() Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.rec
}

func (j *Job) recordPath() string { return filepath.Join(j.Spec.Dir, RecordFile) }

// error = record/event log write failed after outcome was decided; returned record still valid
func (j *Job) Run(ctx context.Context, env Env, output io.Writer, started func(Record)) (Record, error) {
	if ctx.Err() != nil {
		return j.finish(env, false, protocol.JobKilled, nil, notStarted)
	}
	unlock, err := Lock(ctx, env.Paths, j.Spec.Project)
	if err != nil {
		if ctx.Err() != nil {
			return j.finish(env, false, protocol.JobKilled, nil, notStarted)
		}
		return j.finish(env, false, protocol.JobFailed, nil, "project lock: "+err.Error())
	}
	defer unlock()

	j.mu.Lock()
	start := env.Now().UTC()
	j.rec.State, j.rec.Started = protocol.JobRunning, &start
	rec := j.rec
	j.mu.Unlock()
	if err := WriteRecord(j.recordPath(), rec); err != nil {
		return j.finish(env, false, protocol.JobFailed, nil, "job record: "+err.Error())
	}
	// audit trail must be complete: no job_start entry => job does not run
	if err := env.Events.Append(NewEvent(EventJobStart, rec, env.Now())); err != nil {
		return j.finish(env, false, protocol.JobFailed, nil, "event log: "+err.Error())
	}
	if started != nil {
		started(rec)
	}

	state, exitCode, detail := execute(ctx, env.Runner, j.Spec, output)
	return j.finish(env, true, state, exitCode, detail)
}

func execute(ctx context.Context, runner job.Runner, spec job.Spec, output io.Writer) (protocol.JobState, *int, string) {
	log, err := os.OpenFile(filepath.Join(spec.Dir, LogFile), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return protocol.JobFailed, nil, "open log: " + err.Error()
	}
	defer log.Close()
	var out io.Writer = log
	if output != nil {
		out = tee{log: log, extra: output}
	}
	res, err := runner.Run(ctx, spec, out)
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

// extra errors ignored: closed terminal must not fail the job, log.txt stays complete
type tee struct {
	log   io.Writer
	extra io.Writer
}

func (t tee) Write(p []byte) (int, error) {
	n, err := t.log.Write(p)
	t.extra.Write(p[:n])
	return n, err
}

func (j *Job) finish(env Env, started bool, state protocol.JobState, exitCode *int, detail string) (Record, error) {
	j.mu.Lock()
	ended := env.Now().UTC()
	j.rec.State, j.rec.ExitCode, j.rec.Detail, j.rec.Ended = state, exitCode, detail, &ended
	rec := j.rec
	j.mu.Unlock()

	var errs []error
	if err := WriteRecord(j.recordPath(), rec); err != nil {
		errs = append(errs, fmt.Errorf("job record: %w", err))
	}
	if started {
		if err := env.Events.Append(NewEvent(EventJobEnd, rec, env.Now())); err != nil {
			errs = append(errs, fmt.Errorf("event log: %w", err))
		}
	}
	return rec, errors.Join(errs...)
}

// Lock polls: a blocking flock can't be cancelled by Ctrl-C
func Lock(ctx context.Context, paths config.Paths, project string) (func(), error) {
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

func LockFree(paths config.Paths, project string) bool {
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

func CapsFor(cfg *config.Config) limits.Limits {
	return cfg.Caps.WithDefaults(config.DefaultCaps)
}

func RetentionFor(cfg *config.Config) config.Retention {
	return config.Retention{
		JobsPerProject: positiveOr(cfg.Retention.JobsPerProject, config.DefaultRetention.JobsPerProject),
		MaxAge:         positiveOr(cfg.Retention.MaxAge, config.DefaultRetention.MaxAge),
	}
}

func positiveOr[T int | time.Duration](v, d T) T {
	if v <= 0 {
		return d
	}
	return v
}
