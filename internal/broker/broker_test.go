package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/job"
	"actionbox/internal/protocol"
	"actionbox/internal/trust/trustfixtures"
	"actionbox/internal/verify"
)

func TestMain(m *testing.M) {
	code := m.Run()
	trustfixtures.Cleanup()
	os.Exit(code)
}

const testDefs = `schema_version = 1

[actions.build]
description = "Build"
invoker     = "agent"
argv        = ["make", "{target}"]
writes      = ["out"]
timeout     = "1h"
memory_max  = "1G"
[actions.build.params.target]
pattern     = "[a-z]+"
description = "make target"

[actions.deploy]
description = "Deploy"
invoker     = "owner"
argv        = ["deploy"]
`

func TestUnregisteredCwdIsRejected(t *testing.T) {
	h := newHarness(t)

	resp := h.call(protocol.Request{Op: protocol.OpListActions, Cwd: t.TempDir()})

	if resp.Error != "rejected: unregistered project" {
		t.Errorf("error %q", resp.Error)
	}
}

func TestSymlinkedCwdResolvesToProject(t *testing.T) {
	h := newHarness(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(h.projectPath("alpha"), link); err != nil {
		t.Fatal(err)
	}

	resp := h.call(protocol.Request{Op: protocol.OpListActions, Cwd: link})

	if resp.Error != "" || len(resp.Actions) != 1 {
		t.Errorf("response %+v", resp)
	}
}

func TestListActionsShowsAgentActionsOnly(t *testing.T) {
	h := newHarness(t)

	resp := h.call(protocol.Request{Op: protocol.OpListActions, Cwd: filepath.Join(h.projectPath("alpha"), "sub")})

	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	want := []definitions.ListedAction{{
		Name:        "build",
		Description: "Build",
		Params:      map[string]definitions.ListedParam{"target": {Pattern: "[a-z]+", Description: "make target"}},
	}}
	if fmt.Sprint(resp.Actions) != fmt.Sprint(want) {
		t.Errorf("actions %+v", resp.Actions)
	}
}

func TestListActionsWithSignedFixture(t *testing.T) {
	h := newFixtureHarness(t, "signed-clean")

	resp := h.call(protocol.Request{Op: protocol.OpListActions, Cwd: h.projectPath("signed-clean")})

	if resp.Error != "" || len(resp.Actions) != 1 || resp.Actions[0].Name != "lint" {
		t.Errorf("response %+v", resp)
	}
}

func TestUntrustedProjectIsRejected(t *testing.T) {
	h := newFixtureHarness(t, "unsigned-commit")
	cwd := h.projectPath("unsigned-commit")

	list := h.call(protocol.Request{Op: protocol.OpListActions, Cwd: cwd})
	run := h.call(protocol.Request{Op: protocol.OpRunAction, Cwd: cwd, Name: "lint"})

	if !strings.Contains(list.Error, "signature status") {
		t.Errorf("list error %q", list.Error)
	}
	if !strings.Contains(run.Error, "signature status") || run.Job == nil || run.Job.State != protocol.JobRejected {
		t.Errorf("run response %+v", run)
	}
	if h.runner.callCount() != 0 {
		t.Error("runner called")
	}
}

func TestRunActionSucceeds(t *testing.T) {
	h := newHarness(t)
	h.runner.fn = func(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
		fmt.Fprintln(out, "built")
		return job.Result{}, nil
	}

	resp := h.run("alpha", "build", map[string]string{"target": "all"})

	j := resp.Job
	if resp.Error != "" || j.State != protocol.JobSucceeded || j.ExitCode == nil || *j.ExitCode != 0 {
		t.Fatalf("response %+v", resp)
	}
	if j.OutputTail != "built\n" {
		t.Errorf("tail %q", j.OutputTail)
	}
	if j.LogPath != filepath.Join(h.paths.JobDir("alpha", j.ID), "log.txt") {
		t.Errorf("log path %q", j.LogPath)
	}
	spec := h.runner.calls()[0]
	if !slices.Equal(spec.Argv, []string{"make", "all"}) || spec.Project != "alpha" || spec.Action != "build" ||
		spec.RepoPath != h.projectPath("alpha") || !slices.Equal(spec.Writes, []string{"out"}) ||
		spec.Dir != h.paths.JobDir("alpha", j.ID) {
		t.Errorf("spec %+v", spec)
	}
	if spec.Limits.Timeout != 30*time.Minute || spec.Limits.MemoryMax != 1<<30 || spec.Limits.TasksMax != 4096 {
		t.Errorf("limits %+v", spec.Limits)
	}
	kinds := h.events.kinds()
	if !slices.Equal(kinds, []string{"job_start", "job_end"}) {
		t.Errorf("events %v", kinds)
	}
	end := h.events.last()
	if end.State != protocol.JobSucceeded || end.DefinitionsCommit != "c0ffee" || end.Params["target"] != "all" {
		t.Errorf("end event %+v", end)
	}
	if !strings.Contains(h.out.String(), j.ID) {
		t.Errorf("display %q", h.out.String())
	}
}

func TestJobDirLayout(t *testing.T) {
	h := newHarness(t)

	resp := h.run("alpha", "build", map[string]string{"target": "all"})

	dir := h.paths.JobDir("alpha", resp.Job.ID)
	for name, mode := range map[string]os.FileMode{"xdg": 0o700, "tmp": 0o700, "home": 0o700, "log.txt": 0o600, "job.json": 0o600} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode %v", name, info.Mode().Perm())
		}
	}
}

