package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"actionbox/internal/limits"
)

var ErrInvalid = errors.New("invalid config")

var (
	DefaultCaps = limits.Limits{Timeout: 30 * time.Minute, MemoryMax: 8 << 30, TasksMax: 4096, CPUQuota: 400}

	DefaultRetention = Retention{JobsPerProject: 50, MaxAge: 14 * 24 * time.Hour}
)

const DefaultNPMMinReleaseAgeDays = 7

type Config struct {
	PinnedFingerprint    string
	BwrapPath            string
	NPMMinReleaseAgeDays int
	Projects             map[string]Project
	Toolchain            Toolchain
	Caps                 limits.Limits
	Retention            Retention
}

type Project struct {
	Name string
	Path string
}

type Toolchain struct {
	ROPaths []string
	Env     map[string]string
	Path    []string
}

type Retention struct {
	JobsPerProject int
	MaxAge         time.Duration
}

type rawConfig struct {
	PinnedFingerprint    string                `toml:"pinned_fingerprint"`
	BwrapPath            string                `toml:"bwrap_path"`
	NPMMinReleaseAgeDays *int                  `toml:"npm_min_release_age"`
	Projects             map[string]rawProject `toml:"projects"`
	Toolchain            rawToolchain          `toml:"toolchain"`
	Limits               limits.Spec           `toml:"limits"`
	Retention            rawRetention          `toml:"retention"`
}

type rawProject struct {
	Path string `toml:"path"`
}

type rawToolchain struct {
	ROPaths []string          `toml:"ro_paths"`
	Env     map[string]string `toml:"env"`
	Path    []string          `toml:"path"`
}

type rawRetention struct {
	JobsPerProject *int   `toml:"jobs_per_project"`
	MaxAge         string `toml:"max_age"`
}

var (
	fingerprintPattern = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)
	projectNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// set by engine itself, spec §8.7 step 5
var reservedEnv = []string{
	"PATH", "HOME", "LANG", "TERM", "TMPDIR", "MAVEN_OPTS", "npm_config_cache",
	"XDG_RUNTIME_DIR", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF", "DOCKER_HOST", "TESTCONTAINERS_RYUK_DISABLED",
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	return cfg, nil
}

func parse(data []byte) (*Config, error) {
	var raw rawConfig
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("unknown key %s", undecoded[0])
	}

	cfg := &Config{NPMMinReleaseAgeDays: DefaultNPMMinReleaseAgeDays}
	if cfg.PinnedFingerprint, err = parseFingerprint(raw.PinnedFingerprint); err != nil {
		return nil, err
	}
	if err := checkAbsPath(raw.BwrapPath); err != nil {
		return nil, fmt.Errorf("bwrap_path: %w", err)
	}
	cfg.BwrapPath = raw.BwrapPath
	if raw.NPMMinReleaseAgeDays != nil {
		if *raw.NPMMinReleaseAgeDays < 0 {
			return nil, fmt.Errorf("npm_min_release_age: must not be negative, got %d", *raw.NPMMinReleaseAgeDays)
		}
		cfg.NPMMinReleaseAgeDays = *raw.NPMMinReleaseAgeDays
	}
	if cfg.Projects, err = parseProjects(raw.Projects); err != nil {
		return nil, err
	}
	if cfg.Toolchain, err = parseToolchain(raw.Toolchain); err != nil {
		return nil, err
	}
	if cfg.Caps, err = parseCaps(raw.Limits, md.IsDefined("limits", "tasks_max")); err != nil {
		return nil, fmt.Errorf("limits.%w", err)
	}
	if cfg.Retention, err = parseRetention(raw.Retention); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parseFingerprint(s string) (string, error) {
	if !fingerprintPattern.MatchString(s) {
		return "", fmt.Errorf("pinned_fingerprint: want 40 hex digits, got %q", s)
	}
	return strings.ToUpper(s), nil
}

