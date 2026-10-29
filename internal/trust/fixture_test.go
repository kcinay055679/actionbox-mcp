package trust

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"actionbox/internal/trust/trustfixtures"
)

func TestMain(m *testing.M) {
	code := m.Run()
	trustfixtures.Cleanup()
	os.Exit(code)
}

func fixtureRequest(dir string, c trustfixtures.Case) Request {
	req := Request{
		RepoPath:          filepath.Join(dir, c.Repo),
		PinnedFingerprint: c.Pin,
		KeyringPath:       filepath.Join(dir, c.Keyring),
	}
	if c.Trusted != nil {
		req.Trusted = *c.Trusted
	}
	return req
}

func TestVerifyFixtures(t *testing.T) {
	set := trustfixtures.Load(t)
	dir, cases := set.Dir, set.Cases
	if len(cases) != 23 {
		t.Fatalf("want 23 cases, got %d", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			res, err := Verify(context.Background(), fixtureRequest(dir, c))

			if c.Expect == "pass" {
				if err != nil {
					t.Fatalf("want pass, got %v", err)
				}
				assertMatchesWorkingFile(t, filepath.Join(dir, c.Repo), res)
			} else {
				assertReason(t, err, *c.Reason)
			}
			if c.Marker != nil {
				if _, err := os.Lstat(filepath.Join(dir, *c.Marker)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("marker %s exists: repo-configured program ran", *c.Marker)
				}
			}
		})
	}
}

func assertMatchesWorkingFile(t *testing.T, repo string, res Result) {
	t.Helper()
	if len(res.Commit) != 40 {
		t.Errorf("commit %q", res.Commit)
	}
	want, err := os.ReadFile(filepath.Join(repo, definitionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Definitions, want) {
		t.Errorf("definitions %q, want %q", res.Definitions, want)
	}
}

func TestVerifySkipsUnsignedHeadNotTouchingDefinitions(t *testing.T) {
	set := trustfixtures.Load(t)
	dir := set.Dir
	repo := filepath.Join(dir, "unsigned-head-other-file")
	res, err := Verify(context.Background(), Request{
		RepoPath:          repo,
		PinnedFingerprint: set.Owner,
		KeyringPath:       filepath.Join(dir, "keys/owner.gpg"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := gitOutput(t, repo, "rev-parse", "HEAD~1"); res.Commit != want {
		t.Errorf("commit %s, want %s", res.Commit, want)
	}
}

func TestAcceptSkipsAncestryCheck(t *testing.T) {
	set := trustfixtures.Load(t)
	dir := set.Dir
	repo := filepath.Join(dir, "rollback")
	head := gitOutput(t, repo, "rev-parse", "HEAD")
	req := Request{
		RepoPath:          repo,
		PinnedFingerprint: set.Owner,
		KeyringPath:       filepath.Join(dir, "keys/owner.gpg"),
		Trusted:           gitOutput(t, repo, "rev-parse", "main"),
	}

	res, err := Accept(context.Background(), req, head)
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != head {
		t.Errorf("commit %s, want %s", res.Commit, head)
	}
}

func TestAcceptVerifiesGivenCommit(t *testing.T) {
	set := trustfixtures.Load(t)
	dir := set.Dir
	repo := filepath.Join(dir, "rollback")
	req := Request{
		RepoPath:          repo,
		PinnedFingerprint: set.Owner,
		KeyringPath:       filepath.Join(dir, "keys/owner.gpg"),
	}

	_, err := Accept(context.Background(), req, gitOutput(t, repo, "rev-parse", "main"))
	assertReason(t, err, ReasonUncommittedEdits)
}

func TestAcceptRejectsMalformedCommit(t *testing.T) {
	set := trustfixtures.Load(t)
	dir := set.Dir
	req := Request{RepoPath: filepath.Join(dir, "signed-clean")}
	if _, err := Accept(context.Background(), req, "HEAD"); err == nil {
		t.Fatal("want error")
	}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
