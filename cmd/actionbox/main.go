package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
)

const usage = `usage: actionbox <command> [args]

commands:
  verify [project] [--accept <commit>]   check and record a project's trusted definitions
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "verify":
		err = runVerify(ctx, args[1:], stdout)
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
