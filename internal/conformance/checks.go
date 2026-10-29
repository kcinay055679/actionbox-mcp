package conformance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"actionbox/internal/engine"
)

type modeProbe struct {
	containersOnly bool
	run            func(ctx context.Context, m *mode) []Result
}

// modeProbes in report order; P15 last so its store hash covers every container job before it.
var modeProbes = []modeProbe{
	{run: one("P1", checkP1)},
	{run: one("P2", checkP2)},
	{run: one("P3", checkP3)},
	{run: one("P4", checkP4)},
	{run: one("P5", checkP5)},
	{run: one("P6", checkP6)},
	{run: one("P7", checkP7)},
	{run: one("P8", checkP8)},
	{run: one("P9", checkP9)},
	{run: one("P10", checkP10)},
	{run: one("P11", checkP11)},
	{run: checkP12},
	{run: one("P13", func(ctx context.Context, m *mode) (Status, string) {
		return checkCacheDenied(ctx, m, "p13", m.paths.MavenCache(m.project))
	})},
	{run: one("P14", func(ctx context.Context, m *mode) (Status, string) {
		return checkCacheDenied(ctx, m, "p14", m.paths.NPMCache(m.project))
	})},
	{containersOnly: true, run: one("P15", checkP15)},
}

func one(id string, check func(ctx context.Context, m *mode) (Status, string)) func(context.Context, *mode) []Result {
	return func(ctx context.Context, m *mode) []Result {
		s, d := check(ctx, m)
		return []Result{{ID: id, Status: s, Detail: d}}
	}
}

func checkP1(ctx context.Context, m *mode) (Status, string) {
	path := filepath.Join(m.repo, writesDir, "p1-"+m.token)
	j := m.run(ctx, "p1", defaultProbeLimits, nil, "write-ok", path)
	s, d := j.verdict("write-ok")
	if s != Pass {
		return s, d
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != probeContent {
		return Fail, fmt.Sprintf("probe wrote, but host does not see it: %v", err)
	}
	return Pass, d + ", visible on host"
}

func checkP2(ctx context.Context, m *mode) (Status, string) {
	path := filepath.Join(m.repo, "p2-"+m.token)
	j := m.run(ctx, "p2", defaultProbeLimits, nil, "write-denied", path)
	return hostAbsent(j, "write-denied", path)
}

func hostAbsent(j jobRun, probe string, paths ...string) (Status, string) {
	s, d := j.verdict(probe)
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return Fail, fmt.Sprintf("%s exists on host; probe: %s", p, d)
		}
	}
	return s, d
}

var p3Created = []string{".git/hooks/x", ".mvn/x", ".idea/x"}

var p3Existing = []string{".gitattributes", writesDir + "/sub/.bashrc"}

func checkP3(ctx context.Context, m *mode) (Status, string) {
	var args, created []string
	for _, rel := range append(slices.Clone(p3Created), p3Existing...) {
		args = append(args, filepath.Join(m.repo, rel))
	}
	for _, rel := range p3Created {
		created = append(created, filepath.Join(m.repo, rel))
	}
	j := m.run(ctx, "p3", defaultProbeLimits, nil, append([]string{"write-denied"}, args...)...)
	s, d := hostAbsent(j, "write-denied", created...)
	if s != Pass {
		return s, d
	}
	for _, rel := range p3Existing {
		got, err := os.ReadFile(filepath.Join(m.repo, rel))
		if err != nil || string(got) != fixtureFiles[rel] {
			return Fail, fmt.Sprintf("%s changed on host: %v; probe: %s", rel, err, d)
		}
	}
	return Pass, d
}

func checkP4(ctx context.Context, m *mode) (Status, string) {
	j := m.run(ctx, "p4", defaultProbeLimits, nil, "read-denied", m.h.host.Home)
	s, d := j.verdict("read-denied")
	if s != Pass {
		return s, d
	}
	var onHost []string
	for _, rel := range SecretPaths {
		if _, err := os.Lstat(filepath.Join(m.h.host.Home, rel)); err == nil {
			onHost = append(onHost, rel)
		}
	}
	if len(onHost) == 0 {
		return Warn, "vacuous, none of the paths exist on the host; probe: " + d
	}
	return Pass, fmt.Sprintf("host has [%s]; job: %s", strings.Join(onHost, ","), d)
}

