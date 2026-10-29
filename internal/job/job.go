// Package job is the contract between the broker, which schedules jobs, and the engine, which isolates them.
package job

import (
	"context"
	"io"

	"actionbox/internal/limits"
)

type Spec struct {
	ID       string
	Project  string
	RepoPath string
	// Dir is the job dir (spec §8.2 jobs/<project>/<job>), created by the broker.
	Dir        string
	Action     string
	Argv       []string
	Writes     []string
	Containers bool
	// Limits are already clamped to the owner's caps.
	Limits limits.Limits
}

type Result struct {
	ExitCode int
	TimedOut bool
	// Killed: ended by a signal other than the timeout, e.g. MemoryMax or the owner's Ctrl-C.
	Killed bool
}

// Runner runs one job to completion, writing combined stdout/stderr to output.
// An error means the job could not be started or isolated, not that argv failed.
type Runner interface {
	Run(ctx context.Context, spec Spec, output io.Writer) (Result, error)
}
