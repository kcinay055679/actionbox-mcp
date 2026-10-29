// Package trust verifies that a repo's definitions file is signed by the pinned key (spec §8.4).
package trust

import (
	"context"
	"fmt"
)

const (
	ReasonGrafts             = "grafts present"
	ReasonObjectFormat       = "unsupported object format"
	ReasonHashMismatch       = "hash mismatch"
	ReasonSignatureStatus    = "signature status"
	ReasonForeignKey         = "foreign key"
	ReasonMissingDefinitions = "missing definitions"
	ReasonSymlink            = "symlink"
	ReasonFileMode           = "file mode"
	ReasonUncommittedEdits   = "uncommitted edits"
	ReasonRollback           = "rollback"
	definitionsFile          = ".agent-actions.toml"
	maxAncestryCommits       = 10000
)

type Request struct {
	RepoPath          string
	PinnedFingerprint string
	KeyringPath       string
	// Trusted is the last recorded commit, "" if none.
	Trusted string
}

type Result struct {
	Commit      string
	Definitions []byte
}

// Failure is a trust check that failed; operational errors are returned as plain errors.
type Failure struct {
	Reason string
	Detail string
}

func (f *Failure) Error() string {
	if f.Detail == "" {
		return "trust: " + f.Reason
	}
	return fmt.Sprintf("trust: %s: %s", f.Reason, f.Detail)
}

func fail(reason, format string, args ...any) *Failure {
	return &Failure{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

func Verify(ctx context.Context, req Request) (Result, error) {
	repo, err := openRepo(ctx, req.RepoPath)
	if err != nil {
		return Result{}, err
	}
	if req.Trusted != "" && !isObjectID(req.Trusted) {
		return Result{}, fmt.Errorf("trust: invalid trusted commit %q", req.Trusted)
	}
	candidate, err := repo.definitionsCommit(ctx)
	if err != nil {
		return Result{}, err
	}
	res, err := verifyCommit(ctx, repo, req, candidate)
	if err != nil {
		return Result{}, err
	}
	if req.Trusted != "" && candidate != req.Trusted {
		if err := requireAncestor(ctx, repo, candidate, req.Trusted); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// no rollback check: owner's verify --accept (spec §8.4 notes)
func Accept(ctx context.Context, req Request, commit string) (Result, error) {
	if !isObjectID(commit) {
		return Result{}, fmt.Errorf("trust: invalid commit %q", commit)
	}
	repo, err := openRepo(ctx, req.RepoPath)
	if err != nil {
		return Result{}, err
	}
	return verifyCommit(ctx, repo, req, commit)
}

// steps 3–7
func verifyCommit(ctx context.Context, repo *repo, req Request, id string) (Result, error) {
	body, err := repo.readObject(ctx, "commit", id)
	if err != nil {
		return Result{}, err
	}
	payload, sig, err := splitSignature(body)
	if err != nil {
		return Result{}, err
	}
	if err := verifySignature(ctx, req.KeyringPath, req.PinnedFingerprint, sig, payload); err != nil {
		return Result{}, err
	}
	commit, err := parseCommit(body)
	if err != nil {
		return Result{}, err
	}
	blob, err := repo.definitionsBlob(ctx, commit.tree)
	if err != nil {
		return Result{}, err
	}
	if err := requireWorkingFile(req.RepoPath, blob); err != nil {
		return Result{}, err
	}
	return Result{Commit: id, Definitions: blob}, nil
}

// walks parents from re-hashed bodies, never rev-list: that follows grafts, shallow and forged parents
func requireAncestor(ctx context.Context, repo *repo, from, ancestor string) error {
	seen := map[string]bool{from: true}
	queue := []string{from}
	for read := 0; len(queue) > 0; read++ {
		if read == maxAncestryCommits {
			return fail(ReasonRollback, "%s not within %d commits of %s", ancestor, maxAncestryCommits, from)
		}
		id := queue[0]
		queue = queue[1:]
		body, err := repo.readObject(ctx, "commit", id)
		if err != nil {
			return err
		}
		commit, err := parseCommit(body)
		if err != nil {
			return err
		}
		for _, parent := range commit.parents {
			if parent == ancestor {
				return nil
			}
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return fail(ReasonRollback, "%s does not descend from %s", from, ancestor)
}