func TestRunActionResultStates(t *testing.T) {
	tests := []struct {
		name     string
		result   job.Result
		err      error
		state    protocol.JobState
		exitCode *int
	}{
		{"failed", job.Result{ExitCode: 2}, nil, protocol.JobFailed, ptr(2)},
		{"timed out", job.Result{ExitCode: 124, TimedOut: true}, nil, protocol.JobTimedOut, nil},
		{"killed", job.Result{ExitCode: 137, Killed: true}, nil, protocol.JobKilled, nil},
		{"runner error", job.Result{}, errors.New("bwrap missing"), protocol.JobFailed, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.runner.fn = func(context.Context, job.Spec, io.Writer) (job.Result, error) { return tc.result, tc.err }

			resp := h.run("alpha", "build", map[string]string{"target": "all"})

			j := resp.Job
			if j.State != tc.state || fmt.Sprint(deref(j.ExitCode)) != fmt.Sprint(deref(tc.exitCode)) {
				t.Errorf("job %+v", j)
			}
			if tc.err != nil && !strings.Contains(j.Detail, "bwrap missing") {
				t.Errorf("detail %q", j.Detail)
			}
		})
	}
}

func TestRunActionRejections(t *testing.T) {
	tests := []struct {
		name   string
		action string
		params map[string]string
		want   string
	}{
		{"owner-only", "deploy", nil, "not available"},
		{"unknown action", "nope", nil, "not available"},
		{"bad param", "build", map[string]string{"target": "ALL"}, "does not match"},
		{"leading dash", "build", map[string]string{"target": "-x"}, "leading"},
		{"unknown param", "build", map[string]string{"target": "a", "x": "b"}, "unknown param"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			resp := h.run("alpha", tc.action, tc.params)

			if resp.Job == nil || resp.Job.State != protocol.JobRejected || !strings.Contains(resp.Error, tc.want) {
				t.Errorf("response %+v", resp)
			}
			if h.runner.callCount() != 0 {
				t.Error("runner called")
			}
			if entries, _ := os.ReadDir(h.paths.JobsDir("alpha")); len(entries) != 0 {
				t.Errorf("job dirs %v", entries)
			}
			if kinds := h.events.kinds(); !slices.Equal(kinds, []string{"job_rejected"}) {
				t.Errorf("events %v", kinds)
			}
		})
	}
}

func TestRunActionReturnsRunningAfterWaitThenJobStatusSucceeds(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(o *Options) { o.Wait = 50 * time.Millisecond })
	h.runner.fn = func(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
		fmt.Fprintln(out, "working")
		<-release
		return job.Result{}, nil
	}

	resp := h.run("alpha", "build", map[string]string{"target": "all"})

	if resp.Job.State != protocol.JobRunning || resp.Job.ID == "" {
		t.Fatalf("response %+v", resp)
	}
	status := h.status("alpha", resp.Job.ID)
	if status.Job.State != protocol.JobRunning || status.Job.OutputTail != "working\n" {
		t.Errorf("status %+v", status.Job)
	}
	close(release)
	final := h.waitDone("alpha", resp.Job.ID)
	if final.State != protocol.JobSucceeded {
		t.Errorf("final %+v", final)
	}
}

func TestJobsRunFIFOPerProjectAndParallelAcrossProjects(t *testing.T) {
	gates := map[string]chan struct{}{}
	for _, target := range []string{"a", "b", "c", "x"} {
		gates[target] = make(chan struct{})
	}
	h := newHarness(t, func(o *Options) { o.Wait = 20 * time.Millisecond })
	h.runner.fn = func(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
		<-gates[spec.Argv[1]]
		return job.Result{}, nil
	}

	a := h.run("alpha", "build", map[string]string{"target": "a"}).Job
	b := h.run("alpha", "build", map[string]string{"target": "b"}).Job
	c := h.run("alpha", "build", map[string]string{"target": "c"}).Job
	x := h.run("beta", "build", map[string]string{"target": "x"}).Job

	if a.State != protocol.JobRunning || b.State != protocol.JobQueued || c.State != protocol.JobQueued {
		t.Fatalf("states %s %s %s", a.State, b.State, c.State)
	}
	if x.State != protocol.JobRunning {
		t.Errorf("beta state %s, want running in parallel", x.State)
	}
	close(gates["a"])
	h.waitDone("alpha", a.ID)
	h.waitState("alpha", b.ID, protocol.JobRunning)
	if s := h.status("alpha", c.ID).Job.State; s != protocol.JobQueued {
		t.Errorf("c state %s", s)
	}
	close(gates["b"])
	close(gates["c"])
	close(gates["x"])
	h.waitDone("alpha", c.ID)
	h.waitDone("beta", x.ID)
	var order []string
	for _, spec := range h.runner.calls() {
		if spec.Project == "alpha" {
			order = append(order, spec.Argv[1])
		}
	}
	if !slices.Equal(order, []string{"a", "b", "c"}) {
		t.Errorf("order %v", order)
	}
}

func TestJobWaitsForProjectLockHeldByAnotherProcess(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Wait = 50 * time.Millisecond })
	if err := os.MkdirAll(h.paths.ProjectDir("alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(h.paths.Lock("alpha"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	resp := h.run("alpha", "build", map[string]string{"target": "all"})

	if resp.Job.State != protocol.JobQueued || h.runner.callCount() != 0 {
		t.Fatalf("job %+v, calls %d", resp.Job, h.runner.callCount())
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if final := h.waitDone("alpha", resp.Job.ID); final.State != protocol.JobSucceeded {
		t.Errorf("final %+v", final)
	}
}

func TestJobStatusAfterBrokerRestart(t *testing.T) {
	h := newHarness(t)
	h.runner.fn = func(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
		fmt.Fprintln(out, "done")
		return job.Result{ExitCode: 3}, nil
	}
	id := h.run("alpha", "build", map[string]string{"target": "all"}).Job.ID
	h.stop()

	h2 := h.restart()
	resp := h2.status("alpha", id)

	j := resp.Job
	if resp.Error != "" || j.State != protocol.JobFailed || deref(j.ExitCode) != 3 || j.OutputTail != "done\n" {
		t.Errorf("response %+v", resp)
	}
}

func TestJobStatusReportsAbandonedJobAsKilled(t *testing.T) {
	h := newHarness(t)
	id := h.run("alpha", "build", map[string]string{"target": "all"}).Job.ID
	h.stop()
	rewriteState(t, filepath.Join(h.paths.JobDir("alpha", id), "job.json"), protocol.JobRunning)

	h2 := h.restart()
	j := h2.status("alpha", id).Job

	if j.State != protocol.JobKilled || !strings.Contains(j.Detail, "abandoned") {
		t.Errorf("job %+v", j)
	}
}

func TestJobStatusRejectsBadIDs(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"", "../alpha", "20261029-120000.000000-zzzzzzzz", "20261029-120000.000000-0000000a"} {
		resp := h.status("alpha", id)
		if resp.Error == "" {
			t.Errorf("id %q: response %+v", id, resp)
		}
	}
}

func TestFetchOpsNotImplemented(t *testing.T) {
	h := newHarness(t)
	for _, op := range []protocol.Op{protocol.OpRequestFetch, protocol.OpFetchStatus} {
		resp := h.call(protocol.Request{Op: op, Cwd: h.projectPath("alpha")})
		if resp.Error != "not implemented yet (M4)" {
			t.Errorf("%s: %q", op, resp.Error)
		}
	}
}

func TestMalformedRequests(t *testing.T) {
	h := newHarness(t)
	tests := map[string][]byte{
		"not json":      []byte("hello\n"),
		"unknown field": []byte(`{"op":"list_actions","cwd":"/","evil":1}` + "\n"),
		"unknown op":    []byte(`{"op":"rm","cwd":"/"}` + "\n"),
		"too large":     append(bytes.Repeat([]byte("x"), protocol.MaxRequestBytes+10), '\n'),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if resp := h.callRaw(raw); resp.Error == "" {
				t.Errorf("response %+v", resp)
			}
		})
	}
}

