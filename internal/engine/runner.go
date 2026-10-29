package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/job"
)

const (
	exitTimedOut = 124
	exitSIGKILL  = 128 + int(syscall.SIGKILL)
	// > kill-after: timeout gets time to KILL the job before the broker gives up on it
	cancelWaitDelay = 45 * time.Second
)

type Runner struct {
	cfg     *config.Config
	paths   config.Paths
	selfExe string
	home    string
	environ func() []string
	now     func() time.Time
	// run starts cmd and waits; an error means it could not be started or waited for.
	run func(cmd *exec.Cmd) (syscall.WaitStatus, error)
}

var _ job.Runner = (*Runner)(nil)

func NewRunner(cfg *config.Config, paths config.Paths, selfExe string) *Runner {
	home, _ := os.UserHomeDir()
	return &Runner{
		cfg:     cfg,
		paths:   paths,
		selfExe: selfExe,
		home:    home,
		environ: os.Environ,
		now:     time.Now,
		run:     runCmd,
	}
}

func (r *Runner) Run(ctx context.Context, spec job.Spec, output io.Writer) (job.Result, error) {
	if r.home == "" {
		return job.Result{}, errors.New("owner home dir unknown")
	}
	plan := Plan{
		BwrapPath:     r.cfg.BwrapPath,
		SelfExe:       r.selfExe,
		Home:          r.home,
		Toolchain:     r.cfg.Toolchain,
		Repo:          spec.RepoPath,
		JobDir:        spec.Dir,
		MavenCache:    r.paths.MavenCache(spec.Project),
		NPMCache:      r.paths.NPMCache(spec.Project),
		ContainersDir: filepath.Dir(r.paths.ContainersStore(spec.Project)),
		Containers:    spec.Containers,
		Argv:          spec.Argv,
		Limits:        spec.Limits.WithDefaults(config.DefaultCaps),
		HideNixDaemon: exists(nixDaemonDir),
	}
	if err := prepareDirs(plan); err != nil {
		return job.Result{}, err
	}

	binds, err := PrepareBinds(spec.RepoPath, spec.Writes)
	if err != nil {
		return job.Result{}, err
	}
	defer binds.Close()
	plan.Binds = binds

	meta := Meta{Mounts: binds.Mounts()}
	if plan.Containers {
		meta.PodmanSocket = plan.PodmanSocket()
		if err := writeContainerConfigs(plan); err != nil {
			return job.Result{}, err
		}
	}
	if err := WriteMeta(MetaPath(plan.JobDir), meta); err != nil {
		return job.Result{}, err
	}

	argv := plan.CommandArgv()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = outerEnv(r.environ())
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.ExtraFiles = binds.Files()
	// SIGTERM, not SIGKILL: timeout forwards it to the job and applies --kill-after
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = cancelWaitDelay

	start := r.now()
	status, err := r.run(cmd)
	if err != nil {
		return job.Result{}, fmt.Errorf("run job %s: %w", spec.ID, err)
	}
	return resultFor(status, r.now().Sub(start), plan.Limits.Timeout), nil
}

func runCmd(cmd *exec.Cmd) (syscall.WaitStatus, error) {
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return 0, err
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus), nil
}

func resultFor(ws syscall.WaitStatus, elapsed, timeout time.Duration) job.Result {
	if ws.Signaled() {
		return job.Result{ExitCode: 128 + int(ws.Signal()), Killed: true}
	}
	code := ws.ExitStatus()
	switch {
	case code == exitTimedOut:
		return job.Result{ExitCode: code, TimedOut: true}
	// timeout exits 128+KILL when --kill-after fired; an OOM kill gives the same code earlier
	case code == exitSIGKILL && elapsed >= timeout:
		return job.Result{ExitCode: code, TimedOut: true}
	case code > 128:
		return job.Result{ExitCode: code, Killed: true}
	}
	return job.Result{ExitCode: code}
}

func prepareDirs(p Plan) error {
	dirs := []string{p.JobDir + "/tmp", p.JobDir + "/home", p.MavenCache, p.NPMCache}
	if p.Containers {
		dirs = append(dirs, p.OverlayUpper(), p.OverlayWork(), p.ContainersDir+"/store", p.ContainersDir+"/run")
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := os.Mkdir(p.RuntimeDir(), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create %s: %w", p.RuntimeDir(), err)
	}
	return nil
}

func writeContainerConfigs(p Plan) error {
	if err := os.WriteFile(p.StorageConf(), []byte(StorageConf(p.ContainersDir)), 0o644); err != nil {
		return fmt.Errorf("write storage.conf: %w", err)
	}
	if err := os.WriteFile(p.ContainersConf(), []byte(ContainersConf()), 0o644); err != nil {
		return fmt.Errorf("write containers.conf: %w", err)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
