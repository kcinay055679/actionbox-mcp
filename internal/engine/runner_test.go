package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/job"
	"actionbox/internal/limits"
)

func exited(code int) syscall.WaitStatus             { return syscall.WaitStatus(code << 8) }
func signaled(sig syscall.Signal) syscall.WaitStatus { return syscall.WaitStatus(sig) }

func TestResultFor(t *testing.T) {
	timeout := 10 * time.Minute
	tests := []struct {
		name    string
		ws      syscall.WaitStatus
		elapsed time.Duration
		want    job.Result
	}{
		{"success", exited(0), time.Minute, job.Result{ExitCode: 0}},
		{"failure", exited(1), time.Minute, job.Result{ExitCode: 1}},
		{"timeout", exited(124), timeout, job.Result{ExitCode: 124, TimedOut: true}},
		{"timeout then kill-after", exited(137), timeout + 30*time.Second, job.Result{ExitCode: 137, TimedOut: true}},
		{"oom kill before timeout", exited(137), time.Minute, job.Result{ExitCode: 137, Killed: true}},
		{"child got SIGTERM", exited(143), time.Minute, job.Result{ExitCode: 143, Killed: true}},
		{"systemd-run signaled", signaled(syscall.SIGKILL), time.Minute, job.Result{ExitCode: 137, Killed: true}},
		{"job-init abort", exited(125), time.Second, job.Result{ExitCode: 125}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resultFor(tt.ws, tt.elapsed, timeout); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

type testEnv struct {
	runner *Runner
	paths  config.Paths
	spec   job.Spec
	cmd    *exec.Cmd
}

func newTestEnv(t *testing.T, containers bool, status syscall.WaitStatus) *testEnv {
	t.Helper()
	base := t.TempDir()
	paths := config.PathsForHome(base)
	repo := filepath.Join(base, "projects", "p")
	mkdirs(t, repo, ".git")
	jobDir := paths.JobDir("p", "j1")
	mkdirs(t, jobDir)
	cfg := &config.Config{BwrapPath: "/opt/bwrap", Toolchain: config.Toolchain{Env: map[string]string{}}}
	te := &testEnv{paths: paths}
	r := NewRunner(cfg, paths, "/opt/actionbox")
	r.home = base
	r.environ = func() []string { return []string{"PATH=/usr/bin", "SECRET=x"} }
	r.run = func(cmd *exec.Cmd) (syscall.WaitStatus, error) {
		te.cmd = cmd
		return status, nil
	}
	te.runner = r
	te.spec = job.Spec{
		ID: "j1", Project: "p", RepoPath: repo, Dir: jobDir, Action: "test",
		Argv: []string{"mvn", "test"}, Writes: []string{"target"}, Containers: containers,
		Limits: limits.Limits{Timeout: time.Minute, MemoryMax: 1 << 30, TasksMax: 10, CPUQuota: 100},
	}
	return te
}

func TestRunnerBuildsCommand(t *testing.T) {
	// given
	te := newTestEnv(t, false, exited(3))
	var out bytes.Buffer

	// when
	res, err := te.runner.Run(context.Background(), te.spec, &out)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if res != (job.Result{ExitCode: 3}) {
		t.Errorf("result = %+v", res)
	}
	cmd := te.cmd
	if cmd.Path != "systemd-run" && filepath.Base(cmd.Path) != "systemd-run" {
		t.Errorf("path = %s", cmd.Path)
	}
	if !slices.Contains(cmd.Args, "--bind-fd") || cmd.Args[len(cmd.Args)-1] != "test" {
		t.Errorf("args = %q", cmd.Args)
	}
	if !slices.Equal(cmd.Env, []string{"PATH=/usr/bin"}) {
		t.Errorf("env = %q", cmd.Env)
	}
	if cmd.Stdout != &out || cmd.Stderr != &out {
		t.Error("output not wired")
	}
	if len(cmd.ExtraFiles) != 3 {
		t.Errorf("extra files = %d, want repo + target + .git", len(cmd.ExtraFiles))
	}
	for _, d := range []string{"xdg", "tmp", "home"} {
		if _, err := os.Stat(filepath.Join(te.spec.Dir, d)); err != nil {
			t.Errorf("job subdir: %v", err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(te.spec.Dir, "xdg")); fi.Mode().Perm() != 0o700 {
		t.Errorf("xdg mode = %v", fi.Mode().Perm())
	}
	for _, d := range []string{te.paths.MavenCache("p"), te.paths.NPMCache("p")} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("cache dir: %v", err)
		}
	}
	meta, err := ReadMeta(MetaPath(te.spec.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Mounts) != 3 || meta.PodmanSocket != "" {
		t.Errorf("meta = %+v", meta)
	}
	if _, err := os.Stat(filepath.Join(te.spec.Dir, "storage.conf")); err == nil {
		t.Error("storage.conf written for non-container job")
	}
}

func TestRunnerContainerJob(t *testing.T) {
	// given
	te := newTestEnv(t, true, exited(0))

	// when
	if _, err := te.runner.Run(context.Background(), te.spec, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	// then
	for _, f := range []string{"storage.conf", "containers.conf", "ovl/upper", "ovl/work"} {
		if _, err := os.Stat(filepath.Join(te.spec.Dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, d := range []string{te.paths.ContainersStore("p"), te.paths.ContainersRun("p")} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("store dir: %v", err)
		}
	}
	meta, _ := ReadMeta(MetaPath(te.spec.Dir))
	if meta.PodmanSocket != te.spec.Dir+"/xdg/podman.sock" {
		t.Errorf("podman socket = %q", meta.PodmanSocket)
	}
	if !strings.Contains(strings.Join(te.cmd.Args, " "), "--overlay-src") {
		t.Error("no overlay")
	}
}

func TestRunnerRejectsSymlinkedWritesBeforeExec(t *testing.T) {
	// given
	te := newTestEnv(t, false, exited(0))
	symlink(t, t.TempDir(), filepath.Join(te.spec.RepoPath, "target"))

	// when
	_, err := te.runner.Run(context.Background(), te.spec, &bytes.Buffer{})

	// then
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Errorf("err = %v", err)
	}
	if te.cmd != nil {
		t.Error("command ran")
	}
}

func TestRunnerStartFailureIsError(t *testing.T) {
	// given
	te := newTestEnv(t, false, 0)
	te.runner.run = func(*exec.Cmd) (syscall.WaitStatus, error) { return 0, errors.New("no systemd-run") }

	// when
	_, err := te.runner.Run(context.Background(), te.spec, &bytes.Buffer{})

	// then
	if err == nil {
		t.Fatal("want error")
	}
}
