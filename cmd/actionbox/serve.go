package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"actionbox/internal/broker"
	"actionbox/internal/engine"
	"actionbox/internal/eventlog"
	"actionbox/internal/shim"
)

func runServe(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return usageError{fmt.Sprintf("serve takes no arguments, got %q", args)}
	}
	env, err := loadEnv()
	if err != nil {
		return err
	}
	self, err := selfExe()
	if err != nil {
		return err
	}
	events, err := eventlog.Open(env.Paths.Events())
	if err != nil {
		return err
	}
	defer events.Close()
	ln, err := broker.Listen(env.Paths.Socket())
	if err != nil {
		return err
	}

	b := broker.New(broker.Options{
		Env:    env,
		Runner: engine.NewRunner(env.Config, env.Paths, self),
		Events: events,
		Out:    stdout,
	})
	fmt.Fprintf(stdout, "actionbox serve: listening on %s, %d project(s); Ctrl-C revokes all actions\n",
		env.Paths.Socket(), len(env.Config.Projects))
	serveErr := b.Serve(ctx, ln)

	head, err := events.Head()
	if err != nil {
		return fmt.Errorf("event log head: %w", err)
	}
	fmt.Fprintf(stdout, "actionbox serve: stopped; event log head %s\n", head)
	return serveErr
}

func runMCP(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) > 0 {
		return usageError{fmt.Sprintf("mcp takes no arguments, got %q", args)}
	}
	paths, err := defaultPaths()
	if err != nil {
		return err
	}
	return shim.Run(ctx, paths.Socket(), stdin, stdout)
}

func runJobInit(ctx context.Context, args []string, stderr io.Writer) int {
	metaPath, argv, err := engine.ParseJobInitArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "job-init:", err)
		return 125
	}
	return engine.JobInit(ctx, metaPath, argv)
}

// resolved: the engine bind-mounts this path into the job for job-init
func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
