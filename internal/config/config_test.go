package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"actionbox/internal/limits"
)

const fingerprint = "0123456789abcdef0123456789ABCDEF01234567"

const minimal = `pinned_fingerprint = "` + fingerprint + `"
bwrap_path = "/opt/bwrap/bin/bwrap"
`

const specSketch = minimal + `npm_min_release_age = 7

[projects.pcts]
path = "/home/yminder/projects/pcts"

[toolchain]
ro_paths = ["/home/yminder/.sdkman/candidates", "/home/yminder/.local/share/mise/installs"]
env      = { JAVA_HOME = "/home/yminder/.sdkman/candidates/java/current" }
path     = ["/home/yminder/.sdkman/candidates/maven/current/bin"]

[limits]
timeout = "30m"
memory_max = "8G"
tasks_max = 4096
cpu_quota = "400%"

[retention]
jobs_per_project = 50
max_age = "14d"
`

var defaultCaps = limits.Limits{Timeout: 30 * time.Minute, MemoryMax: 8 << 30, TasksMax: 4096, CPUQuota: 400}

func load(t *testing.T, src string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func mustLoad(t *testing.T, src string) *Config {
	t.Helper()
	cfg, err := load(t, src)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLoadSpecSketch(t *testing.T) {
	cfg := mustLoad(t, specSketch)

	want := &Config{
		PinnedFingerprint:    strings.ToUpper(fingerprint),
		BwrapPath:            "/opt/bwrap/bin/bwrap",
		NPMMinReleaseAgeDays: 7,
		Projects: map[string]Project{
			"pcts": {Name: "pcts", Path: "/home/yminder/projects/pcts"},
		},
		Toolchain: Toolchain{
			ROPaths: []string{"/home/yminder/.sdkman/candidates", "/home/yminder/.local/share/mise/installs"},
			Env:     map[string]string{"JAVA_HOME": "/home/yminder/.sdkman/candidates/java/current"},
			Path:    []string{"/home/yminder/.sdkman/candidates/maven/current/bin"},
		},
		Caps:      defaultCaps,
		Retention: Retention{JobsPerProject: 50, MaxAge: 14 * 24 * time.Hour},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg := mustLoad(t, minimal)

	if cfg.Caps != defaultCaps {
		t.Errorf("limits = %+v, want %+v", cfg.Caps, defaultCaps)
	}
	if want := (Retention{JobsPerProject: 50, MaxAge: 14 * 24 * time.Hour}); cfg.Retention != want {
		t.Errorf("retention = %+v, want %+v", cfg.Retention, want)
	}
	if cfg.NPMMinReleaseAgeDays != 7 {
		t.Errorf("npm_min_release_age = %d, want 7", cfg.NPMMinReleaseAgeDays)
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("projects = %v, want none", cfg.Projects)
	}
}

func TestLoadPartialLimits(t *testing.T) {
	cfg := mustLoad(t, minimal+`[limits]
timeout = "1h"
`)
	want := defaultCaps
	want.Timeout = time.Hour
	if cfg.Caps != want {
		t.Errorf("limits = %+v, want %+v", cfg.Caps, want)
	}
}

func TestLoadZeroReleaseAge(t *testing.T) {
	cfg := mustLoad(t, minimal+"npm_min_release_age = 0\n")
	if cfg.NPMMinReleaseAgeDays != 0 {
		t.Errorf("npm_min_release_age = %d, want 0", cfg.NPMMinReleaseAgeDays)
	}
}

func TestLoadRejects(t *testing.T) {
	const bwrap = `bwrap_path = "/opt/bwrap"` + "\n"
	const fpr = `pinned_fingerprint = "` + fingerprint + `"` + "\n"
	tests := []struct {
		name    string
		toml    string
		wantMsg string
	}{
		{name: "not toml", toml: "pinned_fingerprint =", wantMsg: "toml"},
		{name: "unknown key", toml: minimal + "extra = 1\n", wantMsg: "extra"},
		{name: "unknown project key", toml: minimal + "[projects.a]\npath = \"/a\"\nrepo = \"x\"\n", wantMsg: "projects.a.repo"},
		{name: "unknown limits key", toml: minimal + "[limits]\nnice = 5\n", wantMsg: "limits.nice"},
		{name: "missing fingerprint", toml: bwrap, wantMsg: "pinned_fingerprint"},
		{name: "short fingerprint", toml: bwrap + `pinned_fingerprint = "ABCDEF"`, wantMsg: "pinned_fingerprint"},
		{name: "non-hex fingerprint", toml: bwrap + `pinned_fingerprint = "` + strings.Repeat("G", 40) + `"`, wantMsg: "pinned_fingerprint"},
		{name: "spaced fingerprint", toml: bwrap + `pinned_fingerprint = "0123 4567 89AB CDEF 0123  4567 89AB CDEF 0123 4567"`, wantMsg: "pinned_fingerprint"},
		{name: "missing bwrap_path", toml: fpr, wantMsg: "bwrap_path"},
		{name: "relative bwrap_path", toml: fpr + `bwrap_path = "bin/bwrap"`, wantMsg: "bwrap_path"},
		{name: "negative release age", toml: minimal + "npm_min_release_age = -1\n", wantMsg: "npm_min_release_age"},
		{name: "float release age", toml: minimal + "npm_min_release_age = 1.5\n", wantMsg: "npm_min_release_age"},
		{name: "string release age", toml: minimal + "npm_min_release_age = \"7d\"\n", wantMsg: "npm_min_release_age"},
		{name: "bad project name", toml: minimal + "[projects.My_Proj]\npath = \"/a\"\n", wantMsg: "My_Proj"},
		{name: "project name leading dash", toml: minimal + "[projects.-a]\npath = \"/a\"\n", wantMsg: "-a"},
		{name: "project missing path", toml: minimal + "[projects.a]\n", wantMsg: `project "a"`},
		{name: "project relative path", toml: minimal + "[projects.a]\npath = \"projects/a\"\n", wantMsg: "projects/a"},
		{name: "project unclean path", toml: minimal + "[projects.a]\npath = \"/home/x/../a/\"\n", wantMsg: "/home/x/../a/"},
		{name: "project root path", toml: minimal + "[projects.a]\npath = \"/\"\n", wantMsg: `project "a"`},
		{name: "project duplicate path", toml: minimal + "[projects.a]\npath = \"/p\"\n[projects.b]\npath = \"/p\"\n", wantMsg: "/p"},
		{name: "relative ro_path", toml: minimal + "[toolchain]\nro_paths = [\"opt\"]\n", wantMsg: "ro_paths"},
		{name: "relative toolchain path", toml: minimal + "[toolchain]\npath = [\"bin\"]\n", wantMsg: "toolchain.path"},
		{name: "toolchain path with colon", toml: minimal + "[toolchain]\npath = [\"/a:/b\"]\n", wantMsg: "toolchain.path"},
		{name: "bad env name", toml: minimal + "[toolchain]\nenv = { \"A-B\" = \"x\" }\n", wantMsg: "A-B"},
		{name: "reserved env name", toml: minimal + "[toolchain]\nenv = { HOME = \"/root\" }\n", wantMsg: "HOME"},
		{name: "env PATH", toml: minimal + "[toolchain]\nenv = { PATH = \"/bin\" }\n", wantMsg: "PATH"},
		{name: "env value NUL", toml: minimal + "[toolchain]\nenv = { X = \"a\\u0000b\" }\n", wantMsg: "X"},
		{name: "bad timeout cap", toml: minimal + "[limits]\ntimeout = \"forever\"\n", wantMsg: "timeout"},
		{name: "bad memory cap", toml: minimal + "[limits]\nmemory_max = \"8GiB\"\n", wantMsg: "memory_max"},
		{name: "zero tasks cap", toml: minimal + "[limits]\ntasks_max = 0\n", wantMsg: "tasks_max"},
		{name: "bad cpu cap", toml: minimal + "[limits]\ncpu_quota = \"0%\"\n", wantMsg: "cpu_quota"},
		{name: "zero jobs_per_project", toml: minimal + "[retention]\njobs_per_project = 0\n", wantMsg: "jobs_per_project"},
		{name: "bad max_age", toml: minimal + "[retention]\nmax_age = \"2w\"\n", wantMsg: "max_age"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.toml)
			if err == nil {
				t.Fatal("want error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
}

func TestProjectForDir(t *testing.T) {
	cfg := mustLoad(t, minimal+`
[projects.outer]
path = "/home/u/projects"
[projects.app]
path = "/home/u/projects/app"
`)
	tests := []struct {
		dir      string
		wantName string
		wantOK   bool
	}{
		{dir: "/home/u/projects/app", wantName: "app", wantOK: true},
		{dir: "/home/u/projects/app/backend/src", wantName: "app", wantOK: true},
		{dir: "/home/u/projects/app/", wantName: "app", wantOK: true},
		{dir: "/home/u/projects/application", wantName: "outer", wantOK: true},
		{dir: "/home/u/projects", wantName: "outer", wantOK: true},
		{dir: "/home/u/project", wantOK: false},
		{dir: "/home/u", wantOK: false},
		{dir: "/", wantOK: false},
		{dir: "relative/app", wantOK: false},
		{dir: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			name, project, ok := cfg.ProjectForDir(tt.dir)
			if ok != tt.wantOK || name != tt.wantName {
				t.Fatalf("got (%q, %v), want (%q, %v)", name, ok, tt.wantName, tt.wantOK)
			}
			if ok && project != cfg.Projects[name] {
				t.Errorf("project = %+v, want %+v", project, cfg.Projects[name])
			}
		})
	}
}

func TestClampLimits(t *testing.T) {
	cfg := mustLoad(t, minimal)
	got := cfg.ClampLimits(limits.Limits{Timeout: 2 * time.Hour, MemoryMax: 6 << 30})
	want := limits.Limits{Timeout: 30 * time.Minute, MemoryMax: 6 << 30, TasksMax: 4096, CPUQuota: 400}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestPaths(t *testing.T) {
	p := PathsForHome("/home/u")
	tests := []struct{ got, want string }{
		{p.ConfigFile(), "/home/u/.config/actionbox/config.toml"},
		{p.Keyring(), "/home/u/.local/share/actionbox/keyring.gpg"},
		{p.MavenCache("pcts"), "/home/u/.local/share/actionbox/projects/pcts/m2"},
		{p.Ledger("pcts"), "/home/u/.local/share/actionbox/projects/pcts/ledger.jsonl"},
		{p.NPMCache("pcts"), "/home/u/.local/share/actionbox/projects/pcts/npm"},
		{p.ContainersStore("pcts"), "/home/u/.local/share/actionbox/projects/pcts/containers/store"},
		{p.ContainersRun("pcts"), "/home/u/.local/share/actionbox/projects/pcts/containers/run"},
		{p.Lock("pcts"), "/home/u/.local/share/actionbox/projects/pcts/lock"},
		{p.Socket(), "/home/u/.local/state/actionbox/broker.sock"},
		{p.TrustFile("pcts"), "/home/u/.local/state/actionbox/trust/pcts.json"},
		{p.FetchRequest("r1"), "/home/u/.local/state/actionbox/fetch/r1.json"},
		{p.JobsDir("pcts"), "/home/u/.local/state/actionbox/jobs/pcts"},
		{p.JobDir("pcts", "j1"), "/home/u/.local/state/actionbox/jobs/pcts/j1"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %s, want %s", tt.got, tt.want)
		}
	}
}

func TestDefaultPaths(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p != PathsForHome("/home/someone") {
		t.Errorf("got %+v", p)
	}
}
