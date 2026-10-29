package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"actionbox/internal/config"
	"actionbox/internal/engine"
	"actionbox/internal/job"
)

type respondFunc func(spec job.Spec, binds *engine.Binds) (job.Result, string)

// fakeRunner opens the real binds, as the engine does, then answers instead of starting bwrap.
type fakeRunner struct {
	afterBinds func()
	respond    respondFunc
	specs      *[]job.Spec
}

func (f fakeRunner) Run(_ context.Context, spec job.Spec, out io.Writer) (job.Result, error) {
	*f.specs = append(*f.specs, spec)
	binds, err := engine.PrepareBinds(spec.RepoPath, spec.Writes)
	if err != nil {
		return job.Result{}, err
	}
	defer binds.Close()
	if f.afterBinds != nil {
		f.afterBinds()
	}
	res, text := f.respond(spec, binds)
	_, _ = io.WriteString(out, text)
	return res, nil
}

type fakePodman struct{ hashes []string }

func (p *fakePodman) Seed(context.Context, string, string) error { return nil }
func (p *fakePodman) HashTree(context.Context, string) (string, error) {
	h := p.hashes[0]
	if len(p.hashes) > 1 {
		p.hashes = p.hashes[1:]
	}
	return h, nil
}
func (p *fakePodman) RemoveAll(context.Context, string) error { return nil }

func probeOutput(name string, l Line) string {
	l.Name = name
	return l.String() + "\n"
}

// viaFD writes into the writes dir through its fd, like bwrap's --bind-fd mount.
func viaFD(spec job.Spec, binds *engine.Binds, path string) string {
	prefix := filepath.Join(spec.RepoPath, writesDir) + "/"
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return path
	}
	return fmt.Sprintf("/proc/self/fd/%d/%s", binds.Files()[1].Fd(), rest)
}

// isolated answers like a correctly isolated sandbox.
func isolated(spec job.Spec, binds *engine.Binds) (job.Result, string) {
	args := spec.Argv[2:]
	name := args[0]
	switch name {
	case "write-ok":
		return job.Result{}, probeOutput(name, writeOK(viaFD(spec, binds, args[1])))
	case "env":
		var environ []string
		for _, n := range strings.Split(args[1], ",") {
			environ = append(environ, n+"=x")
		}
		return job.Result{}, probeOutput(name, envProbe(environ, args[1:]))
	case "mem":
		return job.Result{ExitCode: 137, Killed: true}, ""
	case "sleep":
		return job.Result{ExitCode: 124, TimedOut: true}, ""
	}
	return job.Result{}, probeOutput(name, ok("simulated"))
}

// leaky runs the file and env probes in-process, i.e. without any isolation.
func leaky(spec job.Spec, binds *engine.Binds) (job.Result, string) {
	args := spec.Argv[2:]
	name := args[0]
	switch name {
	case "write-ok", "write-denied", "read-denied":
		return job.Result{}, probeOutput(name, probes[name](context.Background(), args[1:]))
	case "env":
		return job.Result{}, probeOutput(name, envProbe(os.Environ(), args[1:]))
	case "connect-denied":
		if strings.Contains(args[1], "127.0.0.1") {
			return job.Result{}, probeOutput(name, connectDenied(context.Background(), args[1:]))
		}
	}
	return isolated(spec, binds)
}

type testHost struct {
	host  Host
	specs []job.Spec
	base  string
}

func newTestHost(t *testing.T, respond respondFunc) *testHost {
	t.Helper()
	th := &testHost{base: t.TempDir()}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	th.host = Host{
		NewRunner: func(_ config.Paths, afterBinds func()) job.Runner {
			return fakeRunner{afterBinds: afterBinds, respond: respond, specs: &th.specs}
		},
		Config:  &config.Config{BwrapPath: "/usr/bin/bwrap", Toolchain: config.Toolchain{Env: map[string]string{"JAVA_HOME": "/jdk"}}},
		SelfExe: "/opt/actionbox",
		Home:    home,
		Podman:  &fakePodman{hashes: []string{"h1"}},
		Dial:    func(context.Context, string, string) error { return nil },
	}
	return th
}

func statuses(results []Result) map[string]Status {
	out := map[string]Status{}
	for _, r := range results {
		out[r.ID+" "+r.Mode] = r.Status
	}
	return out
}

