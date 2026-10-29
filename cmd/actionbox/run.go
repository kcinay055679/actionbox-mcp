package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"actionbox/internal/definitions"
	"actionbox/internal/engine"
	"actionbox/internal/eventlog"
	"actionbox/internal/job"
	"actionbox/internal/jobrun"
	"actionbox/internal/protocol"
	"actionbox/internal/trust"
	"actionbox/internal/verify"
)

// test seams: tests can't run bwrap or sign fixtures for every action shape
var (
	verifyProject = verify.Project
	newJobRunner  = func(env verify.Env) (job.Runner, error) {
		self, err := selfExe()
		if err != nil {
			return nil, err
		}
		return engine.NewRunner(env.Config, env.Paths, self), nil
	}
)

type runArgs struct {
	project string
	action  string
	params  map[string]string
}

func parseRunArgs(args []string) (runArgs, error) {
	r := runArgs{params: map[string]string{}}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--project":
			if i+1 == len(args) {
				return r, usageError{"--project needs a name"}
			}
			i++
			r.project = args[i]
		case strings.HasPrefix(arg, "--project="):
			r.project = strings.TrimPrefix(arg, "--project=")
		case strings.HasPrefix(arg, "-"):
			return r, usageError{fmt.Sprintf("unknown flag %q", arg)}
		case r.action == "":
			r.action = arg
		default:
			name, value, ok := strings.Cut(arg, "=")
			if !ok || name == "" {
				return r, usageError{fmt.Sprintf("malformed parameter %q, want name=value", arg)}
			}
			if _, dup := r.params[name]; dup {
				return r, usageError{fmt.Sprintf("duplicate parameter %q", name)}
			}
			r.params[name] = value
		}
	}
	if r.action == "" {
		return r, usageError{"run needs an action"}
	}
	return r, nil
}

// job outcome already printed: only the code is left to report
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("job exit code %d", e.code) }

func runExitCode(err error, stderr io.Writer) int {
	var x exitError
	var u usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &x):
		return x.code
	case errors.As(err, &u):
		fmt.Fprintf(stderr, "%v\n\n%s", err, usage)
		return 2
	default:
		fmt.Fprintln(stderr, err)
		return 1
	}
}

func runRun(ctx context.Context, args []string, stdout io.Writer) error {
	a, err := parseRunArgs(args)
	if err != nil {
		return err
	}
	env, err := loadEnv()
	if err != nil {
		return err
	}
	name := a.project
	if name == "" {
		if name, err = projectForWorkingDir(env.Config); err != nil {
			return err
		}
	}
	outcome, err := verifyProject(ctx, env, name)
	var f *trust.Failure
	if errors.As(err, &f) {
		return fmt.Errorf("%s: untrusted: %s", name, f.Reason+detail(f.Detail))
	}
	if err != nil {
		return err
	}
	project := env.Config.Projects[name]

	action, ok := outcome.Definitions.Actions[a.action]
	if !ok {
		return fmt.Errorf("%s: no action %q (actions: %s)", name, a.action, strings.Join(outcome.Definitions.Names(), ", "))
	}
	if action.Kind == definitions.KindCoordinateFetch {
		return fmt.Errorf("%s: %q is a coordinate fetch: use actionbox fetch", name, a.action)
	}
	argv, err := action.Argv(a.params)
	if err != nil {
		return err
	}

	runner, err := newJobRunner(env)
	if err != nil {
		return err
	}
	events, err := eventlog.Open(env.Paths.Events())
	if err != nil {
		return err
	}
	defer events.Close()
	jenv := jobrun.Env{Paths: env.Paths, Runner: runner, Events: events, Now: time.Now}
	j, err := jobrun.Create(jenv, jobrun.CapsFor(env.Config), definitions.InvokerOwner, project, outcome.Commit, action, a.params, argv)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}

	// informational only: lock may change hands before Run takes it
	if !jobrun.LockFree(env.Paths, name) {
		fmt.Fprintf(stdout, "actionbox run: %s: waiting for the project lock (serve or another run has a job)\n", name)
	}
	rec, runErr := j.Run(ctx, jenv, stdout, func(rec jobrun.Record) {
		fmt.Fprintf(stdout, "actionbox run: %s: job %s %q started (definitions %s)\n", rec.Project, rec.ID, rec.Action, rec.DefinitionsCommit)
	})
	pruneErr := jobrun.Prune(env.Paths, name, jobrun.RetentionFor(env.Config), time.Now(), nil)
	if err := errors.Join(runErr, pruneErr); err != nil {
		fmt.Fprintf(stdout, "actionbox run: %s: %v\n", name, err)
	}

	fmt.Fprintf(stdout, "actionbox run: %s: job %s %q %s; log %s\n",
		rec.Project, rec.ID, rec.Action, rec.Outcome(), filepath.Join(j.Spec.Dir, jobrun.LogFile))
	return jobExit(rec)
}

func jobExit(rec jobrun.Record) error {
	switch {
	case rec.State == protocol.JobSucceeded:
		return nil
	case rec.Started == nil:
		return fmt.Errorf("%s: job %s not run: %s", rec.Project, rec.ID, rec.Detail)
	case rec.State == protocol.JobTimedOut:
		return exitError{124}
	case rec.State == protocol.JobKilled:
		return exitError{137}
	case rec.ExitCode != nil:
		return exitError{*rec.ExitCode}
	default:
		return exitError{1}
	}
}
