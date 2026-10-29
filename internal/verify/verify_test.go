package verify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"actionbox/internal/config"
	"actionbox/internal/trust"
	"actionbox/internal/trust/trustfixtures"
)

func TestMain(m *testing.M) {
	code := m.Run()
	trustfixtures.Cleanup()
	os.Exit(code)
}

func TestProjectRecordsTrustedCommit(t *testing.T) {
	env, _ := fixtureEnv(t, "signed-clean")

	out, err := Project(context.Background(), env, "signed-clean")

	if err != nil {
		t.Fatal(err)
	}
	if got := readState(t, env, "signed-clean"); got != out.Commit {
		t.Errorf("recorded %q, want %q", got, out.Commit)
	}
	if names := out.Definitions.Names(); len(names) != 1 || names[0] != "lint" {
		t.Errorf("actions %v", names)
	}
}

func TestProjectUsesRecordedCommitAsRollbackGuard(t *testing.T) {
	env, set := fixtureEnv(t, "rollback")
	c := set.Case(t, "rollback")
	writeState(t, env, "rollback", *c.Trusted)

	_, err := Project(context.Background(), env, "rollback")

	assertReason(t, err, trust.ReasonRollback)
	if got := readState(t, env, "rollback"); got != *c.Trusted {
		t.Errorf("state changed to %q", got)
	}
}

func TestAcceptOverridesRollbackGuard(t *testing.T) {
	env, set := fixtureEnv(t, "rollback")
	c := set.Case(t, "rollback")
	writeState(t, env, "rollback", *c.Trusted)
	head := gitHead(t, filepath.Join(set.Dir, c.Repo))

	out, err := Accept(context.Background(), env, "rollback", head)

	if err != nil {
		t.Fatal(err)
	}
	if out.Commit != head || readState(t, env, "rollback") != head {
		t.Errorf("accepted %q, recorded %q, want %q", out.Commit, readState(t, env, "rollback"), head)
	}
}

func TestProjectRejectsUnregistered(t *testing.T) {
	env, _ := fixtureEnv(t, "signed-clean")
	if _, err := Project(context.Background(), env, "other"); err == nil {
		t.Fatal("want error")
	}
}

func fixtureEnv(t *testing.T, caseName string) (Env, trustfixtures.Set) {
	t.Helper()
	set := trustfixtures.Load(t)
	c := set.Case(t, caseName)
	paths := config.PathsForHome(t.TempDir())
	copyFile(t, filepath.Join(set.Dir, c.Keyring), paths.Keyring())
	cfg := &config.Config{
		PinnedFingerprint: c.Pin,
		Projects:          map[string]config.Project{caseName: {Path: filepath.Join(set.Dir, c.Repo)}},
	}
	return Env{Config: cfg, Paths: paths}, set
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, env Env, name string) string {
	t.Helper()
	commit, err := readTrusted(env.Paths.TrustFile(name))
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func writeState(t *testing.T, env Env, name, commit string) {
	t.Helper()
	if err := writeTrusted(env.Paths.TrustFile(name), commit); err != nil {
		t.Fatal(err)
	}
}

func gitHead(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func assertReason(t *testing.T, err error, reason string) {
	t.Helper()
	var f *trust.Failure
	if !errors.As(err, &f) || f.Reason != reason {
		t.Fatalf("want reason %q, got %v", reason, err)
	}
}
