package conformance

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const procNetUnixSample = `Num       RefCount Protocol Flags    Type St Inode Path
0000000000000000: 00000002 00000000 00010000 0001 01 23456 /run/user/1000/bus
0000000000000000: 00000002 00000000 00010000 0001 01 23457 @/tmp/.X11-unix/X0
0000000000000000: 00000003 00000000 00000000 0001 03 23458
0000000000000000: 00000003 00000000 00000000 0001 03 23459 /run/user/1000/bus
`

func TestParseProcNetUnix(t *testing.T) {
	paths, abstract, err := ParseProcNetUnix(strings.NewReader(procNetUnixSample))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"/run/user/1000/bus"}) || !slices.Equal(abstract, []string{"@/tmp/.X11-unix/X0"}) {
		t.Errorf("paths = %v, abstract = %v", paths, abstract)
	}
	if _, _, err := ParseProcNetUnix(strings.NewReader("garbage\n")); err == nil {
		t.Error("want error without header")
	}
}

func socketFixture(t *testing.T) (root, sock, proc string) {
	t.Helper()
	root = t.TempDir()
	sock = filepath.Join(root, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	hidden := filepath.Join(root, "proc", "h.sock")
	if err := os.MkdirAll(filepath.Dir(hidden), 0o755); err != nil {
		t.Fatal(err)
	}
	hl, err := net.Listen("unix", hidden)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hl.Close() })
	proc = filepath.Join(t.TempDir(), "unix")
	header := "Num       RefCount Protocol Flags    Type St Inode Path\n"
	if err := os.WriteFile(proc, []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, sock, proc
}

func TestSocketInventory(t *testing.T) {
	// given
	root, sock, proc := socketFixture(t)

	// when
	unexpected := socketInventory(root, proc, nil)
	allowed := socketInventory(root, proc, []string{sock})

	// then
	if unexpected.Outcome != OutcomeFail || !strings.Contains(unexpected.Detail, sock) {
		t.Errorf("unexpected = %+v", unexpected)
	}
	if strings.Contains(unexpected.Detail, "h.sock") {
		t.Error("proc dir not skipped")
	}
	if allowed.Outcome != OutcomeOK {
		t.Errorf("allowed = %+v", allowed)
	}
}

func TestSocketInventoryFlagsProcNetUnixPaths(t *testing.T) {
	// given
	root := t.TempDir()
	proc := filepath.Join(t.TempDir(), "unix")
	if err := os.WriteFile(proc, []byte(procNetUnixSample), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	got := socketInventory(root, proc, nil)

	// then
	if got.Outcome != OutcomeFail || !strings.Contains(got.Detail, "/run/user/1000/bus") {
		t.Errorf("got %+v", got)
	}
}
