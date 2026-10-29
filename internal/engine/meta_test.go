package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func fileIno(fi os.FileInfo) uint64 {
	return fi.Sys().(*syscall.Stat_t).Ino
}

func TestMetaRoundtrip(t *testing.T) {
	// given
	path := filepath.Join(t.TempDir(), "meta.json")
	want := Meta{
		Mounts:       []Mount{{Path: "/r", Dev: 1, Ino: 2}, {Path: "/r/target", Dev: 1, Ino: 3}},
		PodmanSocket: "/j/xdg/podman.sock",
	}

	// when
	if err := WriteMeta(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMeta(path)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRecheckMounts(t *testing.T) {
	// given
	repo := t.TempDir()
	mkdirs(t, repo, "target")
	b := prepare(t, repo, []string{"target"})
	mounts := b.Mounts()

	// when / then
	if err := RecheckMounts(mounts); err != nil {
		t.Fatalf("unchanged mounts: %v", err)
	}

	// given: target swapped after binds were prepared
	if err := os.Rename(repo+"/target", repo+"/target.orig"); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, repo, "target")

	// when
	err := RecheckMounts(mounts)

	// then
	if err == nil || !strings.Contains(err.Error(), repo+"/target") {
		t.Errorf("err = %v, want mismatch naming target", err)
	}
}

func TestRecheckMountsMissingPath(t *testing.T) {
	err := RecheckMounts([]Mount{{Path: filepath.Join(t.TempDir(), "gone"), Dev: 1, Ino: 1}})
	if err == nil {
		t.Fatal("want error")
	}
}