func TestPeerWithOtherUIDGetsNoResponse(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.PeerUID = func(net.Conn) (int, error) { return os.Getuid() + 1, nil }
	})

	conn := h.dial()
	defer conn.Close()
	fmt.Fprintf(conn, `{"op":"run_action","cwd":%q,"name":"build","params":{"target":"a"}}`+"\n", h.projectPath("alpha"))
	rest, _ := io.ReadAll(conn)

	if len(rest) != 0 {
		t.Errorf("got response %q", rest)
	}
	if h.runner.callCount() != 0 {
		t.Error("runner called")
	}
}

func TestPeerUIDOfRealSocketIsOwn(t *testing.T) {
	dir := shortTempDir(t)
	l, err := Listen(filepath.Join(dir, "s", "broker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := net.Dial("unix", l.Addr().String())
		if err == nil {
			defer c.Close()
			time.Sleep(100 * time.Millisecond)
		}
	}()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	uid, err := peerUID(conn)

	if err != nil || uid != os.Getuid() {
		t.Errorf("uid %d, err %v", uid, err)
	}
}

func TestListenCreatesPrivateSocket(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "state", "broker.sock")

	l, err := Listen(path)

	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "broker.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	l, err := Listen(path)

	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestListenRefusesLiveSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "broker.sock")
	live, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	_, err = Listen(path)

	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("err %v", err)
	}
}

func TestListenRefusesNonSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "broker.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Listen(path)

	if err == nil {
		t.Error("want error")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "keep" {
		t.Error("file removed")
	}
}

