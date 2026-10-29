package eventlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newLogPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "events.jsonl")
}

func openLog(t *testing.T, path string) *Log {
	t.Helper()
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
}

func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("line %q: %v", line, err)
	}
	return m
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestOpen_createsDirAndFileWithPrivateModes(t *testing.T) {
	path := newLogPath(t)
	openLog(t, path)

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
}

func TestAppend_chainsLinesAndVerifyAcceptsThem(t *testing.T) {
	// given
	path := newLogPath(t)
	l := openLog(t, path)

	// when
	events := []any{
		JobStart{Project: "pcts", JobID: "j1", Action: "test", Params: map[string]string{"module": "core"}, DefinitionsCommit: "abc"},
		JobEnd{Project: "pcts", JobID: "j1", Action: "test", DefinitionsCommit: "abc", State: "succeeded", ExitCode: 0},
		FetchDecision{Project: "pcts", RequestID: "r1", Ecosystem: "maven", Coordinates: []string{"g:a:1"}, Decision: "approved", Result: "fetched",
			Files: []FetchedFile{{Coordinate: "g:a:1", Path: "m2/g/a/1/a-1.jar", Integrity: "sha256-xyz", Size: 3}}},
	}
	for _, e := range events {
		if err := l.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// then
	lines := readLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	first := decode(t, lines[0])
	if first["prev"] != strings.Repeat("0", 64) {
		t.Errorf("first prev = %v", first["prev"])
	}
	if first["kind"] != "job_start" || first["job_id"] != "j1" || first["definitions_commit"] != "abc" {
		t.Errorf("first line = %v", first)
	}
	ts, err := time.Parse(time.RFC3339Nano, first["ts"].(string))
	if err != nil || ts.Location() != time.UTC {
		t.Errorf("ts = %v (%v)", first["ts"], err)
	}
	second := decode(t, lines[1])
	if second["prev"] != sha(lines[0]) || second["kind"] != "job_end" || second["state"] != "succeeded" {
		t.Errorf("second line = %v", second)
	}
	if _, ok := second["exit_code"]; !ok {
		t.Error("exit_code 0 must be kept")
	}
	third := decode(t, lines[2])
	if third["prev"] != sha(lines[1]) || third["kind"] != "fetch_decision" || third["request_id"] != "r1" {
		t.Errorf("third line = %v", third)
	}
	files := third["files"].([]any)
	if files[0].(map[string]any)["integrity"] != "sha256-xyz" {
		t.Errorf("files = %v", files)
	}

	n, err := Verify(path)
	if err != nil || n != 3 {
		t.Errorf("Verify = %d, %v; want 3, nil", n, err)
	}
}

func TestAppend_keepsCallerTimestamp(t *testing.T) {
	path := newLogPath(t)
	l := openLog(t, path)

	if err := l.Append(map[string]any{"kind": "custom", "ts": "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	if got := decode(t, readLines(t, path)[0])["ts"]; got != "2026-01-01T00:00:00Z" {
		t.Errorf("ts = %v", got)
	}
}

func TestAppend_rejectsNonObject(t *testing.T) {
	l := openLog(t, newLogPath(t))

	if err := l.Append([]string{"x"}); err == nil {
		t.Error("want error for JSON array")
	}
}

func TestAppend_continuesChainAcrossReopen(t *testing.T) {
	path := newLogPath(t)
	l := openLog(t, path)
	if err := l.Append(JobStart{JobID: "a"}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	reopened := openLog(t, path)
	if err := reopened.Append(JobEnd{JobID: "a"}); err != nil {
		t.Fatal(err)
	}

	if n, err := Verify(path); err != nil || n != 2 {
		t.Errorf("Verify = %d, %v", n, err)
	}
}

func TestAppend_findsLastLineBeyondOneReadChunk(t *testing.T) {
	path := newLogPath(t)
	l := openLog(t, path)
	big := strings.Repeat("x", 3*chunkSize)
	if err := l.Append(map[string]any{"kind": "big", "blob": big}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(map[string]any{"kind": "big", "blob": big}); err != nil {
		t.Fatal(err)
	}

	if n, err := Verify(path); err != nil || n != 2 {
		t.Errorf("Verify = %d, %v", n, err)
	}
}

func TestVerify_missingFileIsEmpty(t *testing.T) {
	n, err := Verify(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || n != 0 {
		t.Errorf("Verify = %d, %v; want 0, nil", n, err)
	}
}

func writeChain(t *testing.T, n int) string {
	t.Helper()
	path := newLogPath(t)
	l := openLog(t, path)
	for i := range n {
		if err := l.Append(JobStart{JobID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func rewrite(t *testing.T, path string, lines [][]byte, trailingNewline bool) {
	t.Helper()
	data := bytes.Join(lines, []byte("\n"))
	if trailingNewline {
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerify_tamperedMiddleLineReportsNextLine(t *testing.T) {
	// given
	path := writeChain(t, 5)
	lines := readLines(t, path)
	lines[2] = bytes.Replace(lines[2], []byte(`"job_id":"2"`), []byte(`"job_id":"X"`), 1)
	rewrite(t, path, lines, true)

	// when
	_, err := Verify(path)

	// then
	if err == nil || !strings.Contains(err.Error(), "line 4") || !strings.Contains(err.Error(), "prev mismatch") {
		t.Errorf("err = %v; want line 4 prev mismatch", err)
	}
}

func TestVerify_reportsBadJSON(t *testing.T) {
	path := writeChain(t, 3)
	lines := readLines(t, path)
	lines[1] = []byte("{not json")
	rewrite(t, path, lines, true)

	_, err := Verify(path)

	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "bad JSON") {
		t.Errorf("err = %v; want line 2 bad JSON", err)
	}
}

func TestVerify_reportsMissingPrev(t *testing.T) {
	path := writeChain(t, 2)
	lines := readLines(t, path)
	lines[0] = []byte(`{"kind":"job_start"}`)
	rewrite(t, path, lines, true)

	_, err := Verify(path)

	if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "missing prev") {
		t.Errorf("err = %v; want line 1 missing prev", err)
	}
}

func TestVerify_reportsTruncatedLastLine(t *testing.T) {
	path := writeChain(t, 3)
	lines := readLines(t, path)
	lines[2] = lines[2][:len(lines[2])/2]
	rewrite(t, path, lines, false)

	_, err := Verify(path)

	if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("err = %v; want line 3 truncated", err)
	}
}

func TestAppend_refusesAfterTruncatedLastLine(t *testing.T) {
	path := writeChain(t, 2)
	lines := readLines(t, path)
	rewrite(t, path, [][]byte{lines[0], lines[1][:10]}, false)
	l := openLog(t, path)

	if err := l.Append(JobStart{}); err == nil {
		t.Error("want error appending after truncated line")
	}
}

func TestAppend_concurrentGoroutinesChainCorrectly(t *testing.T) {
	path := newLogPath(t)
	l := openLog(t, path)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if err := l.Append(JobStart{JobID: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if n, err := Verify(path); err != nil || n != 50 {
		t.Errorf("Verify = %d, %v; want 50, nil", n, err)
	}
}

func TestAppend_concurrentInstancesOnSameFileChainCorrectly(t *testing.T) {
	path := newLogPath(t)
	a := openLog(t, path)
	b := openLog(t, path)

	var wg sync.WaitGroup
	for i := range 40 {
		l := a
		if i%2 == 1 {
			l = b
		}
		wg.Go(func() {
			if err := l.Append(JobStart{JobID: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if n, err := Verify(path); err != nil || n != 40 {
		t.Errorf("Verify = %d, %v; want 40, nil", n, err)
	}
}
