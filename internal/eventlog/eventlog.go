package eventlog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var genesisPrev = strings.Repeat("0", 64)

const chunkSize = 4096

type Log struct {
	mu sync.Mutex
	f  *os.File
}

type Kinded interface {
	Kind() string
}

func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("event log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	return &Log{f: f}, nil
}

func (l *Log) Close() error {
	return l.f.Close()
}

func (l *Log) Append(event any) error {
	fields, err := toFields(event)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock event log: %w", err)
	}
	defer syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)

	last, err := lastLine(l.f)
	if err != nil {
		return err
	}
	prev := genesisPrev
	if last != nil {
		prev = hashLine(last)
	}
	fields["prev"] = mustMarshal(prev)

	line, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	return nil
}

func toFields(event any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("event must encode to a JSON object, got %s", raw)
	}
	if k, ok := event.(Kinded); ok {
		if _, set := fields["kind"]; !set {
			fields["kind"] = mustMarshal(k.Kind())
		}
	}
	if _, set := fields["ts"]; !set {
		fields["ts"] = mustMarshal(time.Now().UTC().Format(time.RFC3339Nano))
	}
	return fields, nil
}

func mustMarshal(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func hashLine(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}

func lastLine(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat event log: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return nil, nil
	}

	var tail []byte
	for end := size; end > 0; {
		start := max(end-chunkSize, 0)
		chunk := make([]byte, end-start)
		if _, err := f.ReadAt(chunk, start); err != nil {
			return nil, fmt.Errorf("read event log: %w", err)
		}
		tail = append(chunk, tail...)
		if end == size && tail[len(tail)-1] != '\n' {
			// appending would chain onto a partial line and hide the damage
			return nil, errors.New("event log ends with a truncated line, run verify")
		}
		if i := bytes.LastIndexByte(tail[:len(tail)-1], '\n'); i >= 0 {
			return tail[i+1 : len(tail)-1], nil
		}
		end = start
	}
	return tail[:len(tail)-1], nil
}

func Verify(path string) (int, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()

	r := bufio.NewReader(f)
	expected := genesisPrev
	for n := 1; ; n++ {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			if len(line) > 0 {
				return n - 1, fmt.Errorf("line %d: truncated, no trailing newline", n)
			}
			return n - 1, nil
		}
		if err != nil {
			return n - 1, fmt.Errorf("read event log: %w", err)
		}
		line = line[:len(line)-1]

		var row struct {
			Prev *string `json:"prev"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return n - 1, fmt.Errorf("line %d: bad JSON: %v", n, err)
		}
		if row.Prev == nil {
			return n - 1, fmt.Errorf("line %d: missing prev", n)
		}
		if *row.Prev != expected {
			return n - 1, fmt.Errorf("line %d: prev mismatch, want %s, got %s", n, expected, *row.Prev)
		}
		expected = hashLine(line)
	}
}

// Head is the sha256 of the last line, the anchor the owner keeps outside the log ("" for an empty log).
func (l *Log) Head() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line, err := lastLine(l.f)
	if err != nil || line == nil {
		return "", err
	}
	return hashLine(line), nil
}