func TestRetentionPrunesOnStartupAndAfterJob(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	h := newStoppedHarness(t, func(o *Options) {
		o.Now = func() time.Time { return now }
		o.Env.Config.Retention = config.Retention{JobsPerProject: 3, MaxAge: 48 * time.Hour}
	})
	ages := []time.Duration{72 * time.Hour, 30 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour}
	var ids []string
	for i, age := range ages {
		id := fmt.Sprintf("%s-%08x", now.Add(-age).UTC().Format(idTimeLayout), i)
		ids = append(ids, id)
		if err := os.MkdirAll(h.paths.JobDir("alpha", id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Join(h.paths.JobsDir("alpha"), "not-a-job")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}

	h.start()

	if got := jobDirs(t, h, "alpha"); !slices.Equal(got, ids[2:]) {
		t.Errorf("after startup %v, want %v", got, ids[2:])
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("non-job dir pruned")
	}
	id := h.run("alpha", "build", map[string]string{"target": "all"}).Job.ID
	h.waitDone("alpha", id)
	if got := jobDirs(t, h, "alpha"); !slices.Equal(got, []string{ids[3], ids[4], id}) {
		t.Errorf("after job %v", got)
	}
}

func TestCancelKillsRunningAndQueuedJobs(t *testing.T) {
	started := make(chan struct{})
	h := newHarness(t, func(o *Options) { o.Wait = 20 * time.Millisecond })
	h.runner.fn = func(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
		close(started)
		<-ctx.Done()
		return job.Result{Killed: true, ExitCode: -1}, nil
	}
	running := h.run("alpha", "build", map[string]string{"target": "a"}).Job.ID
	<-started
	queued := h.run("alpha", "build", map[string]string{"target": "b"}).Job.ID

	h.stop()

	if _, err := net.Dial("unix", h.sock); err == nil {
		t.Error("still accepting")
	}
	for _, id := range []string{running, queued} {
		rec, err := readRecord(filepath.Join(h.paths.JobDir("alpha", id), "job.json"))
		if err != nil || rec.State != protocol.JobKilled {
			t.Errorf("%s: %+v %v", id, rec, err)
		}
	}
	if h.runner.callCount() != 1 {
		t.Errorf("calls %d", h.runner.callCount())
	}
}

func TestTail(t *testing.T) {
	var many strings.Builder
	for i := range 300 {
		fmt.Fprintf(&many, "line %d\n", i)
	}
	long := strings.Repeat("y", 100) + "\n"
	tests := []struct {
		name  string
		input string
		check func(string) bool
	}{
		{"empty", "", func(s string) bool { return s == "" }},
		{"short", "a\nb", func(s string) bool { return s == "a\nb" }},
		{"lines", many.String(), func(s string) bool {
			return strings.Count(s, "\n") == protocol.TailLines && strings.HasPrefix(s, "line 100\n")
		}},
		{"bytes", strings.Repeat(long, 199), func(s string) bool {
			return len(s) <= protocol.TailBytes && strings.HasSuffix(s, long)
		}},
		{"utf8 cut", "a" + strings.Repeat("é", protocol.TailBytes), func(s string) bool {
			return len(s) <= protocol.TailBytes && strings.HasPrefix(s, "é")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "log.txt")
			if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := tail(path)
			if err != nil || !tc.check(got) {
				t.Errorf("tail len %d %q..., err %v", len(got), first(got, 40), err)
			}
		})
	}
}

// --- harness ---

type harness struct {
	t       *testing.T
	opts    Options
	paths   config.Paths
	runner  *fakeRunner
	events  *eventRecorder
	out     *syncBuffer
	sock    string
	cancel  context.CancelFunc
	done    chan error
	stopped bool
}

func newHarness(t *testing.T, mods ...func(*Options)) *harness {
	t.Helper()
	h := newStoppedHarness(t, mods...)
	h.start()
	return h
}

func newStoppedHarness(t *testing.T, mods ...func(*Options)) *harness {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.Project{}}
	for _, name := range []string{"alpha", "beta"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(path, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.Projects[name] = config.Project{Name: name, Path: path}
	}
	defs, err := definitions.Parse([]byte(testDefs))
	if err != nil {
		t.Fatal(err)
	}
	fakeVerify := func(_ context.Context, _ verify.Env, name string) (verify.Outcome, error) {
		return verify.Outcome{Project: name, Commit: "c0ffee", Definitions: defs}, nil
	}
	return buildHarness(t, cfg, fakeVerify, mods)
}

func newFixtureHarness(t *testing.T, caseName string) *harness {
	t.Helper()
	set := trustfixtures.Load(t)
	c := set.Case(t, caseName)
	cfg := &config.Config{
		PinnedFingerprint: c.Pin,
		Projects:          map[string]config.Project{caseName: {Name: caseName, Path: filepath.Join(set.Dir, c.Repo)}},
	}
	h := buildHarness(t, cfg, nil, nil)
	copyFile(t, filepath.Join(set.Dir, c.Keyring), h.paths.Keyring())
	h.start()
	return h
}

func buildHarness(t *testing.T, cfg *config.Config, verifyFn VerifyFunc, mods []func(*Options)) *harness {
	t.Helper()
	h := &harness{
		t:      t,
		paths:  config.PathsForHome(t.TempDir()),
		runner: &fakeRunner{},
		events: &eventRecorder{},
		out:    &syncBuffer{},
	}
	h.opts = Options{
		Env:    verify.Env{Config: cfg, Paths: h.paths},
		Runner: h.runner,
		Events: h.events,
		Out:    h.out,
		Verify: verifyFn,
		Wait:   5 * time.Second,
	}
	for _, mod := range mods {
		mod(&h.opts)
	}
	h.sock = filepath.Join(shortTempDir(t), "broker.sock")
	t.Cleanup(h.stop)
	return h
}

func (h *harness) start() {
	h.t.Helper()
	b := New(h.opts)
	l, err := Listen(h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done, h.stopped = cancel, make(chan error, 1), false
	go func() { h.done <- b.Serve(ctx, l) }()
}

func (h *harness) stop() {
	if h.stopped || h.cancel == nil {
		return
	}
	h.stopped = true
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		h.t.Error("serve did not return")
	}
}

func (h *harness) restart() *harness {
	h2 := *h
	h2.start()
	h.t.Cleanup(h2.stop)
	return &h2
}

func (h *harness) projectPath(name string) string {
	return h.opts.Env.Config.Projects[name].Path
}

func (h *harness) dial() net.Conn {
	h.t.Helper()
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	return conn
}

func (h *harness) callRaw(raw []byte) protocol.Response {
	h.t.Helper()
	conn := h.dial()
	defer conn.Close()
	go conn.Write(raw)
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		h.t.Fatalf("read response: %v", err)
	}
	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) call(req protocol.Request) protocol.Response {
	h.t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.callRaw(append(raw, '\n'))
}

