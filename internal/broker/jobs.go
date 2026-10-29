package broker

import (
	"errors"
	"os"
	"path/filepath"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/jobrun"
	"actionbox/internal/protocol"
)

type entry struct {
	job  *jobrun.Job
	done chan struct{}
}

func (b *Broker) jobEnv() jobrun.Env {
	return jobrun.Env{Paths: b.opts.Env.Paths, Runner: b.opts.Runner, Events: b.opts.Events, Now: b.opts.Now}
}

func (b *Broker) enqueue(project config.Project, commit string, action *definitions.Action, params map[string]string, argv []string) (*entry, error) {
	j, err := jobrun.Create(b.jobEnv(), b.caps, definitions.InvokerAgent, project, commit, action, params, argv)
	if err != nil {
		return nil, err
	}
	e := &entry{job: j, done: make(chan struct{})}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.active[activeKey(project.Name, j.Spec.ID)] = e
	b.queues[project.Name] = append(b.queues[project.Name], e)
	if !b.busy[project.Name] {
		b.busy[project.Name] = true
		b.workers.Go(func() { b.work(project.Name) })
	}
	b.display("%s: %s %q queued %v", project.Name, j.Spec.ID, action.Name, params)
	return e, nil
}

func activeKey(project, id string) string { return project + "/" + id }

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
	rec, err := e.job.Run(b.ctx, b.jobEnv(), nil, func(rec jobrun.Record) {
		b.display("%s: %s %q started", rec.Project, rec.ID, rec.Action)
	})
	if err != nil {
		b.display("%s: %s %v", rec.Project, rec.ID, err)
	}
	b.display("%s: %s %q %s", rec.Project, rec.ID, rec.Action, rec.Outcome())

	b.mu.Lock()
	delete(b.active, activeKey(rec.Project, rec.ID))
	b.mu.Unlock()
	close(e.done)
	b.prune(rec.Project)
}

func (b *Broker) jobStatus(project, id string) protocol.Response {
	if !jobrun.ValidID(id) {
		b.display("%s: job_status rejected: invalid job id %q", project, id)
		return protocol.Response{Error: "rejected: invalid job id"}
	}
	b.mu.Lock()
	e, ok := b.active[activeKey(project, id)]
	b.mu.Unlock()
	if ok {
		return protocol.Response{Job: b.view(e.job.Record())}
	}

	rec, err := jobrun.ReadRecord(filepath.Join(b.opts.Env.Paths.JobDir(project, id), jobrun.RecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return protocol.Response{Error: "rejected: unknown job"}
	}
	if err != nil {
		b.display("%s: job_status %s: %v", project, id, err)
		return protocol.Response{Error: "job record unreadable"}
	}
	// not ours and nobody holds the project lock => its broker died mid-job
	if !rec.State.Done() && jobrun.LockFree(b.opts.Env.Paths, project) {
		rec.State, rec.ExitCode, rec.Detail = protocol.JobKilled, nil, "abandoned: no broker runs this job"
	}
	return protocol.Response{Job: b.view(rec)}
}

func (b *Broker) view(rec jobrun.Record) *protocol.Job {
	logPath := filepath.Join(b.opts.Env.Paths.JobDir(rec.Project, rec.ID), jobrun.LogFile)
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
