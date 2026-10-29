package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"actionbox/internal/config"
	"actionbox/internal/limits"
)

const (
	home   = "/home/u"
	repo   = "/home/u/projects/p"
	jobDir = "/home/u/.local/state/actionbox/jobs/p/j1"
	proj   = "/home/u/.local/share/actionbox/projects/p"
)

func testPlan(containers bool) Plan {
	return Plan{
		BwrapPath: "/opt/bwrap",
		SelfExe:   "/home/u/bin/actionbox",
		Home:      home,
		Toolchain: config.Toolchain{
			ROPaths: []string{"/home/u/.sdkman/candidates"},
			Env:     map[string]string{"JAVA_HOME": "/home/u/.sdkman/candidates/java/current", "A_VAR": "a"},
			Path:    []string{"/home/u/.sdkman/candidates/maven/current/bin"},
		},
		Repo:          repo,
		JobDir:        jobDir,
		MavenCache:    proj + "/m2",
		NPMCache:      proj + "/npm",
		ContainersDir: proj + "/containers",
		Containers:    containers,
		Binds: &Binds{
			Repo:      Bind{Dest: repo, ReadOnly: true, FD: 3},
			Writes:    []Bind{{Dest: repo + "/target", FD: 4}},
			Protected: []Bind{{Dest: repo + "/.git", ReadOnly: true, FD: 5}, {Dest: repo + "/target/.npmrc", ReadOnly: true, FD: 6}},
		},
		Argv:          []string{"mvn", "-o", "test"},
		Limits:        limits.Limits{Timeout: 20 * time.Minute, MemoryMax: 6 << 30, TasksMax: 4096, CPUQuota: 400},
		HideNixDaemon: true,
	}
}

func TestBwrapArgsNonContainer(t *testing.T) {
	// when
	got := testPlan(false).BwrapArgs()

	// then
	want := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev", "--dev-bind", "/dev/net/tun", "/dev/net/tun", "--proc", "/proc",
		"--tmpfs", "/run", "--tmpfs", "/tmp", "--tmpfs", "/var/tmp", "--tmpfs", "/dev/shm",
		"--tmpfs", "/media", "--tmpfs", "/mnt", "--tmpfs", home,
		"--tmpfs", "/nix/var/nix/daemon-socket",
		"--unshare-net", "--unshare-pid", "--unshare-ipc", "--new-session", "--die-with-parent",
		"--clearenv",
		"--setenv", "PATH", "/home/u/.sdkman/candidates/maven/current/bin:/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", jobDir + "/home",
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "TERM", "dumb",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "MAVEN_OPTS", "-Dmaven.repo.local=" + jobDir + "/m2 -Dmaven.repo.local.tail=" + proj + "/m2",
		"--setenv", "npm_config_cache", proj + "/npm",
		"--setenv", "npm_config_logs_dir", jobDir + "/tmp",
		"--setenv", "npm_config_update_notifier", "false",
		"--setenv", "A_VAR", "a",
		"--setenv", "JAVA_HOME", "/home/u/.sdkman/candidates/java/current",
		"--ro-bind", "/home/u/.sdkman/candidates", "/home/u/.sdkman/candidates",
		"--ro-bind", "/home/u/bin/actionbox", "/home/u/bin/actionbox",
		"--ro-bind-fd", "3", repo,
		"--bind-fd", "4", repo + "/target",
		"--ro-bind-fd", "5", repo + "/.git",
		"--ro-bind-fd", "6", repo + "/target/.npmrc",
		"--ro-bind", proj + "/m2", proj + "/m2",
		"--ro-bind", proj + "/npm", proj + "/npm",
		"--bind", jobDir, jobDir,
		"--chdir", repo,
		"--", "/home/u/bin/actionbox", "job-init", "--meta", jobDir + "/meta.json", "--", "mvn", "-o", "test",
	}
	if !slices.Equal(got, want) {
		t.Errorf("bwrap args:\n got %q\nwant %q", got, want)
	}
}