func (h *harness) run(project, action string, params map[string]string) protocol.Response {
	h.t.Helper()
	resp := h.call(protocol.Request{Op: protocol.OpRunAction, Cwd: h.projectPath(project), Name: action, Params: params})
	if resp.Job == nil {
		h.t.Fatalf("no job in %+v", resp)
	}
	return resp
}

func (h *harness) status(project, id string) protocol.Response {
	h.t.Helper()
	return h.call(protocol.Request{Op: protocol.OpJobStatus, Cwd: h.projectPath(project), JobID: id})
}

func (h *harness) waitState(project, id string, want protocol.JobState) protocol.Job {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := h.status(project, id)
		if resp.Job != nil && (resp.Job.State == want || want == "" && resp.Job.State.Done()) {
			return *resp.Job
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("job %s: %+v, want %q", id, resp, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) waitDone(project, id string) protocol.Job {
	h.t.Helper()
	return h.waitState(project, id, "")
}

type fakeRunner struct {
	mu    sync.Mutex
	specs []job.Spec
	fn    func(context.Context, job.Spec, io.Writer) (job.Result, error)
}

func (r *fakeRunner) Run(ctx context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	fn := r.fn
	r.mu.Unlock()
	if fn == nil {
		return job.Result{}, nil
	}
	return fn(ctx, spec, out)
}

func (r *fakeRunner) calls() []job.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.specs)
}

func (r *fakeRunner) callCount() int { return len(r.calls()) }

type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (e *eventRecorder) Append(event any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event.(Event))
	return nil
}

func (e *eventRecorder) kinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var kinds []string
	for _, ev := range e.events {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

func (e *eventRecorder) last() Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events[len(e.events)-1]
}

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

// unix socket paths max 108 bytes, t.TempDir() can exceed that
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "abk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func jobDirs(t *testing.T, h *harness, project string) []string {
	t.Helper()
	entries, err := os.ReadDir(h.paths.JobsDir(project))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		if validJobID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func rewriteState(t *testing.T, path string, state protocol.JobState) {
	t.Helper()
	rec, err := readRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	rec.State, rec.ExitCode = state, nil
	if err := writeRecord(path, rec); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), want)
	}
}

func ptr(i int) *int { return &i }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func first(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
