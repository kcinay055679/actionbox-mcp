package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func touch(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func prepare(t *testing.T, repo string, writes []string) *Binds {
	t.Helper()
	b, err := PrepareBinds(repo, writes)
	if err != nil {
		t.Fatalf("PrepareBinds: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

func dests(binds []Bind) []string {
	var out []string
	for _, b := range binds {
		out = append(out, b.Dest)
	}
	return out
}

func TestPrepareBindsOpensRepoWritesAndRootProtected(t *testing.T) {
	// given
	repo := t.TempDir()
	mkdirs(t, repo, ".git", "target", "src")
	touch(t, repo, ".npmrc", "mvnw")

	// when
	b := prepare(t, repo, []string{"target"})

	// then
	if b.Repo.Dest != repo || !b.Repo.ReadOnly || b.Repo.FD != 3 {
		t.Errorf("repo bind = %+v", b.Repo)
	}
	if got := dests(b.Writes); !slices.Equal(got, []string{repo + "/target"}) {
		t.Errorf("writes = %v", got)
	}
	if b.Writes[0].FD != 4 || b.Writes[0].ReadOnly {
		t.Errorf("writes bind = %+v", b.Writes[0])
	}
	wantProtected := []string{repo + "/.git", repo + "/.npmrc", repo + "/mvnw"}
	if got := dests(b.Protected); !slices.Equal(got, wantProtected) {
		t.Errorf("protected = %v, want %v", got, wantProtected)
	}
	for i, p := range b.Protected {
		if p.FD != 5+i || !p.ReadOnly {
			t.Errorf("protected[%d] = %+v", i, p)
		}
	}
	files := b.Files()
	if len(files) != 5 {
		t.Fatalf("files = %d, want 5", len(files))
	}
	for i, m := range b.Mounts() {
		fi, err := os.Lstat(m.Path)
		if err != nil {
			t.Fatal(err)
		}
		if ino := fileIno(fi); ino != m.Ino || m.Dev == 0 {
			t.Errorf("mount %d %s: ino %d, recorded %+v", i, m.Path, ino, m)
		}
	}
}

func TestPrepareBindsCreatesMissingWritesDirs(t *testing.T) {
	// given
	repo := t.TempDir()

	// when
	b := prepare(t, repo, []string{"build/out", "build"})

	// then
	if fi, err := os.Lstat(filepath.Join(repo, "build", "out")); err != nil || !fi.IsDir() {
		t.Fatalf("build/out not created: %v", err)
	}
	if got := dests(b.Writes); !slices.Equal(got, []string{repo + "/build", repo + "/build/out"}) {
		t.Errorf("writes not parent-first: %v", got)
	}
}

func TestPrepareBindsRejectsSymlinks(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, repo, outside string)
		writes  []string
		wantErr string
	}{
		{
			name:    "writes dir is symlink",
			setup:   func(t *testing.T, repo, outside string) { symlink(t, outside, filepath.Join(repo, "target")) },
			writes:  []string{"target"},
			wantErr: "target",
		},
		{
			name:    "writes dir is dangling symlink",
			setup:   func(t *testing.T, repo, outside string) { symlink(t, outside+"/nope", filepath.Join(repo, "target")) },
			writes:  []string{"target"},
			wantErr: "target",
		},
		{
			name: "parent component is symlink",
			setup: func(t *testing.T, repo, outside string) {
				mkdirs(t, outside, "b")
				symlink(t, outside, filepath.Join(repo, "a"))
			},
			writes:  []string{"a/b"},
			wantErr: `"a"`,
		},
		{
			name:    "root protected path is symlink",
			setup:   func(t *testing.T, repo, outside string) { symlink(t, outside, filepath.Join(repo, ".npmrc")) },
			writes:  nil,
			wantErr: ".npmrc",
		},
		{
			name: "protected name in writes dir is symlink",
			setup: func(t *testing.T, repo, outside string) {
				mkdirs(t, repo, "node_modules/pkg")
				symlink(t, outside, filepath.Join(repo, "node_modules/pkg/.git"))
			},
			writes:  []string{"node_modules"},
			wantErr: "node_modules/pkg/.git",
		},
		{
			name:    "writes component is a file",
			setup:   func(t *testing.T, repo, outside string) { touch(t, repo, "target") },
			writes:  []string{"target"},
			wantErr: "not a directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			repo, outside := t.TempDir(), t.TempDir()
			tt.setup(t, repo, outside)

			// when
			b, err := PrepareBinds(repo, tt.writes)

			// then
			if err == nil {
				b.Close()
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if entries, _ := os.ReadDir(outside); len(entries) > 1 {
				t.Errorf("outside dir modified: %v", entries)
			}
		})
	}
}

func TestPrepareBindsRejectsRepoSymlink(t *testing.T) {
	// given
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "repo")
	symlink(t, real, link)

	// when
	_, err := PrepareBinds(link, nil)

	// then
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPrepareBindsFindsProtectedNamesUpToDepthThree(t *testing.T) {
	// given
	repo := t.TempDir()
	touch(t, repo,
		"target/.npmrc",
		"target/a/mvnw",
		"target/a/b/.envrc",
		"target/a/b/c/.bashrc",
		"target/a/b/harmless",
	)
	mkdirs(t, repo, "target/x/y/.git/objects", "target/x/.idea/.git")
	symlink(t, repo+"/target/a", repo+"/target/link")

	// when
	b := prepare(t, repo, []string{"target"})

	// then
	want := []string{
		repo + "/target/.npmrc",
		repo + "/target/a/b/.envrc",
		repo + "/target/a/mvnw",
		repo + "/target/x/.idea",
		repo + "/target/x/y/.git",
	}
	if got := dests(b.Protected); !slices.Equal(got, want) {
		t.Errorf("protected = %v\nwant %v", got, want)
	}
}

func TestPrepareBindsDedupesProtectedOfNestedWrites(t *testing.T) {
	// given
	repo := t.TempDir()
	touch(t, repo, "a/b/.npmrc")

	// when
	b := prepare(t, repo, []string{"a", "a/b"})

	// then
	if got := dests(b.Protected); !slices.Equal(got, []string{repo + "/a/b/.npmrc"}) {
		t.Errorf("protected = %v", got)
	}
	if len(b.Files()) != 4 {
		t.Errorf("files = %d, want 4", len(b.Files()))
	}
}
