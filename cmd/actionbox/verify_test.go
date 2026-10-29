package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"actionbox/internal/trust/trustfixtures"
)

func TestMain(m *testing.M) {
	code := m.Run()
	trustfixtures.Cleanup()
	os.Exit(code)
}

func TestParseVerifyArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    verifyArgs
		wantErr bool
	}{
		{args: nil, want: verifyArgs{}},
		{args: []string{"pcts"}, want: verifyArgs{project: "pcts"}},
		{args: []string{"pcts", "--accept", "abc"}, want: verifyArgs{project: "pcts", accept: "abc"}},
		{args: []string{"--accept=abc", "pcts"}, want: verifyArgs{project: "pcts", accept: "abc"}},
		{args: []string{"--accept"}, wantErr: true},
		{args: []string{"--force"}, wantErr: true},
		{args: []string{"a", "b"}, wantErr: true},
		{args: []string{"--log"}, want: verifyArgs{log: true}},
		{args: []string{"--log", "pcts"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseVerifyArgs(tt.args)
		if (err != nil) != tt.wantErr || (!tt.wantErr && got != tt.want) {
			t.Errorf("parseVerifyArgs(%q) = %+v, %v", tt.args, got, err)
		}
	}
}

func TestVerifyCommand(t *testing.T) {
	set := trustfixtures.Load(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, set, map[string]string{
		"good":     "signed-clean",
		"unsigned": "unsigned-commit",
	})

	tests := []struct {
		args     []string
		wantCode int
		wantOut  string
	}{
		{args: []string{"verify", "good"}, wantCode: 0, wantOut: "good: trusted "},
		{args: []string{"verify", "unsigned"}, wantCode: 1, wantOut: "unsigned: untrusted: signature status"},
		{args: []string{"verify", "missing"}, wantCode: 1, wantOut: `unregistered project "missing"`},
		{args: []string{"verify", "--bogus"}, wantCode: 2, wantOut: "unknown flag"},
		{args: []string{"verify", "--log"}, wantCode: 0, wantOut: "event log: 0 lines, chain intact"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tt.args, strings.NewReader(""), &stdout, &stderr)
			out := stdout.String() + stderr.String()
			if code != tt.wantCode || !strings.Contains(out, tt.wantOut) {
				t.Errorf("code %d, output %q; want %d containing %q", code, out, tt.wantCode, tt.wantOut)
			}
		})
	}
}

func writeConfig(t *testing.T, home string, set trustfixtures.Set, projects map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("pinned_fingerprint = \"" + set.Owner + "\"\nbwrap_path = \"/usr/bin/bwrap\"\n")
	for name, caseName := range projects {
		b.WriteString("\n[projects." + name + "]\npath = \"" + filepath.Join(set.Dir, set.Case(t, caseName).Repo) + "\"\n")
	}
	cfgDir := filepath.Join(home, ".config", "actionbox")
	dataDir := filepath.Join(home, ".local", "share", "actionbox")
	for _, d := range []string{cfgDir, dataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := os.ReadFile(filepath.Join(set.Dir, "keys", "owner.gpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "keyring.gpg"), keyring, 0o600); err != nil {
		t.Fatal(err)
	}
}