func checkP5(ctx context.Context, m *mode) (Status, string) {
	j := m.run(ctx, "p5", defaultProbeLimits, nil, "connect-denied", "tcp:"+externalAddr)
	s, d := j.verdict("connect-denied")
	if s != Pass || m.h.host.Dial == nil {
		return s, d
	}
	if err := m.h.host.Dial(ctx, "tcp", externalAddr); err != nil {
		return Warn, fmt.Sprintf("vacuous, host can't reach %s either (%v); job: %s", externalAddr, err, d)
	}
	return Pass, d + "; host reaches it"
}

// abstract unix sockets are netns-scoped like loopback, so both must be unreachable
func checkP6(ctx context.Context, m *mode) (Status, string) {
	var accepted atomic.Int32
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Fail, "harness listen tcp: " + err.Error()
	}
	defer tcp.Close()
	abstract := "@" + tmpPrefix + m.token
	unix, err := net.Listen("unix", abstract)
	if err != nil {
		return Fail, "harness listen unix: " + err.Error()
	}
	defer unix.Close()
	for _, ln := range []net.Listener{tcp, unix} {
		go acceptCounting(ln, &accepted)
	}
	j := m.run(ctx, "p6", defaultProbeLimits, nil, "connect-denied",
		"tcp:"+tcp.Addr().String(), "unix:"+abstract)
	s, d := j.verdict("connect-denied")
	if n := accepted.Load(); n > 0 {
		return Fail, fmt.Sprintf("harness listeners accepted %d connection(s); probe: %s", n, d)
	}
	return s, d
}

func acceptCounting(ln net.Listener, n *atomic.Int32) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		n.Add(1)
		_ = conn.Close()
	}
}

const socketWalkTimeout = 5 * time.Minute

func checkP7(ctx context.Context, m *mode) (Status, string) {
	args := []string{"sockets"}
	if m.containers {
		args = append(args, engine.Plan{JobDir: m.jobDir("p7")}.PodmanSocket())
	}
	lim := defaultProbeLimits
	lim.Timeout = socketWalkTimeout
	j := m.run(ctx, "p7", lim, nil, args...)
	return j.verdict("sockets")
}

func checkP8(ctx context.Context, m *mode) (Status, string) {
	repo := m.repo + "-p8"
	if err := writeFixture(repo, m.victim); err != nil {
		return Fail, "fixture: " + err.Error()
	}
	if err := os.RemoveAll(filepath.Join(repo, writesDir)); err != nil {
		return Fail, "fixture: " + err.Error()
	}
	if err := os.Symlink(m.victim, filepath.Join(repo, writesDir)); err != nil {
		return Fail, "fixture: " + err.Error()
	}
	before := listNames(m.victim)
	j := m.runIn(ctx, "p8", repo, defaultProbeLimits, nil, "write-ok", filepath.Join(repo, writesDir, "p8-"+m.token))
	if after := listNames(m.victim); !slices.Equal(before, after) {
		return Fail, fmt.Sprintf("victim dir changed: %v -> %v", before, after)
	}
	if j.err == nil {
		return Fail, fmt.Sprintf("job started (%s); output: %s", describe(j.result), tail(j.output))
	}
	if !strings.Contains(j.err.Error(), "symlink") {
		return Fail, "rejected for another reason: " + j.err.Error()
	}
	return Pass, "rejected before start: " + j.err.Error()
}

func checkP9(ctx context.Context, m *mode) (Status, string) {
	target := filepath.Join(m.repo, writesDir)
	moved := target + ".p9-orig"
	marker := "p9-" + m.token
	var hookErr error
	swapped := false
	swap := func() {
		if hookErr = os.Rename(target, moved); hookErr != nil {
			return
		}
		swapped = true
		hookErr = os.Symlink(m.victim, target)
	}
	before := listNames(m.victim)
	j := m.run(ctx, "p9", defaultProbeLimits, swap, "write-ok", filepath.Join(target, marker))
	inOriginal := exists(filepath.Join(moved, marker))
	if swapped {
		if err := restoreSwap(target, moved); err != nil {
			return Fail, "restore writes dir: " + err.Error()
		}
	}
	return judgeP9(j, hookErr, swapped, before, listNames(m.victim), inOriginal)
}

