package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/eventlog"
	"actionbox/internal/job"
	"actionbox/internal/jobrun"
	"actionbox/internal/protocol"
	"actionbox/internal/trust/trustfixtures"
	"actionbox/internal/verify"
)

const runTestDefs = `schema_version = 1

[actions.build]
description = "Build"
invoker     = "agent"
argv        = ["make", "{target}"]
[actions.build.params.target]
pattern     = "[a-z]+"
description = "make target"

[actions.deploy]
description = "Deploy"
invoker     = "owner"
argv        = ["deploy", "{env}"]
[actions.deploy.params.env]
pattern     = "[a-z]+"
description = "target env"

[actions.fetch]
description = "Fetch coordinates"
invoker     = "owner"
kind        = "coordinate-fetch"
`

func TestParseRunArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    runArgs
		wantErr string
	}{
		{args: []string{"build"}, want: runArgs{action: "build", params: map[string]string{}}},
		{args: []string{"build", "target=all", "x=a=b"}, want: runArgs{action: "build", params: map[string]string{"target": "all", "x": "a=b"}}},
		{args: []string{"--project", "alpha", "build"}, want: runArgs{project: "alpha", action: "build", params: map[string]string{}}},
		{args: []string{"build", "--project=alpha", "a="}, want: runArgs{project: "alpha", action: "build", params: map[string]string{"a": ""}}},
		{args: nil, wantErr: "needs an action"},
		{args: []string{"--project"}, wantErr: "--project needs a name"},
		{args: []string{"--bogus", "build"}, wantErr: "unknown flag"},
		{args: []string{"build", "target"}, wantErr: `malformed parameter "target"`},
		{args: []string{"build", "=all"}, wantErr: `malformed parameter "=all"`},
		{args: []string{"build", "a=1", "a=2"}, wantErr: `duplicate parameter "a"`},
	}
	for _, tt := range tests {
		got, err := parseRunArgs(tt.args)
		var u usageError
		switch {
		case tt.wantErr != "" && (!errors.As(err, &u) || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("parseRunArgs(%q) error %v, want usage error %q", tt.args, err, tt.wantErr)
		case tt.wantErr == "" && (err != nil || got.project != tt.want.project || got.action != tt.want.action || fmt.Sprint(got.params) != fmt.Sprint(tt.want.params)):
			t.Errorf("parseRunArgs(%q) = %+v, %v", tt.args, got, err)
		}
	}
}

func TestRunOwnerOnlyActionStreamsOutputAndLogsEvents(t *testing.T) {
	r := newRunHarness(t)
	r.runner.fn = func(_ context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
		fmt.Fprintln(out, "deployed "+spec.Argv[1])
		return job.Result{}, nil
	}
	t.Chdir(filepath.Join(r.projectDir, "sub"))

	code, stdout, stderr := r.run(context.Background(), "deploy", "env=prod")

	if code != 0 || !strings.Contains(stdout, "deployed prod\n") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	specs := r.runner.calls()
	if len(specs) != 1 || !slices.Equal(specs[0].Argv, []string{"deploy", "prod"}) || specs[0].RepoPath != r.projectDir {
		t.Fatalf("specs %+v", specs)
	}
	dir := specs[0].Dir
	if log, err := os.ReadFile(filepath.Join(dir, "log.txt")); err != nil || string(log) != "deployed prod\n" {
		t.Errorf("log.txt %q, %v", log, err)
	}
	rec, err := jobrun.ReadRecord(filepath.Join(dir, "job.json"))
	if err != nil || rec.State != protocol.JobSucceeded || rec.Invoker != definitions.InvokerOwner || rec.DefinitionsCommit != "c0ffee" {
		t.Errorf("job.json %+v, %v", rec, err)
	}
	events := r.events()
	if len(events) != 2 || events[0]["kind"] != "job_start" || events[1]["kind"] != "job_end" {
		t.Fatalf("events %v", events)
	}
	end := events[1]
	if end["invoker"] != "owner" || end["state"] != "succeeded" || end["exit_code"] != 0.0 || end["job_id"] != rec.ID ||
		end["definitions_commit"] != "c0ffee" || fmt.Sprint(end["params"]) != "map[env:prod]" {
		t.Errorf("job_end %v", end)
	}
	if _, err := eventlog.Verify(r.paths.Events()); err != nil {
		t.Errorf("event log chain: %v", err)
	}
}

