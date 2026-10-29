package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage: actionbox <command> [args]

commands:
  serve                                  run the broker in the foreground; Ctrl-C revokes all actions
  mcp                                    MCP server for the agent (stdio), forwards to serve
  verify [project] [--accept <commit>]   check and record a project's trusted definitions
  verify --log                           check the event log's hash chain
  run <action> [name=value...]           run any action yourself (also owner-only), without serve
  conformance [--agent-env]              check the isolation guarantees on this machine
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = runServe(ctx, args[1:], stdout)
	case "mcp":
		err = runMCP(ctx, args[1:], stdin, stdout)
	case "verify":
		err = runVerify(ctx, args[1:], stdout)
	case "run":
		return runExitCode(runRun(ctx, args[1:], stdout), stderr)
	case "conformance":
		err = runConformance(ctx, args[1:], stdout)
	case "job-init":
		// internal: runs inside the job sandbox, its exit code is the job's
		return runJobInit(ctx, args[1:], stderr)
	case "probe":
		// internal: conformance probes, run inside the job sandbox
		return runProbe(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	var u usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &u):
		fmt.Fprintf(stderr, "%v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintln(stderr, err)
		return 1
	}
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }
