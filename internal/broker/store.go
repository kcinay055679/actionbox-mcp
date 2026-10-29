package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"actionbox/internal/limits"
	"actionbox/internal/protocol"
)

// job.json, not meta.json: engine owns meta.json in same dir
type record struct {
	ID                string            `json:"job_id"`
	Project           string            `json:"project"`
	Action            string            `json:"action"`
	Params            map[string]string `json:"params"`
	Argv              []string          `json:"argv"`
	DefinitionsCommit string            `json:"definitions_commit"`
	Limits            limits.Limits     `json:"limits"`
	State             protocol.JobState `json:"state"`
	ExitCode          *int              `json:"exit_code,omitempty"`
	Detail            string            `json:"detail,omitempty"`
	Created           time.Time         `json:"created"`
	Started           *time.Time        `json:"started,omitempty"`
	Ended             *time.Time        `json:"ended,omitempty"`
}

func writeRecord(path string, rec record) error {
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

func readRecord(path string) (record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	var rec record
	err = json.Unmarshal(raw, &rec)
	return rec, err
}

func tail(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	offset := max(0, info.Size()-protocol.TailBytes)
	buf := make([]byte, info.Size()-offset)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	buf = buf[:n]
	for offset > 0 && len(buf) > 0 && !utf8.RuneStart(buf[0]) {
		buf = buf[1:]
	}

	end := len(buf)
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	for range protocol.TailLines {
		i := bytes.LastIndexByte(buf[:end], '\n')
		if i < 0 {
			return string(buf), nil
		}
		end = i
	}
	return string(buf[end+1:]), nil
}

// R20; dirs not named like a job ID are left alone
func (b *Broker) prune(project string) {
	dir := b.opts.Env.Paths.JobsDir(project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			b.display("%s: prune: %v", project, err)
		}
		return
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && validJobID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	now := b.opts.Now()
	for i, id := range ids {
		created, err := time.Parse(idTimeLayout, id[:len(idTimeLayout)])
		tooOld := err == nil && now.Sub(created) > b.retention.MaxAge
		if i < b.retention.JobsPerProject && !tooOld {
			continue
		}
		b.mu.Lock()
		_, active := b.active[activeKey(project, id)]
		b.mu.Unlock()
		if active {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, id)); err != nil {
			b.display("%s: prune %s: %v", project, id, err)
		}
	}
}
