package trust

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type repo struct {
	path string
	git  string
}

// step 1 preconditions
func openRepo(ctx context.Context, path string) (*repo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("trust: %w", err)
	}
	r := &repo{path: abs, git: git}

	if _, err := os.Lstat(filepath.Join(abs, ".git", "info", "grafts")); !errors.Is(err, os.ErrNotExist) {
		return nil, fail(ReasonGrafts, "%s", filepath.Join(".git", "info", "grafts"))
	}
	format, err := r.run(ctx, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	if f := strings.TrimSpace(string(format)); f != "sha1" {
		return nil, fail(ReasonObjectFormat, "%s", f)
	}
	return r, nil
}

func (r *repo) run(ctx context.Context, args ...string) ([]byte, error) {
	argv := append([]string{"-C", r.path, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, r.git, argv...)
	// minimal env: caller's GIT_DIR, GIT_OBJECT_DIRECTORY etc. must not redirect us
	cmd.Env = []string{
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_DIR=" + filepath.Join(r.path, ".git"),
		"GIT_WORK_TREE=" + r.path,
		"LC_ALL=C",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("trust: git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// step 2: hint only, caller verifies it
func (r *repo) definitionsCommit(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-list", "-1", "HEAD", "--", definitionsFile)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fail(ReasonMissingDefinitions, "no commit touches %s", definitionsFile)
	}
	if !isObjectID(id) {
		return "", fmt.Errorf("trust: unexpected rev-list output %q", out)
	}
	return id, nil
}

// readObject returns the object body only if it hashes to id: git doesn't re-hash on read.
func (r *repo) readObject(ctx context.Context, typ, id string) ([]byte, error) {
	body, err := r.run(ctx, "cat-file", typ, id)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fail(ReasonHashMismatch, "%s %s unreadable: %v", typ, id, err)
	}
	if err != nil {
		return nil, err
	}
	if got := hashObject(typ, body); got != id {
		return nil, fail(ReasonHashMismatch, "%s %s hashes to %s", typ, id, got)
	}
	return body, nil
}

// steps 5, 6
func (r *repo) definitionsBlob(ctx context.Context, tree string) ([]byte, error) {
	body, err := r.readObject(ctx, "tree", tree)
	if err != nil {
		return nil, err
	}
	entries, err := parseTree(body)
	if err != nil {
		return nil, err
	}
	id, err := definitionsEntry(entries)
	if err != nil {
		return nil, err
	}
	return r.readObject(ctx, "blob", id)
}
