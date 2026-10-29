package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// exitInitFailed: job-init could not set up the job, argv did not run.
const exitInitFailed = 125

const (
	pingTimeout     = 30 * time.Second
	pingInterval    = 100 * time.Millisecond
	serviceStopWait = 10 * time.Second
)

// ParseJobInitArgs parses `--meta <path> -- <argv...>`.
func ParseJobInitArgs(args []string) (metaPath string, argv []string, err error) {
	if len(args) < 4 || args[0] != "--meta" || args[2] != "--" {
		return "", nil, errors.New("usage: job-init --meta <path> -- <argv...>")
	}
	return args[1], args[3:], nil
}

// JobInit runs inside the sandbox (spec §8.8) and returns the exit code for the process.
func JobInit(ctx context.Context, metaPath string, argv []string) int {
	return jobInit{serviceCmd: podmanServiceCmd, pingTimeout: pingTimeout, stderr: os.Stderr}.run(ctx, metaPath, argv)
}

type jobInit struct {
	serviceCmd  func(sock string) *exec.Cmd
	pingTimeout time.Duration
	stderr      io.Writer
}

func podmanServiceCmd(sock string) *exec.Cmd {
	return exec.Command("podman", "system", "service", "--time=0", "unix://"+sock)
}

func (j jobInit) run(ctx context.Context, metaPath string, argv []string) int {
	meta, err := ReadMeta(metaPath)
	if err == nil {
		err = RecheckMounts(meta.Mounts)
	}
	if err != nil {
		return j.fail(err)
	}
	if meta.PodmanSocket != "" {
		stop, err := j.startService(ctx, meta.PodmanSocket)
		if err != nil {
			return j.fail(err)
		}
		defer stop()
	}
	code, err := runForwardingSignals(argv)
	if err != nil {
		return j.fail(err)
	}
	return code
}

func (j jobInit) fail(err error) int {
	fmt.Fprintf(j.stderr, "actionbox job-init: %v\n", err)
	return exitInitFailed
}

func (j jobInit) startService(ctx context.Context, sock string) (stop func(), err error) {
	cmd := j.serviceCmd(sock)
	cmd.Stdout = j.stderr
	cmd.Stderr = j.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start podman service: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop = func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(serviceStopWait):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	if err := waitForPing(ctx, sock, j.pingTimeout, exited); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

// waitForPing polls GET /_ping until 200, the timeout, or exited delivers.
func waitForPing(ctx context.Context, sock string, timeout time.Duration, exited <-chan error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	defer client.CloseIdleConnections()
	tick := time.NewTicker(pingInterval)
	defer tick.Stop()
	for {
		if ping(ctx, client) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("podman service on %s not ready after %v", sock, timeout)
		case err := <-exited:
			return fmt.Errorf("podman service exited before ready: %v", err)
		case <-tick.C:
		}
	}
}

func ping(ctx context.Context, client *http.Client) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/_ping", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// runForwardingSignals: job-init is not pid 1 (bwrap's init is), so it must pass SIGTERM on itself.
func runForwardingSignals(argv []string) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", argv[0], err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return 0, err
	}
	ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return ws.ExitStatus(), nil
}
