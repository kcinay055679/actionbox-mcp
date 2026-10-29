// Package verify checks a project's definitions file and records its trusted commit (spec §8.4).
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"actionbox/internal/config"
	"actionbox/internal/definitions"
	"actionbox/internal/trust"
)

const ReasonInvalidDefinitions = "invalid definitions"

type Env struct {
	Config *config.Config
	Paths  config.Paths
}

type Outcome struct {
	Project     string
	Commit      string
	Definitions *definitions.Definitions
}

type trustState struct {
	Commit string `json:"commit"`
}

// Project verifies against the recorded trusted commit and records the verified one.
func Project(ctx context.Context, env Env, name string) (Outcome, error) {
	req, err := request(env, name)
	if err != nil {
		return Outcome{}, err
	}
	res, err := trust.Verify(ctx, req)
	if err != nil {
		return Outcome{}, err
	}
	return record(env, name, res)
}

// Accept makes commit the trusted commit without the rollback check (`verify --accept`).
func Accept(ctx context.Context, env Env, name, commit string) (Outcome, error) {
	req, err := request(env, name)
	if err != nil {
		return Outcome{}, err
	}
	res, err := trust.Accept(ctx, req, commit)
	if err != nil {
		return Outcome{}, err
	}
	return record(env, name, res)
}

func request(env Env, name string) (trust.Request, error) {
	project, ok := env.Config.Projects[name]
	if !ok {
		return trust.Request{}, fmt.Errorf("unregistered project %q", name)
	}
	trusted, err := readTrusted(env.Paths.TrustFile(name))
	if err != nil {
		return trust.Request{}, err
	}
	return trust.Request{
		RepoPath:          project.Path,
		PinnedFingerprint: env.Config.PinnedFingerprint,
		KeyringPath:       env.Paths.Keyring(),
		Trusted:           trusted,
	}, nil
}

// record only after parsing: an unparsable file must not become the rollback anchor
func record(env Env, name string, res trust.Result) (Outcome, error) {
	defs, err := definitions.Parse(res.Definitions)
	if err != nil {
		return Outcome{}, &trust.Failure{Reason: ReasonInvalidDefinitions, Detail: err.Error()}
	}
	if err := writeTrusted(env.Paths.TrustFile(name), res.Commit); err != nil {
		return Outcome{}, err
	}
	return Outcome{Project: name, Commit: res.Commit, Definitions: defs}, nil
}

func readTrusted(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var s trustState
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return s.Commit, nil
}

func writeTrusted(path, commit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(trustState{Commit: commit})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