func restoreSwap(target, moved string) error {
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	return os.Rename(moved, target)
}

func judgeP9(j jobRun, hookErr error, swapped bool, before, after []string, inOriginal bool) (Status, string) {
	if !slices.Equal(before, after) {
		return Fail, fmt.Sprintf("victim dir written: %v -> %v", before, after)
	}
	if !swapped || hookErr != nil {
		return Fail, fmt.Sprintf("harness could not swap the writes dir: %v", hookErr)
	}
	if j.err != nil {
		return Fail, "job not started: " + j.err.Error()
	}
	line, found := ParseLine(j.output, "write-ok")
	switch {
	case found && line.Outcome == OutcomeOK && inOriginal:
		return Pass, "original dir mounted from fd, victim untouched"
	case found && line.Outcome == OutcomeOK:
		return Fail, "probe wrote, but neither into the original dir nor the victim: " + line.Detail
	case found:
		return Warn, "victim untouched, but writes dir not writable: " + line.Detail
	case strings.Contains(string(j.output), "Race condition"):
		return Pass, "bwrap refused the swapped mount point: " + tail(j.output)
	// 125 alone is any job-init failure; only RecheckMounts says "mount point"
	case j.result.ExitCode == 125 && strings.Contains(string(j.output), "mount point"):
		return Pass, "job-init aborted on mount mismatch: " + tail(j.output)
	}
	return Fail, fmt.Sprintf("no probe result, job %s; output: %s", describe(j.result), tail(j.output))
}