func parseProjects(raw map[string]rawProject) (map[string]Project, error) {
	projects := make(map[string]Project, len(raw))
	byPath := make(map[string]string, len(raw))
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		path := raw[name].Path
		if !projectNamePattern.MatchString(name) {
			return nil, fmt.Errorf("project %q: name must match %s", name, projectNamePattern)
		}
		if err := checkAbsPath(path); err != nil {
			return nil, fmt.Errorf("project %q: path: %w", name, err)
		}
		if path == "/" {
			return nil, fmt.Errorf("project %q: path must not be /", name)
		}
		if other, dup := byPath[path]; dup {
			return nil, fmt.Errorf("project %q: path %q already used by project %q", name, path, other)
		}
		byPath[path] = name
		projects[name] = Project{Name: name, Path: path}
	}
	return projects, nil
}

func parseToolchain(raw rawToolchain) (Toolchain, error) {
	for _, p := range raw.ROPaths {
		if err := checkAbsPath(p); err != nil {
			return Toolchain{}, fmt.Errorf("toolchain.ro_paths: %w", err)
		}
	}
	for _, p := range raw.Path {
		if err := checkAbsPath(p); err != nil {
			return Toolchain{}, fmt.Errorf("toolchain.path: %w", err)
		}
		if strings.Contains(p, ":") {
			return Toolchain{}, fmt.Errorf("toolchain.path: %q contains ':'", p)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Env)) {
		if err := checkEnv(name, raw.Env[name]); err != nil {
			return Toolchain{}, fmt.Errorf("toolchain.env %q: %w", name, err)
		}
	}
	return Toolchain(raw), nil
}

func checkEnv(name, value string) error {
	switch {
	case !envNamePattern.MatchString(name):
		return fmt.Errorf("name must match %s", envNamePattern)
	case slices.Contains(reservedEnv, name):
		return errors.New("set by actionbox itself")
	case strings.ContainsRune(value, 0):
		return errors.New("value contains NUL")
	}
	return nil
}

func parseCaps(spec limits.Spec, tasksMaxSet bool) (limits.Limits, error) {
	if tasksMaxSet && spec.TasksMax == 0 {
		return limits.Limits{}, errors.New("tasks_max: must be positive")
	}
	caps, err := spec.Parse()
	if err != nil {
		return limits.Limits{}, err
	}
	return caps.WithDefaults(DefaultCaps), nil
}

func parseRetention(raw rawRetention) (Retention, error) {
	r := DefaultRetention
	if raw.JobsPerProject != nil {
		if *raw.JobsPerProject <= 0 {
			return Retention{}, fmt.Errorf("retention.jobs_per_project: must be positive, got %d", *raw.JobsPerProject)
		}
		r.JobsPerProject = *raw.JobsPerProject
	}
	if raw.MaxAge != "" {
		age, err := limits.ParseDuration(raw.MaxAge)
		if err != nil {
			return Retention{}, fmt.Errorf("retention.max_age: %w", err)
		}
		r.MaxAge = age
	}
	return r, nil
}

func checkAbsPath(p string) error {
	switch {
	case p == "":
		return errors.New("is required")
	case !filepath.IsAbs(p):
		return fmt.Errorf("%q must be absolute", p)
	case filepath.Clean(p) != p:
		return fmt.Errorf("%q must be in clean form %q", p, filepath.Clean(p))
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("%q contains NUL", p)
	}
	return nil
}

// dir must be realpath'd: prefix match on a symlinked path picks the wrong project
func (c *Config) ProjectForDir(dir string) (string, Project, bool) {
	if !filepath.IsAbs(dir) {
		return "", Project{}, false
	}
	dir = filepath.Clean(dir)
	var best Project
	for _, p := range c.Projects {
		if (dir == p.Path || strings.HasPrefix(dir, p.Path+"/")) && len(p.Path) > len(best.Path) {
			best = p
		}
	}
	return best.Name, best, best.Name != ""
}

func (c *Config) ClampLimits(l limits.Limits) limits.Limits {
	return c.Caps.Clamp(l)
}
