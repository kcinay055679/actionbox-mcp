package conformance

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
}

// readOnlyTree simulates ro mounts with permission bits: dir/ro-file and dir/sub are not writable.
func readOnlyTree(t *testing.T) (dir string) {
	t.Helper()
	skipIfRoot(t)
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "ro-file"), []byte("keep"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "sub"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "sub"), 0o755) })
	return dir
}

func TestWriteOK(t *testing.T) {
	dir := readOnlyTree(t)
	if got := writeOK(filepath.Join(dir, "f")); got.Outcome != OutcomeOK {
		t.Errorf("writable dir: %+v", got)
	}
	if got := writeOK(filepath.Join(dir, "sub", "f")); got.Outcome != OutcomeFail {
		t.Errorf("read-only dir: %+v", got)
	}
}

func TestWriteDeniedOnReadOnlyPaths(t *testing.T) {
	// given
	dir := readOnlyTree(t)
	file := filepath.Join(dir, "sub", "ro-file")

	// when
	got := writeDenied([]string{filepath.Join(dir, "sub", "x"), file})

	// then
	if got.Outcome != OutcomeOK {
		t.Errorf("got %+v", got)
	}
	if data, _ := os.ReadFile(file); string(data) != "keep" {
		t.Errorf("file changed: %q", data)
	}
}

func TestWriteDeniedFailsOnWritableOrMissing(t *testing.T) {
	dir := readOnlyTree(t)
	writable := writeDenied([]string{filepath.Join(dir, "sub", "x"), filepath.Join(dir, "new")})
	if writable.Outcome != OutcomeFail || !strings.Contains(writable.Detail, "new (write)") {
		t.Errorf("writable: %+v", writable)
	}
	missing := writeDenied([]string{filepath.Join(dir, "absent", "x")})
	if missing.Outcome != OutcomeFail || !strings.Contains(missing.Detail, "not testable") {
		t.Errorf("missing parent: %+v", missing)
	}
}

func TestWriteDeniedFailsWhenExistingFileRemovable(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	f := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(f, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	got := writeDenied([]string{f})
	if got.Outcome != OutcomeFail || !strings.Contains(got.Detail, "(remove)") {
		t.Errorf("got %+v", got)
	}
}

func TestReadDenied(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	if got := readDenied(home); got.Outcome != OutcomeOK {
		t.Errorf("empty home: %+v", got)
	}

	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("//r/:_authToken=x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if got := readDenied(home); got.Outcome != OutcomeOK {
		t.Errorf("unreadable file: %+v", got)
	}

	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readDenied(home)
	if got.Outcome != OutcomeFail || !strings.Contains(got.Detail, "id_ed25519") {
		t.Errorf("readable key: %+v", got)
	}
}

func TestConnectDenied(t *testing.T) {
	// given
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	ln.Close()
	abstract := "@" + tmpPrefix + randomToken()
	ul, err := net.Listen("unix", abstract)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	ctx := context.Background()

	// when / then
	if got := connectDenied(ctx, []string{"tcp:" + closed}); got.Outcome != OutcomeOK {
		t.Errorf("closed port: %+v", got)
	}
	if got := connectDenied(ctx, []string{"tcp:" + closed, "tcp:" + open.Addr().String()}); got.Outcome != OutcomeFail {
		t.Errorf("open port: %+v", got)
	}
	if got := connectDenied(ctx, []string{"unix:" + abstract}); got.Outcome != OutcomeFail {
		t.Errorf("abstract socket: %+v", got)
	}
	if got := connectDenied(ctx, []string{"udp:1.1.1.1:53"}); got.Outcome != OutcomeFail {
		t.Errorf("bad target: %+v", got)
	}
}

func TestEnvProbe(t *testing.T) {
	allowed := "PATH,HOME,TERM"
	tests := []struct {
		name    string
		environ []string
		want    Outcome
	}{
		{"allowlisted only", []string{"PATH=/bin", "HOME=/j"}, OutcomeOK},
		{"extra name", []string{"PATH=/bin", "SSH_AUTH_SOCK=/s"}, OutcomeFail},
		{"canary name", []string{"PATH=/bin", "CANARY_TOKEN=c4"}, OutcomeFail},
		{"canary value under allowed name", []string{"PATH=/bin:c4"}, OutcomeFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envProbe(tt.environ, []string{allowed, CanaryEnv, "c4"})
			if got.Outcome != tt.want {
				t.Errorf("got %+v", got)
			}
		})
	}
	if got := envProbe(nil, []string{allowed}); got.Outcome != OutcomeFail {
		t.Errorf("bad usage: %+v", got)
	}
}

func TestTmpWriteThenCheck(t *testing.T) {
	// given
	persist := []string{t.TempDir(), t.TempDir()}
	empty := []string{t.TempDir(), filepath.Join(t.TempDir(), "absent")}

	// when
	write := tmpWrite(persist, "tok")
	persisted := tmpCheck(persist, empty, "tok")
	for _, d := range persist {
		_ = os.Remove(filepath.Join(d, tmpPrefix+"tok"))
	}
	clean := tmpCheck(persist, empty, "tok")

	// then
	if write.Outcome != OutcomeOK || !strings.Contains(write.Detail, "written") {
		t.Errorf("write: %+v", write)
	}
	if persisted.Outcome != OutcomeFail {
		t.Errorf("persisted: %+v", persisted)
	}
	if clean.Outcome != OutcomeOK {
		t.Errorf("clean: %+v", clean)
	}
	if err := os.WriteFile(filepath.Join(empty[0], "usb"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tmpCheck(persist, empty, "tok"); got.Outcome != OutcomeFail {
		t.Errorf("non-empty mount dir: %+v", got)
	}
}

func TestUnlimitedProbesReportFailure(t *testing.T) {
	if got := memHog(strconv.Itoa(32 << 20)); got.Outcome != OutcomeFail {
		t.Errorf("mem: %+v", got)
	}
	if got := taskLoop("3"); got.Outcome != OutcomeFail || !strings.Contains(got.Detail, "started 3") {
		t.Errorf("tasks: %+v", got)
	}
	if got := sleepProbe("1"); got.Outcome != OutcomeFail {
		t.Errorf("sleep: %+v", got)
	}
}

func TestRunProbe(t *testing.T) {
	var out, errOut bytes.Buffer
	dir := t.TempDir()

	code := RunProbe(context.Background(), []string{"write-ok", filepath.Join(dir, "f")}, &out, &errOut)

	if code != 0 || !strings.HasPrefix(out.String(), "PROBE write-ok ok wrote ") {
		t.Errorf("code %d, out %q", code, out.String())
	}
	if code := RunProbe(context.Background(), []string{"write-ok"}, &out, &errOut); code != 1 {
		t.Errorf("missing arg: code %d", code)
	}
	if code := RunProbe(context.Background(), []string{"nope"}, &out, &errOut); code != 2 {
		t.Errorf("unknown: code %d", code)
	}
}
