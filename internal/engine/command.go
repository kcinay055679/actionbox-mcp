package engine

import (
	"bytes"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"actionbox/internal/config"
	"actionbox/internal/limits"
)

const (
	systemPath     = "/usr/local/bin:/usr/bin:/bin"
	nixDaemonDir   = "/nix/var/nix/daemon-socket"
	killAfter      = "30s"
	podmanSockName = "podman.sock"
)

// Plan is everything needed to build the engine command of one job (spec §8.7 step 4, §8.8).
type Plan struct {
	BwrapPath string
	SelfExe   string
	// Home is the owner's home, hidden behind a tmpfs.
	Home          string
	Toolchain     config.Toolchain
	Repo          string
	JobDir        string
	MavenCache    string
	NPMCache      string
	ContainersDir string
	Containers    bool
	Binds         *Binds
	Argv          []string
	Limits        limits.Limits
	// HideNixDaemon: --tmpfs on a path missing from the ro root fails, so only set when it exists.
	HideNixDaemon bool
}

func (p Plan) RuntimeDir() string     { return p.JobDir + "/xdg" }
func (p Plan) PodmanSocket() string   { return p.RuntimeDir() + "/" + podmanSockName }
func (p Plan) ContainersConf() string { return p.JobDir + "/containers.conf" }
func (p Plan) StorageConf() string    { return p.JobDir + "/storage.conf" }
func (p Plan) OverlayUpper() string   { return p.JobDir + "/ovl/upper" }
func (p Plan) OverlayWork() string    { return p.JobDir + "/ovl/work" }

// CommandArgv is the full host command: systemd-run scope, timeout, podman unshare, bwrap.
func (p Plan) CommandArgv() []string {
	argv := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect"}
	argv = append(argv, limitFlags(p.Limits)...)
	argv = append(argv, "--",
		"timeout", "--kill-after="+killAfter, timeoutSeconds(p.Limits.Timeout),
		"podman", "unshare", "--", p.BwrapPath)
	return append(argv, p.BwrapArgs()...)
}

func (p Plan) BwrapArgs() []string {
	a := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev", "--dev-bind", "/dev/net/tun", "/dev/net/tun", "--proc", "/proc",
	}
	for _, dir := range []string{"/run", "/tmp", "/var/tmp", "/dev/shm", "/media", "/mnt", p.Home} {
		a = append(a, "--tmpfs", dir)
	}
	if p.HideNixDaemon {
		a = append(a, "--tmpfs", nixDaemonDir)
	}
	a = append(a, "--unshare-net", "--unshare-pid", "--unshare-ipc", "--new-session", "--die-with-parent")

	// bwrap applies --clearenv/--setenv while parsing, so --clearenv must come first
	a = append(a, "--clearenv")
	for _, kv := range p.env() {
		a = append(a, "--setenv", kv[0], kv[1])
	}

	for _, t := range p.Toolchain.ROPaths {
		a = append(a, "--ro-bind", t, t)
	}
	// job-init runs from this binary, which may live under the hidden $HOME
	a = append(a, "--ro-bind", p.SelfExe, p.SelfExe)

	a = append(a, "--ro-bind-fd", strconv.Itoa(p.Binds.Repo.FD), p.Binds.Repo.Dest)
	for _, w := range p.Binds.Writes {
		a = append(a, "--bind-fd", strconv.Itoa(w.FD), w.Dest)
	}
	// after writes: later mounts shadow earlier ones
	for _, pr := range p.Binds.Protected {
		a = append(a, "--ro-bind-fd", strconv.Itoa(pr.FD), pr.Dest)
	}

	a = append(a, "--ro-bind", p.MavenCache, p.MavenCache, "--ro-bind", p.NPMCache, p.NPMCache)
	if p.Containers {
		a = append(a,
			"--overlay-src", p.ContainersDir,
			"--overlay", p.OverlayUpper(), p.OverlayWork(), p.ContainersDir)
	}
	a = append(a, "--bind", p.JobDir, p.JobDir, "--chdir", p.Repo)

	a = append(a, "--", p.SelfExe, "job-init", "--meta", MetaPath(p.JobDir), "--")
	return append(a, p.Argv...)
}

// env is the §8.7 step 5 allowlist, in table order.
func (p Plan) env() [][2]string {
	// toolchain first: [toolchain].path exists to pick tool versions over system ones
	path := strings.Join(append(slices.Clone(p.Toolchain.Path), systemPath), ":")
	env := [][2]string{
		{"PATH", path},
		{"HOME", p.JobDir + "/home"},
		{"LANG", "C.UTF-8"},
		{"TERM", "dumb"},
		{"TMPDIR", "/tmp"},
		{"MAVEN_OPTS", "-Dmaven.repo.local=" + p.JobDir + "/m2 -Dmaven.repo.local.tail=" + p.MavenCache},
		{"npm_config_cache", p.NPMCache},
		{"npm_config_logs_dir", p.JobDir + "/tmp"},
		{"npm_config_update_notifier", "false"},
	}
	for _, k := range slices.Sorted(maps.Keys(p.Toolchain.Env)) {
		env = append(env, [2]string{k, p.Toolchain.Env[k]})
	}
	if p.Containers {
		env = append(env,
			[2]string{"XDG_RUNTIME_DIR", p.RuntimeDir()},
			[2]string{"CONTAINERS_CONF", p.ContainersConf()},
			[2]string{"CONTAINERS_STORAGE_CONF", p.StorageConf()},
			[2]string{"DOCKER_HOST", "unix://" + p.PodmanSocket()},
			[2]string{"TESTCONTAINERS_RYUK_DISABLED", "true"},
		)
	}
	return env
}

func limitFlags(l limits.Limits) []string {
	return []string{
		"-p", "MemoryMax=" + strconv.FormatUint(l.MemoryMax, 10),
		"-p", "TasksMax=" + strconv.Itoa(l.TasksMax),
		"-p", "CPUQuota=" + strconv.Itoa(l.CPUQuota) + "%",
	}
}

// rounds up: a sub-second remainder must not shorten the owner's timeout
func timeoutSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(math.Ceil(d.Seconds())), 10)
}

// systemd-run needs the user bus, podman unshare its rootless session; the job env is set by bwrap.
var outerEnvNames = []string{"PATH", "HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}

func outerEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(outerEnvNames, name) {
			out = append(out, kv)
		}
	}
	return out
}

type storageConf struct {
	Storage struct {
		Driver    string `toml:"driver"`
		GraphRoot string `toml:"graphroot"`
		RunRoot   string `toml:"runroot"`
	} `toml:"storage"`
}

type containersConf struct {
	Containers struct {
		LogDriver string `toml:"log_driver"`
	} `toml:"containers"`
	Engine struct {
		CgroupManager string `toml:"cgroup_manager"`
		EventsLogger  string `toml:"events_logger"`
	} `toml:"engine"`
}

// StorageConf must stay identical for host fetches and jobs (spec §8.8, E14d).
func StorageConf(containersDir string) string {
	var c storageConf
	c.Storage.Driver = "vfs"
	c.Storage.GraphRoot = containersDir + "/store"
	c.Storage.RunRoot = containersDir + "/run"
	return encodeTOML(c)
}

func ContainersConf() string {
	var c containersConf
	c.Containers.LogDriver = "k8s-file"
	c.Engine.CgroupManager = "cgroupfs"
	c.Engine.EventsLogger = "file"
	return encodeTOML(c)
}

func encodeTOML(v any) string {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(v); err != nil {
		panic(err) // static struct of strings
	}
	return buf.String()
}
