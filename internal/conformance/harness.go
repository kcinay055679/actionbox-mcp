package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/job"
	"actionbox/internal/limits"
)

const (
	CanaryEnv    = "CANARY_TOKEN"
	writesDir    = "target"
	externalAddr = "1.1.1.1:443"
)

var defaultProbeLimits = limits.Limits{Timeout: time.Minute, MemoryMax: 1 << 30, TasksMax: 512, CPUQuota: 200}

type Host struct {
	NewRunner func(paths config.Paths, afterBinds func()) job.Runner
	Config    *config.Config
	SelfExe   string
	Home      string
	Podman    Podman
	// Dial: host-side reachability, a P5 pass is vacuous when the host is offline too
	Dial func(ctx context.Context, network, addr string) error
}

type Podman interface {
	Seed(ctx context.Context, containersDir, image string) error
	HashTree(ctx context.Context, dir string) (string, error)
	RemoveAll(ctx context.Context, dir string) error
}

type harness struct {
	host   Host
	base   string
	canary string
	out    io.Writer
}

func Run(ctx context.Context, host Host, base string, out io.Writer) ([]Result, error) {
	if host.Home == "" || host.SelfExe == "" || host.Config == nil || host.NewRunner == nil {
		return nil, fmt.Errorf("conformance: incomplete host setup")
	}
	h := &harness{host: host, base: base, canary: randomToken(), out: out}
	restore := setEnv(CanaryEnv, h.canary)
	defer restore()

	var results []Result
	for _, containers := range []bool{false, true} {
		m, err := h.newMode(ctx, containers)
		if err != nil {
			return results, err
		}
		for _, p := range modeProbes {
			if p.containersOnly && !containers {
				continue
			}
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			for _, r := range p.run(ctx, m) {
				r.Mode = ModeName(containers)
				results = append(results, r)
				WriteResult(out, r)
			}
		}
	}
	return results, nil
}

func setEnv(name, value string) (restore func()) {
	old, had := os.LookupEnv(name)
	_ = os.Setenv(name, value)
	return func() {
		if had {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	}
}

func randomToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type mode struct {
	h           *harness
	containers  bool
	project     string
	paths       config.Paths
	repo        string
	victim      string
	token       string
	storeBefore string
	seedImage   string
	setupNote   string
}

func (h *harness) newMode(ctx context.Context, containers bool) (*mode, error) {
	name := "conformance-plain"
	if containers {
		name = "conformance-containers"
	}
	dir := filepath.Join(h.base, name)
	m := &mode{
		h: h, containers: containers, project: name,
		paths:  config.PathsForHome(dir),
		repo:   filepath.Join(dir, "repo"),
		victim: filepath.Join(dir, "victim"),
		token:  randomToken(),
	}
	if err := writeFixture(m.repo, m.victim); err != nil {
		return nil, err
	}
	containersDir := m.containersDir()
	for _, d := range []string{m.paths.MavenCache(name), m.paths.NPMCache(name), containersDir + "/store", containersDir + "/run"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if containers && h.host.Podman != nil {
		m.seed(ctx)
	}
	return m, nil
}

func (m *mode) containersDir() string { return filepath.Dir(m.paths.ContainersStore(m.project)) }

func (m *mode) seed(ctx context.Context) {
	podman := m.h.host.Podman
	m.seedImage = SeedImage
	if err := podman.Seed(ctx, m.containersDir(), SeedImage); err != nil {
		m.seedImage = ""
		m.setupNote = fmt.Sprintf("seeding failed (%v), store starts empty", err)
	}
	sum, err := podman.HashTree(ctx, m.containersDir())
	if err != nil {
		m.setupNote = fmt.Sprintf("hash store before: %v", err)
		return
	}
	m.storeBefore = sum
}

var fixtureFiles = map[string]string{
	".agent-actions.toml":      "# conformance fixture\n",
	".git/HEAD":                "ref: refs/heads/main\n",
	".gitattributes":           "* text=auto\n",
	writesDir + "/sub/.bashrc": "# conformance fixture\n",
	".mvn/maven.config":        "\n",
	".idea/workspace.xml":      "<project/>\n",
	"src/Main.java":            "class Main {}\n",
}

var fixtureDirs = []string{".git/hooks", ".mvn", ".idea"}

func writeFixture(repo, victim string) error {
	for _, d := range fixtureDirs {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			return err
		}
	}
	for rel, content := range fixtureFiles {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(victim, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(victim, "keep"), []byte(probeContent), 0o644)
}

func (m *mode) jobDir(id string) string { return m.paths.JobDir(m.project, id) }

func (m *mode) spec(id, repo string, lim limits.Limits, argv []string) job.Spec {
	caps := m.h.host.Config.Caps
	if caps == (limits.Limits{}) {
		caps = config.DefaultCaps
	}
	return job.Spec{
		ID: id, Project: m.project, RepoPath: repo, Dir: m.jobDir(id),
		Action:     "conformance-" + id,
		Argv:       append([]string{m.h.host.SelfExe, "probe"}, argv...),
		Writes:     []string{writesDir},
		Containers: m.containers,
		Limits:     caps.Clamp(lim),
	}
}

type jobRun struct {
	result job.Result
	output []byte
	err    error
}

func (m *mode) run(ctx context.Context, id string, lim limits.Limits, afterBinds func(), argv ...string) jobRun {
	return m.runIn(ctx, id, m.repo, lim, afterBinds, argv...)
}

func (m *mode) runIn(ctx context.Context, id, repo string, lim limits.Limits, afterBinds func(), argv ...string) jobRun {
	spec := m.spec(id, repo, lim, argv)
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return jobRun{err: err}
	}
	var out bytes.Buffer
	res, err := m.h.host.NewRunner(m.paths, afterBinds).Run(ctx, spec, &out)
	return jobRun{result: res, output: out.Bytes(), err: err}
}

// verdict maps the probe's own line; a missing line is a failure, never a pass.
func (j jobRun) verdict(probe string) (Status, string) {
	if j.err != nil {
		return Fail, "job not started: " + j.err.Error()
	}
	line, found := ParseLine(j.output, probe)
	if !found {
		return Fail, fmt.Sprintf("no probe result, job %s; output: %s", describe(j.result), tail(j.output))
	}
	switch line.Outcome {
	case OutcomeOK:
		return Pass, line.Detail
	case OutcomeSkip:
		return Skip, line.Detail
	}
	return Fail, line.Detail
}

func describe(r job.Result) string {
	switch {
	case r.TimedOut:
		return fmt.Sprintf("timed out (exit %d)", r.ExitCode)
	case r.Killed:
		return fmt.Sprintf("killed (exit %d)", r.ExitCode)
	}
	return fmt.Sprintf("exit %d", r.ExitCode)
}

const tailLines = 3

func tail(output []byte) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	s := strings.Join(lines, " | ")
	if s == "" {
		return "(empty)"
	}
	return s
}

func DialNet(ctx context.Context, network, addr string) error {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	return conn.Close()
}