func TestBwrapArgsContainer(t *testing.T) {
	// when
	got := testPlan(true).BwrapArgs()

	// then
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"--setenv XDG_RUNTIME_DIR " + jobDir + "/xdg",
		"--setenv CONTAINERS_CONF " + jobDir + "/containers.conf",
		"--setenv CONTAINERS_STORAGE_CONF " + jobDir + "/storage.conf",
		"--setenv DOCKER_HOST unix://" + jobDir + "/xdg/podman.sock",
		"--setenv TESTCONTAINERS_RYUK_DISABLED true",
		"--ro-bind " + proj + "/npm " + proj + "/npm " +
			"--overlay-src " + proj + "/containers " +
			"--overlay " + jobDir + "/ovl/upper " + jobDir + "/ovl/work " + proj + "/containers " +
			"--bind " + jobDir + " " + jobDir,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

func TestBwrapArgsWritesBeforeProtected(t *testing.T) {
	got := testPlan(false).BwrapArgs()
	lastWrite := slices.Index(got, "--bind-fd")
	firstProtected := slices.Index(got, repo+"/.git") - 2
	if lastWrite < 0 || firstProtected < lastWrite || got[firstProtected] != "--ro-bind-fd" {
		t.Errorf("writes at %d, protected at %d: %q", lastWrite, firstProtected, got)
	}
}

func TestBwrapArgsSetsOnlyAllowlistedEnv(t *testing.T) {
	// given
	allowed := []string{
		"PATH", "HOME", "LANG", "TERM", "TMPDIR", "MAVEN_OPTS",
		"npm_config_cache", "npm_config_logs_dir", "npm_config_update_notifier", "A_VAR", "JAVA_HOME",
		"XDG_RUNTIME_DIR", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF", "DOCKER_HOST", "TESTCONTAINERS_RYUK_DISABLED",
	}
	for _, containers := range []bool{false, true} {
		// when
		got := testPlan(containers).BwrapArgs()

		// then
		clear := slices.Index(got, "--clearenv")
		var names []string
		for i, a := range got {
			if a == "--setenv" {
				if i < clear {
					t.Errorf("--setenv before --clearenv")
				}
				names = append(names, got[i+1])
			}
		}
		for _, n := range names {
			if !slices.Contains(allowed, n) {
				t.Errorf("containers=%v: unexpected env %s", containers, n)
			}
		}
		if wantN := 11 + map[bool]int{false: 0, true: 5}[containers]; len(names) != wantN {
			t.Errorf("containers=%v: %d vars %v, want %d", containers, len(names), names, wantN)
		}
	}
}

func TestBwrapArgsWithoutNixDaemon(t *testing.T) {
	p := testPlan(false)
	p.HideNixDaemon = false
	if slices.Contains(p.BwrapArgs(), "/nix/var/nix/daemon-socket") {
		t.Error("nix daemon tmpfs without /nix on host")
	}
}

func TestCommandArgv(t *testing.T) {
	// when
	got := testPlan(false).CommandArgv()

	// then
	wantPrefix := []string{
		"systemd-run", "--user", "--scope", "--quiet", "--collect",
		"-p", "MemoryMax=6442450944", "-p", "TasksMax=4096", "-p", "CPUQuota=400%", "--",
		"timeout", "--kill-after=30s", "1200",
		"podman", "unshare", "--", "/opt/bwrap",
	}
	if !slices.Equal(got[:len(wantPrefix)], wantPrefix) {
		t.Errorf("prefix:\n got %q\nwant %q", got[:len(wantPrefix)], wantPrefix)
	}
	if !slices.Equal(got[len(wantPrefix):], testPlan(false).BwrapArgs()) {
		t.Error("bwrap args not appended")
	}
}

func TestLimitFlags(t *testing.T) {
	tests := []struct {
		in   limits.Limits
		want []string
	}{
		{
			in:   limits.Limits{Timeout: time.Minute, MemoryMax: 8 << 30, TasksMax: 4096, CPUQuota: 400},
			want: []string{"-p", "MemoryMax=8589934592", "-p", "TasksMax=4096", "-p", "CPUQuota=400%"},
		},
		{
			in:   limits.Limits{Timeout: time.Minute, MemoryMax: 512 << 20, TasksMax: 1, CPUQuota: 50},
			want: []string{"-p", "MemoryMax=536870912", "-p", "TasksMax=1", "-p", "CPUQuota=50%"},
		},
	}
	for _, tt := range tests {
		if got := limitFlags(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("limitFlags(%+v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTimeoutSeconds(t *testing.T) {
	tests := map[time.Duration]string{
		20 * time.Minute:        "1200",
		1500 * time.Millisecond: "2",
		time.Millisecond:        "1",
	}
	for in, want := range tests {
		if got := timeoutSeconds(in); got != want {
			t.Errorf("timeoutSeconds(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestOuterEnvKeepsOnlySessionVars(t *testing.T) {
	got := outerEnv([]string{
		"PATH=/usr/bin", "HOME=/home/u", "XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "GITHUB_TOKEN=x", "USER=u",
	})
	want := []string{
		"PATH=/usr/bin", "HOME=/home/u", "XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "USER=u",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestContainerConfigFiles(t *testing.T) {
	// when
	storage, containers := StorageConf(proj+"/containers"), ContainersConf()

	// then
	wantStorage := "[storage]\n  driver = \"vfs\"\n  graphroot = \"" + proj + "/containers/store\"\n  runroot = \"" + proj + "/containers/run\"\n"
	if storage != wantStorage {
		t.Errorf("storage.conf:\n%s\nwant\n%s", storage, wantStorage)
	}
	wantContainers := "[containers]\n  log_driver = \"k8s-file\"\n\n[engine]\n  cgroup_manager = \"cgroupfs\"\n  events_logger = \"file\"\n"
	if containers != wantContainers {
		t.Errorf("containers.conf:\n%s\nwant\n%s", containers, wantContainers)
	}
}