func TestRunExitCodeFollowsJob(t *testing.T) {
	tests := []struct {
		name      string
		result    job.Result
		err       error
		wantCode  int
		wantState protocol.JobState
	}{
		{"succeeded", job.Result{}, nil, 0, protocol.JobSucceeded},
		{"failed", job.Result{ExitCode: 3}, nil, 3, protocol.JobFailed},
		{"timed out", job.Result{TimedOut: true, ExitCode: -1}, nil, 124, protocol.JobTimedOut},
		{"killed", job.Result{Killed: true, ExitCode: -1}, nil, 137, protocol.JobKilled},
		{"not isolated", job.Result{}, errors.New("bwrap missing"), 1, protocol.JobFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunHarness(t)
			r.runner.fn = func(context.Context, job.Spec, io.Writer) (job.Result, error) { return tt.result, tt.err }

			code, stdout, stderr := r.run(context.Background(), "--project", "alpha", "deploy", "env=prod")

			if code != tt.wantCode || !strings.Contains(stdout+stderr, string(tt.wantState)) {
				t.Errorf("code %d, output %q; want %d, %s", code, stdout+stderr, tt.wantCode, tt.wantState)
			}
		})
	}
}

func TestRunRejectsBeforeRunningJob(t *testing.T) {
	tests := []struct {
		args     []string
		wantCode int
		wantOut  string
	}{
		{args: []string{"--project", "alpha"}, wantCode: 2, wantOut: "needs an action"},
		{args: []string{"--project", "alpha", "deploy", "env"}, wantCode: 2, wantOut: "malformed parameter"},
		{args: []string{"--project", "alpha", "deploy", "env=a", "env=b"}, wantCode: 2, wantOut: "duplicate parameter"},
		{args: []string{"--project", "alpha", "deploy", "env=PROD"}, wantCode: 1, wantOut: "env"},
		{args: []string{"--project", "alpha", "deploy"}, wantCode: 1, wantOut: "env"},
		{args: []string{"--project", "alpha", "deploy", "env=a", "x=1"}, wantCode: 1, wantOut: "x"},
		{args: []string{"--project", "alpha", "nope"}, wantCode: 1, wantOut: `no action "nope"`},
		{args: []string{"--project", "alpha", "fetch"}, wantCode: 1, wantOut: "use actionbox fetch"},
		{args: []string{"--project", "missing", "deploy"}, wantCode: 1, wantOut: `unregistered project "missing"`},
		{args: []string{"deploy", "env=a"}, wantCode: 1, wantOut: "not inside a registered project"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			r := newRunHarness(t)
			t.Chdir(t.TempDir())

			code, stdout, stderr := r.run(context.Background(), tt.args...)

			if code != tt.wantCode || !strings.Contains(stdout+stderr, tt.wantOut) {
				t.Errorf("code %d, output %q; want %d containing %q", code, stdout+stderr, tt.wantCode, tt.wantOut)
			}
			if r.runner.callCount() != 0 || len(r.events()) != 0 {
				t.Errorf("runner calls %d, events %v", r.runner.callCount(), r.events())
			}
		})
	}
}

func TestRunRefusesUntrustedProject(t *testing.T) {
	set := trustfixtures.Load(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, set, map[string]string{"unsigned": "unsigned-commit"})
	runner := &fakeRunner{}
	stubRunner(t, runner)
	t.Chdir(filepath.Join(set.Dir, set.Case(t, "unsigned-commit").Repo))

	var stdout, stderr bytes.Buffer
	code := runExitCode(runRun(context.Background(), []string{"lint"}, &stdout), &stderr)

	if code != 1 || !strings.Contains(stderr.String(), "unsigned: untrusted: signature status") {
		t.Errorf("code %d, stderr %q", code, stderr.String())
	}
	if runner.callCount() != 0 {
		t.Error("runner called")
	}
}

