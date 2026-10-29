package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"actionbox/internal/config"
	"actionbox/internal/trust"
	"actionbox/internal/verify"
)

type verifyArgs struct {
	project string
	accept  string
}

func parseVerifyArgs(args []string) (verifyArgs, error) {
	var v verifyArgs
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--accept":
			if i+1 == len(args) {
				return v, usageError{"--accept needs a commit"}
			}
			i++
			v.accept = args[i]
		case strings.HasPrefix(arg, "--accept="):
			v.accept = strings.TrimPrefix(arg, "--accept=")
		case strings.HasPrefix(arg, "-"):
			return v, usageError{fmt.Sprintf("unknown flag %q", arg)}
		case v.project == "":
			v.project = arg
		default:
			return v, usageError{fmt.Sprintf("unexpected argument %q", arg)}
		}
	}
	return v, nil
}

func runVerify(ctx context.Context, args []string, stdout io.Writer) error {
	v, err := parseVerifyArgs(args)
	if err != nil {
		return err
	}
	env, err := loadEnv()
	if err != nil {
		return err
	}
	name := v.project
	if name == "" {
		if name, err = projectForWorkingDir(env.Config); err != nil {
			return err
		}
	}

	var out verify.Outcome
	if v.accept != "" {
		out, err = verify.Accept(ctx, env, name, v.accept)
	} else {
		out, err = verify.Project(ctx, env, name)
	}
	var f *trust.Failure
	if errors.As(err, &f) {
		return fmt.Errorf("%s: untrusted: %s", name, f.Reason+detail(f.Detail))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: trusted %s, actions: %s\n", out.Project, out.Commit, strings.Join(out.Definitions.Names(), ", "))
	return nil
}

func detail(d string) string {
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

func loadEnv() (verify.Env, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return verify.Env{}, err
	}
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return verify.Env{}, err
	}
	return verify.Env{Config: cfg, Paths: paths}, nil
}

func projectForWorkingDir(cfg *config.Config) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(wd)
	if err != nil {
		return "", err
	}
	name, _, ok := cfg.ProjectForDir(real)
	if !ok {
		return "", fmt.Errorf("%s is not inside a registered project", real)
	}
	return name, nil
}
