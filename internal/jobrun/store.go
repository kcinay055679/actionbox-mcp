package jobrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/limits"
	"actionbox/internal/protocol"
)

// job.json, not meta.json: engine owns meta.json in same dir
type Record struct {
	ID                string              `json:"job_id"`
	Project           string              `json:"project"`
	Action            string              `json:"action"`
	Invoker           definitions.Invoker `json:"invoker"`
	Params            map[string]string   `json:"params"`
	Argv              []string            `json:"argv"`
	DefinitionsCommit string              `json:"definitions_commit"`
	Limits            limits.Limits       `json:"limits"`
	State             protocol.JobState   `json:"state"`
	ExitCode          *int                `json:"exit_code,omitempty"`
	Detail            string              `json:"detail,omitempty"`
	Created           time.Time           `json:"created"`
	Started           *time.Time          `json:"started,omitempty"`
	Ended             *time.Time          `json:"ended,omitempty"`
}

func (r Record) Outcome() string {
	s := string(r.State)
	if r.ExitCode != nil {
		s += fmt.Sprintf(" (exit %d)", *r.ExitCode)
	}
	if r.Detail != "" {
		s += ": " + r.Detail
	}
	return s
}

func WriteRecord(path string, rec Record) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".job-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func ReadRecord(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	err = json.Unmarshal(raw, &rec)
	return rec, err
}

type Event struct {
	TS                time.Time           `json:"ts"`
	Kind              string              `json:"kind"`
	Project           string              `json:"project"`
	JobID             string              `json:"job_id,omitempty"`
	Action            string              `json:"action"`
	Invoker           definitions.Invoker `json:"invoker,omitempty"`
	Params            map[string]string   `json:"params"`
	DefinitionsCommit string              `json:"definitions_commit,omitempty"`
	State             protocol.JobState   `json:"state"`
	ExitCode          *int                `json:"exit_code"`
	Detail            string              `json:"detail,omitempty"`
}

const (
	EventJobStart    = "job_start"
	EventJobEnd      = "job_end"
	EventJobRejected = "job_rejected"
)

func NewEvent(kind string, rec Record, ts time.Time) Event {
	return Event{
		TS:                ts.UTC(),
		Kind:              kind,
		Project:           rec.Project,
		JobID:             rec.ID,
		Action:            rec.Action,
		Invoker:           rec.Invoker,
		Params:            rec.Params,
		DefinitionsCommit: rec.DefinitionsCommit,
		State:             rec.State,
		ExitCode:          rec.ExitCode,
		Detail:            rec.Detail,
	}
}

// R20; dirs not named like a job ID are left alone
func Prune(paths config.Paths, project string, ret config.Retention, now time.Time, active func(id string) bool) error {
	dir := paths.JobsDir(project)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && ValidID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	var errs []error
	for i, id := range ids {
		created, err := time.Parse(IDTimeLayout, id[:len(IDTimeLayout)])
		tooOld := err == nil && now.Sub(created) > ret.MaxAge
		if i < ret.JobsPerProject && !tooOld {
			continue
		}
		if active != nil && active(id) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, id)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}