func TestRunWaitsForProjectLock(t *testing.T) {
	r := newRunHarness(t)
	unlock, err := jobrun.Lock(context.Background(), r.paths, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		code, _, _ := r.run(context.Background(), "--project", "alpha", "deploy", "env=prod")
		done <- code
	}()

	time.Sleep(200 * time.Millisecond)
	if r.runner.callCount() != 0 {
		t.Fatal("job ran while the project lock was held")
	}
	unlock()

	select {
	case code := <-done:
		if code != 0 || r.runner.callCount() != 1 {
			t.Errorf("code %d, calls %d", code, r.runner.callCount())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish after the lock was released")
	}
	if !strings.Contains(r.stdout.String(), "waiting for the project lock") {
		t.Errorf("stdout %q", r.stdout.String())
	}
}

func TestRunCancelWhileWaitingForLock(t *testing.T) {
	r := newRunHarness(t)
	unlock, err := jobrun.Lock(context.Background(), r.paths, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	code, stdout, stderr := r.run(ctx, "--project", "alpha", "deploy", "env=prod")

	if code != 1 || !strings.Contains(stdout+stderr, "cancelled before the job started") {
		t.Errorf("code %d, output %q", code, stdout+stderr)
	}
	if r.runner.callCount() != 0 || len(r.events()) != 0 {
		t.Errorf("calls %d, events %v", r.runner.callCount(), r.events())
	}
}

func TestRunPrunesOldJobs(t *testing.T) {
	r := newRunHarness(t)
	old := fmt.Sprintf("%s-%08x", time.Now().Add(-15*24*time.Hour).UTC().Format(jobrun.IDTimeLayout), 0)
	if err := os.MkdirAll(r.paths.JobDir("alpha", old), 0o700); err != nil {
		t.Fatal(err)
	}

	if code, stdout, stderr := r.run(context.Background(), "--project", "alpha", "deploy", "env=prod"); code != 0 {
		t.Fatalf("code %d, output %q", code, stdout+stderr)
	}

	if _, err := os.Stat(r.paths.JobDir("alpha", old)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old job dir kept: %v", err)
	}
}

// --- harness ---

type runHarness struct {
	t          *testing.T
	paths      config.Paths
	projectDir string
	runner     *fakeRunner
	stdout     *syncBuffer
}

func newRunHarness(t *testing.T) *runHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projectDir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := config.PathsForHome(home)
	cfg := "pinned_fingerprint = \"" + strings.Repeat("A", 40) + "\"\nbwrap_path = \"/usr/bin/bwrap\"\n\n" +
		"[projects.alpha]\npath = \"" + projectDir + "\"\n"
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	defs, err := definitions.Parse([]byte(runTestDefs))
	if err != nil {
		t.Fatal(err)
	}
	prevVerify := verifyProject
	verifyProject = func(_ context.Context, env verify.Env, name string) (verify.Outcome, error) {
		if _, ok := env.Config.Projects[name]; !ok {
			return verify.Outcome{}, fmt.Errorf("unregistered project %q", name)
		}
		return verify.Outcome{Project: name, Commit: "c0ffee", Definitions: defs}, nil
	}
	t.Cleanup(func() { verifyProject = prevVerify })
	r := &runHarness{t: t, paths: paths, projectDir: projectDir, runner: &fakeRunner{}, stdout: &syncBuffer{}}
	stubRunner(t, r.runner)
	return r
}

func stubRunner(t *testing.T, runner job.Runner) {
	prev := newJobRunner
	newJobRunner = func(verify.Env) (job.Runner, error) { return runner, nil }
	t.Cleanup(func() { newJobRunner = prev })
}

func (r *runHarness) run(ctx context.Context, args ...string) (int, string, string) {
	r.t.Helper()
	var stderr bytes.Buffer
	before := len(r.stdout.String())
	code := runExitCode(runRun(ctx, args, r.stdout), &stderr)
	return code, r.stdout.String()[before:], stderr.String()
}

func (r *runHarness) events() []map[string]any {
	r.t.Helper()
	f, err := os.Open(r.paths.Events())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	var events []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			r.t.Fatal(err)
		}
		events = append(events, ev)
	}
	return events
}

type fakeRunner struct {
	mu    sync.Mutex
	specs []job.Spec
	fn    func(context.Context, job.Spec, io.Writer) (job.Result, error)
}

func (f *fakeRunner) Run(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return job.Result{}, nil
	}
	return fn(ctx, spec, out)
}

func (f *fakeRunner) calls() []job.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.specs)
}

func (f *fakeRunner) callCount() int { return len(f.calls()) }

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
