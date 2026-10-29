package engine

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a fake `podman system service` when re-executed by a test.
func TestMain(m *testing.M) {
	if sock := os.Getenv("ACTIONBOX_FAKE_PODMAN_SOCK"); sock != "" {
		serveFakePodman(sock)
		return
	}
	os.Exit(m.Run())
}

func serveFakePodman(sock string) {
	l, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			_, _ = w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func fakeServiceCmd(sock string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "ACTIONBOX_FAKE_PODMAN_SOCK="+sock)
	return cmd
}

func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ab")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "podman.sock")
}

func TestParseJobInitArgs(t *testing.T) {
	tests := []struct {
		in       []string
		wantMeta string
		wantArgv []string
		wantErr  bool
	}{
		{in: []string{"--meta", "/j/meta.json", "--", "mvn", "--", "-x"}, wantMeta: "/j/meta.json", wantArgv: []string{"mvn", "--", "-x"}},
		{in: []string{"--meta", "/j/meta.json", "--"}, wantErr: true},
		{in: []string{"--meta", "/j/meta.json", "mvn"}, wantErr: true},
		{in: []string{"--", "mvn"}, wantErr: true},
		{in: nil, wantErr: true},
	}
	for _, tt := range tests {
		meta, argv, err := ParseJobInitArgs(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("%q: err = %v", tt.in, err)
			continue
		}
		if meta != tt.wantMeta || !slices.Equal(argv, tt.wantArgv) {
			t.Errorf("%q: got %q %q", tt.in, meta, argv)
		}
	}
}

func writeTestMeta(t *testing.T, m Meta) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meta.json")
	if err := WriteMeta(path, m); err != nil {
		t.Fatal(err)
	}
	return path
}

func testInit(stderr *bytes.Buffer) jobInit {
	return jobInit{serviceCmd: fakeServiceCmd, pingTimeout: 5 * time.Second, stderr: stderr}
}

func TestJobInitRunsArgv(t *testing.T) {
	// given
	repo := t.TempDir()
	b := prepare(t, repo, nil)
	metaPath := writeTestMeta(t, Meta{Mounts: b.Mounts()})

	// when
	code := testInit(&bytes.Buffer{}).run(context.Background(), metaPath, []string{"sh", "-c", "exit 7"})

	// then
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestJobInitAbortsOnMountMismatch(t *testing.T) {
	// given
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	metaPath := writeTestMeta(t, Meta{Mounts: []Mount{{Path: repo, Dev: 1, Ino: 1}}})
	var stderr bytes.Buffer

	// when
	code := testInit(&stderr).run(context.Background(), metaPath, []string{"touch", marker})

	// then
	if code != exitInitFailed {
		t.Errorf("exit code = %d, want %d", code, exitInitFailed)
	}
	if !strings.Contains(stderr.String(), repo) {
		t.Errorf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("argv ran despite mismatch")
	}
}

func TestJobInitStartsPodmanServiceForContainerJobs(t *testing.T) {
	// given
	sock := shortSock(t)
	metaPath := writeTestMeta(t, Meta{PodmanSocket: sock})

	// when
	code := testInit(&bytes.Buffer{}).run(context.Background(), metaPath, []string{"test", "-S", sock})

	// then
	if code != 0 {
		t.Errorf("exit code = %d, socket not up while argv ran", code)
	}
}

func TestWaitForPing(t *testing.T) {
	// given
	sock := shortSock(t)
	go func() {
		time.Sleep(200 * time.Millisecond)
		serveFakePodman(sock)
	}()

	// when
	err := waitForPing(context.Background(), sock, 5*time.Second, nil)

	// then
	if err != nil {
		t.Fatal(err)
	}
}

func TestWaitForPingTimesOut(t *testing.T) {
	// given
	sock := shortSock(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	// when
	err = waitForPing(context.Background(), sock, 300*time.Millisecond, nil)

	// then
	if err == nil {
		t.Fatal("want timeout")
	}
}

func TestWaitForPingStopsWhenServiceExits(t *testing.T) {
	// given
	exited := make(chan error, 1)
	exited <- nil
	start := time.Now()

	// when
	err := waitForPing(context.Background(), shortSock(t), 10*time.Second, exited)

	// then
	if err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}
