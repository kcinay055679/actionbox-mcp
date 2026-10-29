// Package trustfixtures builds the A3 trust fixtures (testdata/trust/gen-fixtures.sh) once per test binary.
package trustfixtures

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type Case struct {
	Name    string  `json:"name"`
	Repo    string  `json:"repo"`
	Expect  string  `json:"expect"`
	Reason  *string `json:"reason"`
	Pin     string  `json:"pin"`
	Keyring string  `json:"keyring"`
	Trusted *string `json:"trusted"`
	Marker  *string `json:"marker"`
}

type Set struct {
	Dir   string
	Owner string
	Cases []Case
}

func (s Set) Case(t testing.TB, name string) Case {
	t.Helper()
	for _, c := range s.Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no fixture case %q", name)
	return Case{}
}

var state struct {
	once sync.Once
	root string
	set  Set
	err  error
}

// Load skips the test when a required tool is missing.
func Load(t testing.TB) Set {
	t.Helper()
	for _, tool := range []string{"bash", "git", "gpg", "gpgv", "gpgconf", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	state.once.Do(generate)
	if state.err != nil {
		t.Fatal(state.err)
	}
	return state.set
}

// Cleanup is meant for TestMain, after m.Run.
func Cleanup() {
	if state.root != "" {
		os.RemoveAll(state.root)
	}
}

func generate() {
	// short path: gpg-agent socket path limit
	state.root, state.err = os.MkdirTemp("", "ab")
	if state.err != nil {
		return
	}
	dir := filepath.Join(state.root, "f")
	out, err := exec.Command("bash", generatorPath(), dir).CombinedOutput()
	if err != nil {
		state.err = errors.Join(err, errors.New(string(out)))
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		state.err = err
		return
	}
	var manifest struct {
		Owner string `json:"owner"`
		Cases []Case `json:"cases"`
	}
	if state.err = json.Unmarshal(raw, &manifest); state.err != nil {
		return
	}
	state.set = Set{Dir: dir, Owner: manifest.Owner, Cases: manifest.Cases}
}

func generatorPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "../../../testdata/trust/gen-fixtures.sh")
}
