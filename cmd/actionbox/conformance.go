package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"actionbox/internal/config"
	"actionbox/internal/conformance"
	"actionbox/internal/engine"
	"actionbox/internal/job"
)

func runConformance(ctx context.Context, args []string, stdout io.Writer) error {
	agentEnv := false
	for _, a := range args {
		if a != "--agent-env" {
			return usageError{fmt.Sprintf("conformance: unknown argument %q", a)}
		}
		agentEnv = true
	}
	if agentEnv {
		results := conformance.ScanAgentEnv(os.Environ())
		for _, r := range results {
			conformance.WriteResult(stdout, r)
		}
		conformance.WriteSummary(stdout, results)
		return nil
	}

	env, err := loadEnv()
	if err != nil {
		return err
	}
	self, err := selfExe()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	base, err := os.MkdirTemp("", "actionbox-conformance-")
	if err != nil {
		return err
	}
	podman := conformance.LookupPodman(self)
	defer removeBase(stdout, podman, base)

	host := conformance.Host{
		NewRunner: func(paths config.Paths, afterBinds func()) job.Runner {
			r := engine.NewRunner(env.Config, paths, self)
			r.AfterBinds = afterBinds
			return r
		},
		Config:  env.Config,
		SelfExe: self,
		Home:    home,
		Podman:  podman,
		Dial:    conformance.DialNet,
	}
	fmt.Fprintf(stdout, "conformance: P1–P15 in %s; A1–A6 are not covered here; run --agent-env from the agent's Fence for P16\n\n", base)
	results, err := conformance.Run(ctx, host, base, stdout)
	conformance.WriteSummary(stdout, results)
	if err != nil {
		return err
	}
	if n := conformance.Count(results, conformance.Fail); n > 0 {
		return fmt.Errorf("conformance: %d check(s) failed", n)
	}
	return nil
}

// container layers belong to subuids, so a plain RemoveAll can fail without podman unshare
func removeBase(stdout io.Writer, podman conformance.Podman, base string) {
	err := os.RemoveAll(base)
	if err != nil && podman != nil {
		err = podman.RemoveAll(context.Background(), base)
	}
	if err != nil {
		fmt.Fprintf(stdout, "conformance: could not remove %s: %v\n", base, err)
	}
}

func runProbe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return conformance.RunProbe(ctx, args, stdout, stderr)
}
