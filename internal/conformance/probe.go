package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	dialTimeout = 3 * time.Second
	tmpPrefix   = "actionbox-conformance-"
)

var (
	persistDirs = []string{"/var/tmp", "/dev/shm"}
	mountDirs   = []string{"/media", "/mnt"}
)

type probeFunc func(ctx context.Context, args []string) Line

var probes = map[string]probeFunc{
	"write-ok":       func(_ context.Context, a []string) Line { return writeOK(oneArg(a)) },
	"write-denied":   func(_ context.Context, a []string) Line { return writeDenied(a) },
	"read-denied":    func(_ context.Context, a []string) Line { return readDenied(oneArg(a)) },
	"connect-denied": func(ctx context.Context, a []string) Line { return connectDenied(ctx, a) },
	"sockets":        func(_ context.Context, a []string) Line { return socketInventory("/", "/proc/net/unix", a) },
	"env":            func(_ context.Context, a []string) Line { return envProbe(os.Environ(), a) },
	"tmp-write":      func(_ context.Context, a []string) Line { return tmpWrite(persistDirs, oneArg(a)) },
	"tmp-check":      func(_ context.Context, a []string) Line { return tmpCheck(persistDirs, mountDirs, oneArg(a)) },
	"mem":            func(_ context.Context, a []string) Line { return memHog(oneArg(a)) },
	"tasks":          func(_ context.Context, a []string) Line { return taskLoop(oneArg(a)) },
	"sleep":          func(_ context.Context, a []string) Line { return sleepProbe(oneArg(a)) },
	"podman":         func(ctx context.Context, a []string) Line { return podmanProbe(ctx, oneArg(a)) },
	"hash-tree":      func(_ context.Context, a []string) Line { return hashTreeProbe(oneArg(a)) },
}

func RunProbe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: actionbox probe <name> [args...]")
		return 2
	}
	p, ok := probes[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown probe %q\n", args[0])
		return 2
	}
	line := p(ctx, args[1:])
	line.Name = args[0]
	fmt.Fprintln(stdout, line)
	if line.Outcome == OutcomeFail {
		return 1
	}
	return 0
}

// bad argv = harness bug, reported as fail
func oneArg(args []string) string {
	if len(args) != 1 {
		return ""
	}
	return args[0]
}

func ok(format string, a ...any) Line {
	return Line{Outcome: OutcomeOK, Detail: fmt.Sprintf(format, a...)}
}
func fail(format string, a ...any) Line {
	return Line{Outcome: OutcomeFail, Detail: fmt.Sprintf(format, a...)}
}
func skip(format string, a ...any) Line {
	return Line{Outcome: OutcomeSkip, Detail: fmt.Sprintf(format, a...)}
}

const probeContent = "actionbox conformance\n"

func writeOK(path string) Line {
	if path == "" {
		return fail("usage: write-ok <path>")
	}
	if err := os.WriteFile(path, []byte(probeContent), 0o644); err != nil {
		return fail("write %s: %v", path, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != probeContent {
		return fail("read back %s: %q, %v", path, got, err)
	}
	return ok("wrote %s", path)
}

// Only EROFS/EACCES/EPERM/EBUSY count as denial: ENOENT would pass on a missing mount.
func writeDenied(paths []string) Line {
	if len(paths) == 0 {
		return fail("usage: write-denied <path>...")
	}
	var allowed, untested, denied []string
	for _, p := range paths {
		fi, statErr := os.Lstat(p)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		switch {
		case err == nil:
			_ = f.Close()
			allowed = append(allowed, p+" (write)")
			continue
		case !isDenial(err):
			untested = append(untested, p+": "+errnoText(err))
			continue
		}
		if statErr == nil && fi.Mode().IsRegular() {
			if err := os.Remove(p); err == nil {
				allowed = append(allowed, p+" (remove)")
				continue
			}
		}
		denied = append(denied, filepath.Base(p)+": "+errnoText(err))
	}
	if len(allowed) > 0 || len(untested) > 0 {
		return fail("writable: [%s], not testable: [%s]", strings.Join(allowed, ", "), strings.Join(untested, ", "))
	}
	return ok("denied %s", strings.Join(denied, "; "))
}

func isDenial(err error) bool {
	for _, e := range []error{syscall.EROFS, syscall.EACCES, syscall.EPERM, syscall.EBUSY} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func errnoText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return err.Error()
}

var SecretPaths = []string{".ssh", ".gnupg", ".m2/settings.xml", ".npmrc", ".docker/config.json"}

func readDenied(home string) Line {
	if home == "" {
		return fail("usage: read-denied <home>")
	}
	var readable, notes []string
	for _, rel := range SecretPaths {
		p := filepath.Join(home, rel)
		r, note := readablePaths(p)
		readable = append(readable, r...)
		notes = append(notes, rel+": "+note)
	}
	if len(readable) > 0 {
		return fail("readable: %s", strings.Join(readable, ", "))
	}
	return ok("%s", strings.Join(notes, "; "))
}

func readablePaths(p string) (readable []string, note string) {
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "absent"
	}
	if err != nil {
		return nil, errnoText(err)
	}
	if !fi.IsDir() {
		if _, err := os.ReadFile(p); err != nil {
			return nil, errnoText(err)
		}
		return []string{p}, "readable"
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, errnoText(err)
	}
	for _, e := range entries {
		child := filepath.Join(p, e.Name())
		if e.IsDir() {
			readable = append(readable, child+"/")
			continue
		}
		if _, err := os.ReadFile(child); err == nil {
			readable = append(readable, child)
		}
	}
	return readable, fmt.Sprintf("%d entries, none readable", len(entries))
}

func connectDenied(ctx context.Context, targets []string) Line {
	if len(targets) == 0 {
		return fail("usage: connect-denied <tcp:addr|unix:addr>...")
	}
	var connected, refused []string
	for _, t := range targets {
		network, addr, found := strings.Cut(t, ":")
		if !found || (network != "tcp" && network != "unix") {
			return fail("bad target %q", t)
		}
		d := net.Dialer{Timeout: dialTimeout}
		conn, err := d.DialContext(ctx, network, addr)
		if err == nil {
			_ = conn.Close()
			connected = append(connected, t)
			continue
		}
		refused = append(refused, t+": "+errnoText(err))
	}
	if len(connected) > 0 {
		return fail("connected: %s", strings.Join(connected, ", "))
	}
	return ok("%s", strings.Join(refused, "; "))
}

func envProbe(environ, args []string) Line {
	if len(args) != 3 {
		return fail("usage: env <allowed,...> <forbidden-name> <canary-value>")
	}
	allowed := strings.Split(args[0], ",")
	forbidden, canary := args[1], args[2]
	var names, extra, leaks []string
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		names = append(names, name)
		if !slices.Contains(allowed, name) {
			extra = append(extra, name)
		}
		if name == forbidden || (canary != "" && strings.Contains(value, canary)) {
			leaks = append(leaks, name)
		}
	}
	var missing []string
	for _, a := range allowed {
		if !slices.Contains(names, a) {
			missing = append(missing, a)
		}
	}
	slices.Sort(extra)
	detail := fmt.Sprintf("%d vars", len(names))
	if len(missing) > 0 {
		detail += ", allowlisted but unset: " + strings.Join(missing, ",")
	}
	if len(leaks) > 0 || len(extra) > 0 {
		return fail("canary in: [%s], not allowlisted: [%s]; %s",
			strings.Join(leaks, ","), strings.Join(extra, ","), detail)
	}
	return ok("only allowlisted names; %s", detail)
}