func TestRunIsolatedSandboxPassesEverything(t *testing.T) {
	// given
	th := newTestHost(t, isolated)
	var out bytes.Buffer

	// when
	results, err := Run(context.Background(), th.host, th.base, &out)

	// then
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != Pass {
			t.Errorf("%s", r)
		}
	}
	got := statuses(results)
	for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8", "P9", "P10", "P11",
		"P12-mem", "P12-tasks", "P12-timeout", "P13", "P14"} {
		for _, c := range []bool{false, true} {
			if _, ok := got[id+" "+ModeName(c)]; !ok {
				t.Errorf("missing %s %s", id, ModeName(c))
			}
		}
	}
	if _, ok := got["P15 containers=false"]; ok {
		t.Error("P15 ran without containers")
	}
	if got["P15 containers=true"] != Pass {
		t.Error("P15 missing in container mode")
	}
	if strings.Count(out.String(), "\n") != len(results) {
		t.Errorf("streamed %q", out.String())
	}
	if _, set := os.LookupEnv(CanaryEnv); set {
		t.Error("canary left in env")
	}
}

func TestRunJobSpecs(t *testing.T) {
	// given
	th := newTestHost(t, isolated)
	th.host.Config.Caps = config.DefaultCaps

	// when
	if _, err := Run(context.Background(), th.host, th.base, io.Discard); err != nil {
		t.Fatal(err)
	}

	// then
	for _, s := range th.specs {
		if s.Argv[0] != "/opt/actionbox" || s.Argv[1] != "probe" || !slices.Equal(s.Writes, []string{"target"}) {
			t.Errorf("spec %s: argv %q writes %q", s.ID, s.Argv, s.Writes)
		}
		if !strings.HasPrefix(s.Dir, th.base) || !strings.HasPrefix(s.RepoPath, th.base) {
			t.Errorf("spec %s outside base: %s %s", s.ID, s.Dir, s.RepoPath)
		}
		if s.Limits.Timeout == 0 || s.Limits.MemoryMax == 0 {
			t.Errorf("spec %s limits %+v", s.ID, s.Limits)
		}
		switch {
		case s.ID == "p7" && s.Containers:
			if s.Argv[3] != s.Dir+"/xdg/podman.sock" {
				t.Errorf("p7 allowed sockets %q", s.Argv[3:])
			}
		case s.ID == "p7":
			if len(s.Argv) != 3 {
				t.Errorf("p7 allowed sockets %q", s.Argv[3:])
			}
		case s.ID == "p10":
			allowed := strings.Split(s.Argv[3], ",")
			for _, want := range []string{"PATH", "HOME", "JAVA_HOME", "npm_config_cache"} {
				if !slices.Contains(allowed, want) {
					t.Errorf("p10 allowlist lacks %s: %v", want, allowed)
				}
			}
			if slices.Contains(allowed, "DOCKER_HOST") != s.Containers {
				t.Errorf("p10 containers=%t allowlist %v", s.Containers, allowed)
			}
		case s.ID == "p12-mem":
			if s.Limits.MemoryMax != p12MemoryMax {
				t.Errorf("p12-mem limits %+v", s.Limits)
			}
		}
	}
}

func TestRunLeakySandboxFails(t *testing.T) {
	// given
	th := newTestHost(t, leaky)

	// when
	results, err := Run(context.Background(), th.host, th.base, io.Discard)

	// then
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(results)
	for _, id := range []string{"P2", "P3", "P4", "P6", "P10", "P13", "P14"} {
		if s := got[id+" "+ModeName(false)]; s != Fail {
			t.Errorf("%s = %s, want FAIL", id, s)
		}
	}
	if got["P1 "+ModeName(false)] != Pass {
		t.Error("P1 should pass in-process")
	}
}

func TestRunMissingProbeLineFails(t *testing.T) {
	// given
	th := newTestHost(t, func(job.Spec, *engine.Binds) (job.Result, string) {
		return job.Result{ExitCode: 125}, "actionbox job-init: podman service not ready\n"
	})

	// when
	results, _ := Run(context.Background(), th.host, th.base, io.Discard)

	// then
	for _, r := range results {
		if r.ID == "P8" {
			continue
		}
		if r.Status == Pass {
			t.Errorf("%s passed without probe output", r)
		}
	}
	if !strings.Contains(results[0].Detail, "podman service not ready") {
		t.Errorf("detail lacks output: %s", results[0].Detail)
	}
}

