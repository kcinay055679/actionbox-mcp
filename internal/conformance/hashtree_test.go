package conformance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustHash(t *testing.T, dir string) string {
	t.Helper()
	sum, _, err := HashTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestHashTreeDetectsChanges(t *testing.T) {
	// given
	dir := t.TempDir()
	f := filepath.Join(dir, "a", "f")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a/f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	base := mustHash(t, dir)
	mtime := time.Unix(1_700_000_000, 0)

	// when / then
	if mustHash(t, dir) != base {
		t.Fatal("hash not stable")
	}
	if err := os.WriteFile(f, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(f, mtime, mtime)
	changed := mustHash(t, dir)
	if changed == base {
		t.Error("content change not detected")
	}
	_ = os.Chtimes(f, mtime.Add(time.Second), mtime.Add(time.Second))
	if mustHash(t, dir) == changed {
		t.Error("mtime change not detected")
	}
	if err := os.WriteFile(filepath.Join(dir, "new"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if mustHash(t, dir) == changed {
		t.Error("new file not detected")
	}
}

func TestHashTreeAbsentDir(t *testing.T) {
	if got := mustHash(t, filepath.Join(t.TempDir(), "none")); got != "absent" {
		t.Errorf("got %s", got)
	}
}

func TestParseHashLine(t *testing.T) {
	line := hashTreeProbe(t.TempDir())
	line.Name = "hash-tree"
	sum, err := ParseHashLine([]byte("podman noise\n" + line.String() + "\n"))
	if err != nil || len(sum) != 64 {
		t.Errorf("sum = %q, err = %v", sum, err)
	}
	if _, err := ParseHashLine([]byte("nothing")); err == nil {
		t.Error("want error without line")
	}
}