func tmpWrite(dirs []string, token string) Line {
	if token == "" {
		return fail("usage: tmp-write <token>")
	}
	var notes []string
	for _, d := range dirs {
		p := filepath.Join(d, tmpPrefix+token)
		if err := os.WriteFile(p, []byte(probeContent), 0o644); err != nil {
			notes = append(notes, d+": "+errnoText(err))
			continue
		}
		notes = append(notes, d+": written")
	}
	return ok("%s", strings.Join(notes, "; "))
}

func tmpCheck(dirs, emptyDirs []string, token string) Line {
	if token == "" {
		return fail("usage: tmp-check <token>")
	}
	var problems []string
	for _, d := range dirs {
		p := filepath.Join(d, tmpPrefix+token)
		if _, err := os.Lstat(p); err == nil {
			problems = append(problems, p+" persisted")
		}
	}
	for _, d := range emptyDirs {
		entries, err := os.ReadDir(d)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, d+": "+errnoText(err))
		}
		if len(entries) > 0 {
			problems = append(problems, fmt.Sprintf("%s has %d entries", d, len(entries)))
		}
	}
	if len(problems) > 0 {
		return fail("%s", strings.Join(problems, "; "))
	}
	return ok("%s absent, %s empty", strings.Join(dirs, ","), strings.Join(emptyDirs, ","))
}

const (
	memChunk = 16 << 20
	pageSize = 4096
)

// own cap: without MemoryMax the host must survive it
func memHog(limitArg string) Line {
	limit, err := strconv.ParseUint(limitArg, 10, 64)
	if err != nil || limit == 0 {
		return fail("usage: mem <bytes>")
	}
	var held [][]byte
	for total := uint64(0); total < limit; total += memChunk {
		chunk := make([]byte, memChunk)
		for i := 0; i < len(chunk); i += pageSize {
			chunk[i] = 1
		}
		held = append(held, chunk)
	}
	return fail("allocated %d MiB without being killed (MemoryMax not enforced, or swapped)", len(held)*memChunk>>20)
}

// own cap: a missing TasksMax must not fork-bomb the host
func taskLoop(capArg string) Line {
	limit, err := strconv.Atoi(capArg)
	if err != nil || limit <= 0 {
		return fail("usage: tasks <max children>")
	}
	sleeper, sleepArgs := sleeperCmd()
	var children []*exec.Cmd
	defer func() {
		for _, c := range children {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}()
	for len(children) < limit {
		c := exec.Command(sleeper, sleepArgs...)
		if err := c.Start(); err != nil {
			return ok("bounded: child %d failed: %v", len(children)+1, err)
		}
		children = append(children, c)
	}
	return fail("started %d children without hitting TasksMax", len(children))
}

func sleeperCmd() (string, []string) {
	if p, err := exec.LookPath("sleep"); err == nil {
		return p, []string{"60"}
	}
	self, _ := os.Executable()
	return self, []string{"probe", "sleep", "60"}
}

func sleepProbe(secsArg string) Line {
	secs, err := strconv.Atoi(secsArg)
	if err != nil || secs <= 0 {
		return fail("usage: sleep <seconds>")
	}
	time.Sleep(time.Duration(secs) * time.Second)
	return fail("slept %ds without being stopped", secs)
}