func listNames(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func checkP10(ctx context.Context, m *mode) (Status, string) {
	allowed := m.allowedEnv("p10")
	j := m.run(ctx, "p10", defaultProbeLimits, nil, "env", strings.Join(allowed, ","), CanaryEnv, m.h.canary)
	return j.verdict("env")
}

// allowedEnv reads the engine's own --setenv list, so P10 follows the engine's allowlist.
func (m *mode) allowedEnv(jobID string) []string {
	plan := engine.Plan{
		BwrapPath:     m.h.host.Config.BwrapPath,
		SelfExe:       m.h.host.SelfExe,
		Home:          m.h.host.Home,
		Toolchain:     m.h.host.Config.Toolchain,
		Repo:          m.repo,
		JobDir:        m.jobDir(jobID),
		MavenCache:    m.paths.MavenCache(m.project),
		NPMCache:      m.paths.NPMCache(m.project),
		ContainersDir: m.containersDir(),
		Containers:    m.containers,
		Binds:         &engine.Binds{},
	}
	return SetenvNames(plan.BwrapArgs())
}

func SetenvNames(bwrapArgs []string) []string {
	var names []string
	for i := 0; i+1 < len(bwrapArgs); i++ {
		if bwrapArgs[i] == "--" {
			break
		}
		if bwrapArgs[i] == "--setenv" {
			names = append(names, bwrapArgs[i+1])
			i += 2
		}
	}
	return names
}

func checkP11(ctx context.Context, m *mode) (Status, string) {
	w := m.run(ctx, "p11-write", defaultProbeLimits, nil, "tmp-write", m.token)
	if s, d := w.verdict("tmp-write"); s != Pass {
		return s, "write job: " + d
	}
	for _, d := range persistDirs {
		if p := filepath.Join(d, tmpPrefix+m.token); exists(p) {
			return Fail, p + " exists on host"
		}
	}
	c := m.run(ctx, "p11-check", defaultProbeLimits, nil, "tmp-check", m.token)
	s, d := c.verdict("tmp-check")
	_, wd := w.verdict("tmp-write")
	return s, d + "; first job: " + wd
}

const (
	p12MemoryMax = 128 << 20
	p12HogBytes  = 1 << 30
	p12TasksMax  = 128
	p12MaxSpawn  = 300
	p12Timeout   = 3 * time.Second
	p12SleepSecs = 30
)

func checkP12(ctx context.Context, m *mode) []Result {
	mem := defaultProbeLimits
	mem.MemoryMax = p12MemoryMax
	tasks := defaultProbeLimits
	tasks.TasksMax = p12TasksMax
	timeout := defaultProbeLimits
	timeout.Timeout = p12Timeout

	memJob := m.run(ctx, "p12-mem", mem, nil, "mem", strconv.Itoa(p12HogBytes))
	tasksJob := m.run(ctx, "p12-tasks", tasks, nil, "tasks", strconv.Itoa(p12MaxSpawn))
	sleepJob := m.run(ctx, "p12-timeout", timeout, nil, "sleep", strconv.Itoa(p12SleepSecs))
	return []Result{
		judged("P12-mem", judgeMem, memJob),
		judged("P12-tasks", judgeTasks, tasksJob),
		judged("P12-timeout", judgeTimeout, sleepJob),
	}
}

func judged(id string, judge func(jobRun) (Status, string), j jobRun) Result {
	s, d := judge(j)
	return Result{ID: id, Status: s, Detail: d}
}

func judgeMem(j jobRun) (Status, string) {
	if j.err != nil {
		return Fail, "job not started: " + j.err.Error()
	}
	if j.result.Killed && !j.result.TimedOut {
		return Pass, fmt.Sprintf("killed at MemoryMax=%dM (%s)", p12MemoryMax>>20, describe(j.result))
	}
	s, d := j.verdict("mem")
	if s == Pass {
		s = Fail
	}
	return s, fmt.Sprintf("not killed (%s): %s", describe(j.result), d)
}

func judgeTasks(j jobRun) (Status, string) {
	if j.err != nil {
		return Fail, "job not started: " + j.err.Error()
	}
	line, found := ParseLine(j.output, "tasks")
	switch {
	case found && line.Outcome == OutcomeOK:
		return Pass, fmt.Sprintf("TasksMax=%d: %s", p12TasksMax, line.Detail)
	case found:
		return Fail, line.Detail
	case j.result.Killed:
		return Pass, "killed: " + describe(j.result)
	}
	return Warn, fmt.Sprintf("no probe result, job %s, likely the Go runtime hit TasksMax; output: %s",
		describe(j.result), tail(j.output))
}

func judgeTimeout(j jobRun) (Status, string) {
	if j.err != nil {
		return Fail, "job not started: " + j.err.Error()
	}
	if j.result.TimedOut {
		return Pass, fmt.Sprintf("timed out after %v (%s)", p12Timeout, describe(j.result))
	}
	_, d := j.verdict("sleep")
	return Fail, fmt.Sprintf("not timed out (%s): %s", describe(j.result), d)
}

func checkCacheDenied(ctx context.Context, m *mode, id, cache string) (Status, string) {
	path := filepath.Join(cache, id+"-"+m.token)
	j := m.run(ctx, id, defaultProbeLimits, nil, "write-denied", path)
	return hostAbsent(j, "write-denied", path)
}

func checkP15(ctx context.Context, m *mode) (Status, string) {
	podman := m.h.host.Podman
	if podman == nil {
		return Skip, "podman not on PATH"
	}
	if m.storeBefore == "" {
		return Fail, "no baseline: " + m.setupNote
	}
	seed := m.seedImage
	if seed == "" {
		seed = "-"
	}
	j := m.run(ctx, "p15", defaultProbeLimits, nil, "podman", seed)
	after, err := podman.HashTree(ctx, m.containersDir())
	if err != nil {
		return Fail, "hash store after: " + err.Error()
	}
	return judgeP15(j, m.storeBefore, after, m.setupNote)
}

func judgeP15(j jobRun, before, after, note string) (Status, string) {
	if before != after {
		return Fail, fmt.Sprintf("project container store changed: %s -> %s", short(before), short(after))
	}
	s, d := j.verdict("podman")
	if note != "" {
		d += "; " + note
	}
	switch s {
	case Pass:
		return Pass, "store byte-identical; " + d
	case Fail:
		if _, found := ParseLine(j.output, "podman"); found {
			return Warn, "store byte-identical, but not every write ran: " + d
		}
	}
	return s, d
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

var errNoLine = errors.New("no probe line")

func ParseHashLine(output []byte) (string, error) {
	line, found := ParseLine(output, "hash-tree")
	if !found {
		return "", errNoLine
	}
	if line.Outcome != OutcomeOK {
		return "", errors.New(line.Detail)
	}
	sum, _, _ := strings.Cut(line.Detail, " ")
	return sum, nil
}