func TestRunP15DetectsStoreChange(t *testing.T) {
	th := newTestHost(t, isolated)
	th.host.Podman = &fakePodman{hashes: []string{"before", "after"}}

	results, _ := Run(context.Background(), th.host, th.base, io.Discard)

	if s := statuses(results)["P15 containers=true"]; s != Fail {
		t.Errorf("P15 = %s", s)
	}
}

func TestRunP15SkipsWithoutPodman(t *testing.T) {
	th := newTestHost(t, isolated)
	th.host.Podman = nil

	results, _ := Run(context.Background(), th.host, th.base, io.Discard)

	if s := statuses(results)["P15 containers=true"]; s != Skip {
		t.Errorf("P15 = %s", s)
	}
}

func TestRunWarnsOnVacuousProbes(t *testing.T) {
	th := newTestHost(t, isolated)
	th.host.Home = t.TempDir()
	th.host.Dial = func(context.Context, string, string) error { return errors.New("offline") }

	got := statuses(mustRun(t, th))

	if got["P4 "+ModeName(false)] != Warn || got["P5 "+ModeName(false)] != Warn {
		t.Errorf("P4 = %s, P5 = %s", got["P4 "+ModeName(false)], got["P5 "+ModeName(false)])
	}
}

func mustRun(t *testing.T, th *testHost) []Result {
	t.Helper()
	results, err := Run(context.Background(), th.host, th.base, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func TestJudgeP9(t *testing.T) {
	okLine := []byte(probeOutput("write-ok", ok("wrote")))
	tests := []struct {
		name       string
		job        jobRun
		after      []string
		inOriginal bool
		want       Status
	}{
		{"fd mount", jobRun{output: okLine}, []string{"keep"}, true, Pass},
		{"victim written", jobRun{output: okLine}, []string{"keep", "p9"}, false, Fail},
		{"job-init backstop", jobRun{result: job.Result{ExitCode: 125}, output: []byte("mount point changed")}, []string{"keep"}, false, Pass},
		{"bwrap race check", jobRun{result: job.Result{ExitCode: 1}, output: []byte("bwrap: Race condition binding dirfd")}, []string{"keep"}, false, Pass},
		{"unexplained exit", jobRun{result: job.Result{ExitCode: 1}}, []string{"keep"}, false, Fail},
		{"wrote somewhere else", jobRun{output: okLine}, []string{"keep"}, false, Fail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, d := judgeP9(tt.job, nil, true, []string{"keep"}, tt.after, tt.inOriginal); got != tt.want {
				t.Errorf("got %s (%s)", got, d)
			}
		})
	}
	if got, _ := judgeP9(jobRun{output: okLine}, errors.New("EXDEV"), false, nil, nil, true); got != Fail {
		t.Error("failed swap must fail")
	}
}

func TestJudgeLimits(t *testing.T) {
	memNotKilled := jobRun{output: []byte(probeOutput("mem", fail("allocated")))}
	if s, _ := judgeMem(memNotKilled); s != Fail {
		t.Errorf("mem not killed = %s", s)
	}
	if s, _ := judgeMem(jobRun{result: job.Result{ExitCode: 137, TimedOut: true}}); s != Fail {
		t.Errorf("mem timed out = %s", s)
	}
	if s, _ := judgeTasks(jobRun{output: []byte(probeOutput("tasks", fail("started 300")))}); s != Fail {
		t.Errorf("tasks unbounded = %s", s)
	}
	if s, _ := judgeTasks(jobRun{result: job.Result{ExitCode: 2}, output: []byte("runtime: failed to create new OS thread")}); s != Warn {
		t.Errorf("tasks runtime crash = %s", s)
	}
	if s, _ := judgeTimeout(jobRun{output: []byte(probeOutput("sleep", fail("slept")))}); s != Fail {
		t.Errorf("sleep not stopped = %s", s)
	}
}

func TestJudgeP15WarnsWhenStoreNotExercised(t *testing.T) {
	j := jobRun{output: []byte(probeOutput("podman", fail("import: exit 125")))}
	if s, _ := judgeP15(j, "h", "h", ""); s != Warn {
		t.Errorf("got %s", s)
	}
	if s, _ := judgeP15(jobRun{result: job.Result{ExitCode: 125}}, "h", "h", ""); s != Fail {
		t.Errorf("job-init failure = %s", s)
	}
}

func TestSetenvNamesStopsAtArgv(t *testing.T) {
	got := SetenvNames([]string{"--clearenv", "--setenv", "A", "1", "--setenv", "B", "--x", "--", "--setenv", "C", "3"})
	if !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("got %v", got)
	}
}
