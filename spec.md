# Agent Action Broker: Design & Implementation Spec

- **Status:** Requirements complete; open items in §11
- **Date:** 2026-10-28
- **Owner:** Yanick Minder
- **Origin:** Requirements session (grill-me method, Q1–Q21)

**Evidence tags used throughout:**

- **[obs-H]**: observed on the target host
- **[obs-C]**: observed in a test container (Ubuntu 24.04, kernel 6.18, Podman 4.9.3, bwrap 0.9.0, git 2.43.0, Fence 0.1.67)
- **[doc]**: documented by the named source
- **[inf]**: inference, not tested
- **⚠**: unverified

---

## 1. Problem

Claude Code runs inside **Fence** (a bubblewrap-based sandbox) with a deliberately defensive policy: no Docker, no Maven, no network beyond what's allowed. Some agent tasks still need those tools (run tests, build, lint, Testcontainers).

Goal: a **fixed, per-project set of actions** that the agent can invoke and that run on the host **without weakening the sandbox**. The initial idea was "something like mise tasks, exposed via MCP". This document is the result of refining that idea.

## 2. Goals / Non-goals

**Goals**
- The agent invokes only actions you defined and signed, with typed parameters.
- Actions run isolated: no secrets, no network, write access only where declared.
- Container-based tests (Testcontainers + Postgres) work.
- Minimal host changes (one package: `podman`).

**Non-goals**
- Protecting against malicious third-party dependencies (threat C, §3).
- Making agent-written code safe to run **outside** the sandbox. That remains your review boundary.
- Multi-user or team-shared deployment.

## 3. Threat model

| ID | Threat | In scope |
|---|---|---|
| A | Accidents: destructive commands, runaway processes | Yes |
| B | Prompt injection: the agent is steered into exfiltration or persistence | Yes |
| C | Compromised dependency executing during a build | **No** |

**Key consequence:** an allowlisted build action is code execution by design. `mvn test` runs test code the agent can write. Under B, every action must therefore run isolated from secrets, network, and host persistence paths. **The allowlist selects sandbox profiles; it is not itself the security boundary.**

## 4. Target environment

| Item | Value | Evidence |
|---|---|---|
| OS | Pop!_OS (Ubuntu 22.04 base), Sway/Regolith | [obs-H] |
| cgroups | v2; user manager delegates `cpuset cpu io memory pids` | [obs-H] |
| Podman | 3.4.4, runtime crun 0.17 | [obs-H] |
| bubblewrap | 0.6.1 (no overlay support; that needs ≥ 0.11) | [obs-H], [doc] bwrap NEWS |
| util-linux | 2.37.2 (no `unshare --map-auto`) | [obs-H] |
| `/dev/net/tun` | `crw-rw-rw-` | [obs-H] |
| subuid | `100000:65536` (inferred from the observed uid_map) | [obs-H] |
| Docker | rootless, used for normal work; untouched by this design | [stated] |
| Signing | GnuPG commit signing | [stated] |
| Fence (host) | version unknown ⚠, run `fence --version` | — |
| Nix | installed (`/nix` present) | [obs-H] |

## 5. Decision log

| # | Question | Decision | Why | Rejected |
|---|---|---|---|---|
| Q1 | Threat model | A + B, not C | User choice | — |
| Q2 | Network and credentials for actions | Run actions offline, no credentials | Under B, a test can read anything the build can | Live network in tests |
| Q3 | Docker usage | Testcontainers + image build only | Docker socket = engine privileges; ad-hoc `run` unneeded | `compose up`, ad-hoc `run` |
| Q4 | Where definitions live | In repo, trusted only if the last commit touching the file is signed by the pinned key | Travels with the repo, no extra infrastructure | Out-of-repo file; Fence write-deny |
| Q5 | Signing capability | Agent commits but cannot sign; GPG | Agent sockets unreachable (verified in Q17) | — |
| Q6 | Where the runner lives | Broker outside Fence, socket into the jail | stdio MCP servers inherit the jail; nested bwrap fails | MCP server inside Fence |
| Q7/Q8 | Network isolation vs. Testcontainers | Engine runs **inside** the action sandbox (option C) | `--unshare-net` hides host-loopback ports; an in-sandbox engine shares the sandbox loopback | Host firewall (nftables), pasta forwarding, fixed ports |
| Q9 | Container engine | Podman (rootless), not Docker | Daemonless: inherits the sandbox's namespaces | Your rootless dockerd (sees real `$HOME`, has egress) |
| Q10 | Argument freedom | Typed params validated against the signed schema; argv arrays only | Covers "run one test" without exposing the CLI surface | Fixed-only; free-form |
| Q11 | Fetch approval | Out-of-band CLI on host | Claude Code's own settings may be agent-writable | Claude Code permission prompt; desktop dialog |
| Q12 | Caches | Dedicated per-project caches, never read outside the sandbox | A poisoned shared `~/.m2` would execute on your next host build | Shared personal caches; read-only overlay (needs bwrap ≥ 0.11) |
| Q13/Q14 | Repo write scope and engine | Repo read-only, per-action writable directories; Fence engine for non-container actions, bwrap engine for container actions; cross-engine conformance test | Allowlist fails closed; Podman doesn't run inside Fence | Repo read-write; deny-list only |
| Q15 | MCP surface | Generic tools, job IDs, capped output, one job per project | Long runs, stable permission rules | One MCP tool per action |
| Q16 | Fetching | **User-only, coordinate-based fetch on host**; sandboxes never get network | Never executes repo code on host; no credentials in sandboxes | Sandboxed fetch with network; signed-tree project fetch; "unchanged files" gate |
| Q17 | Socket exposure in agent's Fence | No change needed | Inventory found no reachable sockets [obs-H] | `denyRead` on `/run/user/1000` |
| Q18 | Broker lifecycle | Foreground `serve` now; later started by the Fence launcher | Visible kill switch, no persistent host config | systemd user socket activation |
| Q19 | Definitions format | Dedicated `.agent-actions.toml` | Needs fields mise lacks; mise tasks are shell strings | Reuse `mise.toml` tasks |
| Q20 | Resource limits | Timeout + `systemd-run --user --scope` limits per job | Covers cgroup-less containers inside the scope | Timeout only |

## 6. What we tried

| # | Experiment | Env | Result |
|---|---|---|---|
| E1 | Unix socket through `bwrap --unshare-net` | [obs-C] | Works |
| E2 | Host-loopback TCP through `--unshare-net` | [obs-C] | **Refused**; the namespace has only `lo` (led to Q8) |
| E3 | `core.fsmonitor` in `.git/config` | [obs-C] | Executed 6× during `git diff` + `status`; 0× with `-c core.fsmonitor=false` |
| E4 | Signature check: signed+clean / uncommitted edit / unsigned commit / foreign-key commit | [obs-C] | PASS / FAIL / FAIL (`N`) / FAIL (`E`) |
| E5 | Rootless Podman inside bwrap, default config | [obs-C] | **Fails**: `newuidmap` blocked (`NoNewPrivs=1`) |
| E6 | Single-UID mode (`/etc/subuid` masked, vfs, `ignore_chown_errors`) | [obs-C] | Container runs; published port reachable; egress blocked; bind-mounted `~/.ssh` empty; Docker-compatible API socket works |
| E7 | Single-UID mode, `--user 999` | [obs-C] | **Fails**: `cannot setuid to unmapped uid 999`, which breaks the official Postgres image |
| E8 | `unshare --map-auto` + bwrap | [obs-C] | 65,537 UIDs mapped with `NoNewPrivs=1`; container start blocked by the test env's hybrid cgroups (inconclusive) |
| E9 | **`podman unshare` + bwrap** | [obs-C] | Full mapping; `--user 999` works; port reachable; egress blocked |
| E10 | Second Fence for actions | [obs-C] | `.git/hooks` write blocked by default; declared dir writable; egress blocked. Podman inside Fence **fails** in all variants (newuidmap; setgroups; read-only `/run`) |
| E11 | Fence `allowUnixSockets` on Linux | [obs-C] | **No-op**: all sockets reachable with or without it. Only `denyRead` hides a socket; an `allowRead` carve-out inside a denied dir doesn't restore it |
| E12 | `$XDG_RUNTIME_DIR` sockets from the agent's Fence | [obs-H] | Fence rewrites the variable to an empty dir; `/run/user/1000` doesn't exist inside |
| E13 | Full socket inventory inside the agent's Fence + positive control | [obs-H] | No reachable sockets; a control socket under `~` is reachable |
| E14 | `pg-spike.sh` iterations | [obs-H] | (a) pasted into zsh: no word-splitting; (b) no `--map-auto`; (c) `/run/user/1000` hidden → private `XDG_RUNTIME_DIR`; (d) runroot mismatch → shared runroot; (e) **PASS**: Postgres ready, `PORT-REACHABLE`, uid_map `0 1000 1 \| 1 100000 65536`, `ifaces: lo` |

## 7. Architecture

```text
 Host (your UID, outside any sandbox)
 ┌──────────────────────────────────────────────────────────────────┐
 │  you ── <broker> fetch / run (user-only) ─┐                      │
 │                                           ▼                      │
 │  <broker> serve ◄── ~/.local/state/<broker>/broker.sock          │
 │     │ verify signed .agent-actions.toml (isolated GNUPGHOME)     │
 │     │ per-project lock, systemd-run --user --scope (limits)      │
 │     ├─► Fence engine:      fence --settings <generated> -- argv  │
 │     └─► Container engine:  podman unshare -- bwrap ... -- argv   │
 │                              └─ podman system service (job-local)│
 │                                                                  │
 │  Agent's Fence (unchanged policy)                                │
 │   └─ Claude Code ─stdio─► <broker> mcp (shim) ─► broker.sock     │
 └──────────────────────────────────────────────────────────────────┘
```

## 8. Specification

### 8.1 Components

| Component | Runs where | Purpose |
|---|---|---|
| `<broker> serve` | Host, foreground terminal | Accepts socket requests, verifies trust, runs jobs, shows events |
| `<broker> mcp` | Inside the agent's Fence (spawned by Claude Code) | stdio MCP server; forwards to `broker.sock` |
| `<broker> fetch <id>` | Host | Shows, validates, and executes a pending fetch request |
| `<broker> run <action>` | Host | Runs a user-only (or any) action |
| `<broker> verify [path]` | Host | Prints trust status of a project |
| `<broker> conformance [path]` | Host | Runs §9 probes against both engines |

Language suggestion (not decided): Go. A single static binary, and Fence is usable as a Go package [doc] (pkg.go.dev/github.com/fencesandbox/fence).

### 8.2 Host paths

The agent's Fence must **not** have write access to any of these.

| Path | Content |
|---|---|
| `~/.config/<broker>/config.toml` | Pinned GPG fingerprint, project allowlist, toolchain paths, default limits |
| `~/.local/share/<broker>/gnupg/` | Verifier keyring: your **public** key only, ownertrust ultimate |
| `~/.local/share/<broker>/projects/<id>/m2/` | Maven cache |
| `~/.local/share/<broker>/projects/<id>/npm/` | npm cache |
| `~/.local/share/<broker>/projects/<id>/containers/{store,run}/` | Podman graphroot + runroot (always used as a pair) |
| `~/.local/state/<broker>/broker.sock` | Socket (agent needs connect only) |
| `~/.local/state/<broker>/trust/<id>.json` | Last accepted definitions commit (rollback guard) |
| `~/.local/state/<broker>/jobs/<id>/<job>/` | `log.txt`, `meta.json` (agent needs read) |

`<id>` = first 12 hex characters of sha256(realpath(repo)).

**DECIDE-1 (project allowlist):** I propose the broker serves only repos listed in `config.toml`, and refuses any other path the shim sends. This prevents the agent from pointing the broker at an arbitrary directory.

### 8.3 Definitions file: `.agent-actions.toml`

```toml
schema_version = 1

[actions.test]
description = "Run backend tests, optionally one class"
invoker     = "agent"                  # agent | user
kind        = "run"                    # run | coordinate-fetch
engine      = "containers"             # fence | containers
argv        = ["mvn", "-o", "-B", "test", "-Dtest={test_class}"]
writes      = ["target"]
timeout     = "20m"
memory_max  = "6G"
[actions.test.params.test_class]
pattern     = '[A-Za-z0-9_.#*]+'       # broker anchors as ^...$
optional    = true
description = "Surefire -Dtest filter"

[actions.lint]
description = "Frontend lint"
invoker     = "agent"
engine      = "fence"
argv        = ["npx", "--offline", "eslint", "src"]
writes      = []

[actions.npm-ci]
description = "Install node_modules from cache"
invoker     = "agent"
engine      = "fence"
argv        = ["npm", "ci", "--offline", "--ignore-scripts"]
writes      = ["node_modules"]

[actions.format]
description = "Apply formatter to sources"
invoker     = "agent"
engine      = "fence"
argv        = ["mvn", "-o", "-B", "spotless:apply"]
writes      = ["src", "target"]

[actions.fetch]
description = "Fetch coordinates requested by the agent"
invoker     = "user"
kind        = "coordinate-fetch"
```

**Rules (the broker rejects the whole file on any violation):**

- `argv`: an array of strings. A `{param}` placeholder may appear inside an element. A substituted value is never split. If an optional param is omitted, every element containing its placeholder is **dropped**.
- Param values: a full match of `^(pattern)$`, ≤ 256 bytes, no NUL. Unknown params are rejected.
- `writes`: repo-relative **directories**. No absolute paths, no `..`, no glob, no protected path (below), and not an ancestor or descendant of a protected path.
- **Symlinks (DECIDE-2):** the broker `lstat`s every component of each `writes` path, of protected paths, and of the repo root. It refuses if any is a symlink. Otherwise the agent could turn `target` into a symlink and redirect the writable bind mount [inf]. Missing directories are created by the broker before binding.
- Limits: optional per action, clamped to the caps in `config.toml`.
- **Protected paths** (never writable, re-bound read-only in the container engine): `.git`, `.agent-actions.toml`, `.idea`, `.vscode`, `.envrc`, `.mcp.json`, `.claude`, `mvnw`, `mvnw.cmd`, `.mvn`, `.npmrc`, plus Fence's built-in dangerous-file list (§11, O1).

### 8.4 Trust verification

Runs on every request, before anything executes.

```bash
export GNUPGHOME="$HOME/.local/share/<broker>/gnupg"
export GIT_NO_REPLACE_OBJECTS=1          # DECIDE-3, see below
G=(git -C "$REPO" -c core.fsmonitor=false -c core.hooksPath=/dev/null
       -c gpg.program=gpg -c gpg.format=openpgp)

[ -e "$REPO/.git/info/grafts" ] && fail "grafts present"
C=$("${G[@]}" log -1 --format=%H -- .agent-actions.toml) || fail
read -r ST FPR PFPR < <("${G[@]}" log -1 --format='%G? %GF %GP' "$C")
[ "$ST" = G ] || fail "signature status $ST"
[ "$PFPR" = "$PIN" ] || [ "$FPR" = "$PIN" ] || fail "foreign key"
"${G[@]}" diff --quiet "$C" -- .agent-actions.toml || fail "uncommitted edits"
if [ -n "$LAST" ]; then
  "${G[@]}" merge-base --is-ancestor "$LAST" "$C" || fail "rollback"  # DECIDE-4
fi
CONTENT=$("${G[@]}" show "$C:.agent-actions.toml")   # parse THIS, never the working file
```

Then record `C` as `LAST`.

- `%G?`/`%GF`/`%GP` semantics: [doc] git pretty-formats. `%GP` is the primary key fingerprint when a subkey signs.
- **DECIDE-3 (replace refs) [inf, untested]:** git honors `refs/replace/*` by default. The agent can create replace objects, so `git show C:file` could return a substituted blob while `C`'s signature still verifies. `GIT_NO_REPLACE_OBJECTS=1` ([doc] git(1)) disables that. Same reasoning for grafts.
- **DECIDE-4 (rollback):** the agent can check out an older commit whose signed definitions contained an action you have since removed. I propose accepting only commits that descend from the last accepted one. Override with `<broker> verify --accept <commit>`.

### 8.5 Socket protocol (proposed)

- Unix stream socket, newline-delimited JSON, one request → one response.
- The broker checks the peer UID via `SO_PEERCRED` == its own UID ([doc] man 7 unix).
- Requests: `list_actions`, `run_action`, `job_status`, `request_fetch` (fields as in §8.6, plus `repo`, the shim's cwd).

### 8.6 MCP tools (served by `<broker> mcp`)

| Tool | Input | Output |
|---|---|---|
| `list_actions` | — | `[{name, description, params:{name:{pattern, optional, description}}}]`. Only `invoker="agent"` actions |
| `run_action` | `{name, params}` | Waits ≤ 30 s, then `{job_id, state, exit_code?, output_tail?}` |
| `job_status` | `{job_id}` | `{state: queued\|running\|succeeded\|failed\|timed_out\|killed\|rejected, exit_code, output_tail, log_path}` |
| `request_fetch` | `{ecosystem: maven\|npm\|image, coordinates: [..]}` | `{request_id, state: pending_user}`. You then run `<broker> fetch <request_id>` |

- `output_tail`: the last 200 lines, ≤ 16 KiB. The full log is at `log_path`, which the agent's Fence must be allowed to read.
- Output is **untrusted** text. It's capped but not sanitized.
- **DECIDE-5 (registration):** register the shim at **user scope**, not in the repo's `.mcp.json`. The latter is agent-writable, and an agent could swap the command. ⚠ Check the Claude Code docs for the user-scope flag of `claude mcp add`.

### 8.7 Job execution (common)

1. Verify trust (§8.4). Validate the action, invoker, and params.
2. Acquire the per-project lock. FIFO queue; fetches take the same lock.
3. Create the job dir with `xdg/` (mode 0700), `tmp/`, `log.txt`.
4. Wrap the job:

   ```bash
   systemd-run --user --scope --quiet --collect \
     -p MemoryMax=… -p TasksMax=… -p CPUQuota=… -- \
   timeout --kill-after=30s <timeout> <engine command>
   ```

   [doc] systemd-run(1), systemd.resource-control(5). Proposed defaults: 30 min, `MemoryMax=8G`, `TasksMax=4096`, `CPUQuota=400%`.
5. Environment for both engines:
   - `MAVEN_OPTS=-Dmaven.repo.local=<cache>/m2`
   - `npm_config_cache=<cache>/npm` ([doc] npm config: env `npm_config_*`)
   - Unset: `SSH_AUTH_SOCK`, `DBUS_SESSION_BUS_ADDRESS`, `GPG_AGENT_INFO`, `DOCKER_HOST` (re-set for the container engine)

### 8.8 Fence engine

Generated per job (field names: Fence config reference, [doc] secondary via DeepWiki @552dc6d0):

```json
{
  "filesystem": {
    "defaultDenyRead": true,
    "allowRead":  ["<repo>", "<toolchain paths>", "<cache dirs>", "<job dir>"],
    "allowWrite": ["<repo>/<each writes dir>", "<cache dirs>", "<job dir>"]
  },
  "network": { "allowedDomains": [] }
}
```

Command: `fence --settings <job>/fence.json -- <argv>`, cwd = repo.

**DECIDE-6 (read scope):** the original Q13 decision covered writes only. Fence reads everything by default, so a test could read `~/.ssh/id_*`, `~/.m2/settings.xml`, and `~/.npmrc` and write them into `target/`, where the agent reads them. I propose `defaultDenyRead` plus an explicit `allowRead` list. Toolchains (JDK, Maven, Node; e.g. `~/.sdkman`, `~/.local/share/mise/installs`) come from `config.toml`.

### 8.9 Container engine (verified core: E9, E14)

```bash
podman unshare -- bwrap \
  --dev-bind / / \
  --tmpfs /run --tmpfs /tmp --tmpfs "$HOME" \
  --unshare-net --unshare-pid --new-session --die-with-parent \
  <for each toolchain path T: --ro-bind T T> \
  --ro-bind "$REPO" "$REPO" \
  <for each writes dir D: --bind "$REPO/D" "$REPO/D"> \
  <for each protected path P: --ro-bind-try "$REPO/P" "$REPO/P"> \
  --bind "$CACHE" "$CACHE" --bind "$JOB" "$JOB" \
  --setenv XDG_RUNTIME_DIR "$JOB/xdg" \
  --setenv CONTAINERS_CONF "$JOB/containers.conf" \
  --setenv CONTAINERS_STORAGE_CONF "$JOB/storage.conf" \
  --setenv DOCKER_HOST "unix://$JOB/xdg/podman.sock" \
  --setenv TESTCONTAINERS_RYUK_DISABLED true \
  --chdir "$REPO" \
  -- <broker> job-init -- <argv>
```

- **Order matters:** later mounts shadow earlier ones ([doc] bwrap(1)). Protected paths come **after** `writes`.
- `job-init` starts `podman system service --time=0 unix://$XDG_RUNTIME_DIR/podman.sock`, waits for `/_ping`, runs argv, then stops the service.
- `storage.conf`: `[storage] driver="vfs"`, `graphroot=<cache>/containers/store`, `runroot=<cache>/containers/run`. **Identical** for host fetch and jobs (E14d).
- `containers.conf`: `[containers] log_driver="k8s-file"`, `[engine] events_logger="file"`, `cgroup_manager="cgroupfs"` ([doc] containers.conf(5); key names ⚠ verify against Podman 3.4's man page).
- Not covered by the spike: `--unshare-pid`, `--new-session`, `--tmpfs /tmp`, the toolchain binds, and the API service under Podman 3.4. Covered by conformance (§9).
- `--dev-bind / /` exposes host paths outside the tmpfs'd ones, e.g. the Nix daemon socket `/nix/var/nix/daemon-socket/socket` [inf]. Conformance probe P7 must pass; hide offenders with `--tmpfs` over their directory.

### 8.10 Coordinate fetch (user-only)

These run on the host, in an empty `mktemp -d` cwd, with your normal credentials and **no repo files involved**.

| Ecosystem | Accepted format (regex, anchored) | Command |
|---|---|---|
| Maven | `[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+:[A-Za-z0-9_.+-]+(:[a-z0-9-]+(:[A-Za-z0-9_.-]+)?)?` (g:a:v[:packaging[:classifier]]) | `mvn -B -ntp org.apache.maven.plugins:maven-dependency-plugin:<PINNED>:get -Dartifact=<coord> -Dmaven.repo.local=<cache>/m2` |
| npm | `(@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*@[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?` (exact versions only) | `npm cache add <spec> --cache <cache>/npm` |
| Image | `[a-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+@sha256:[a-f0-9]{64}` (registry host + digest required) | `podman --root <store> --runroot <run> --storage-driver=vfs pull <ref>` |

- `<PINNED>`: a maven-dependency-plugin version you verified on Maven Central. ⚠ Not chosen yet. The `artifact` format is from the plugin's `get` goal docs (⚠ re-check).
- `<broker> fetch <id>` prints every coordinate, asks y/N, runs argv arrays (never a shell), and takes the project lock.
- Expect iterative round trips: offline build fails → agent requests the missing artifact → you fetch.

### 8.11 Agent-side requirements (your Fence config)

- The broker socket lives under `~/.local/state/<broker>/`. Connecting through Fence's read-only mount works (E13 control).
- None of the §8.2 paths may be in the agent's `allowWrite`.
- No `allowUnixSockets` needed; it's a no-op on Linux (E11).

## 9. Conformance & acceptance tests

`<broker> conformance` runs P1–P8 in **both** engines, before first use and after any Fence, Podman, or bwrap upgrade.

| ID | Probe | Expected |
|---|---|---|
| P1 | Write a file in a declared `writes` dir | Success |
| P2 | Write a file in the repo outside `writes` | Fail |
| P3 | Write `.git/hooks/x`, `.mvn/x`, `.idea/x` | Fail |
| P4 | Read `~/.ssh/*`, `~/.gnupg/*`, `~/.m2/settings.xml`, `~/.npmrc`, `~/.docker/config.json` | Fail / absent |
| P5 | TCP connect to an external host | Fail |
| P6 | TCP connect to a host-loopback service started by the test harness | Fail |
| P7 | Socket inventory (§6 E13 method) | Only `$JOB/xdg/podman.sock` (container engine) or nothing |
| P8 | `writes` entry that's a symlink | Broker rejects before start |
| A1 | Container engine: Postgres via Podman CLI (`pg-spike.sh`) | Ready + port reachable ✅ [obs-H] |
| A2 | Container engine: a real Testcontainers test from a project | Passes offline |
| A3 | Trust: §6 E4 cases + replace-ref + grafts + rollback | Only signed, clean, descendant passes |

## 10. Residual risks

- **Agent-written code run outside the sandbox** (IntelliJ Maven import, `mvnw`, your own runs) executes with full privileges. Review is the only control.
- **Sandbox build outputs** (jars, images) are untrusted until rebuilt by you.
- **Prompt injection through output**: capped, not prevented. The agent already reads untrusted repo content.
- **Kernel attack surface**: unprivileged user namespaces (bwrap, `podman unshare`).
- **Fence correctness** for the Fence engine: version-dependent, mitigated by conformance.
- **Threat C**: a malicious dependency fetched by coordinates runs inside the sandbox only, which is the intended containment. If you use the same dependency in your normal builds, that's ordinary supply-chain risk.

## 11. Open items

| ID | Item | How to close |
|---|---|---|
| O1 | Fence's full built-in dangerous-file list | Read Fence source; mirror it in §8.3 protected paths |
| O2 | Testcontainers ↔ Podman 3.4 Docker-compat API version | A2 with your real project; check the docker-java minimum API version |
| O3 | Unverified bwrap flags under `podman unshare` (§8.9) | Conformance P1–P7 |
| O4 | Host Fence version | `fence --version`; re-run E11 if it differs from 0.1.67 |
| O5 | Replace-refs attack (DECIDE-3) | A3 test |
| O6 | `containers.conf` key names on Podman 3.4 | `man containers.conf` on the host |
| O7 | Pinned maven-dependency-plugin version | Choose and verify on Maven Central |
| O8 | Shared rootless pause process between host fetch and jobs | Observe during A1/A2; [inf] harmless, since bwrap adds its own mount namespace |
| O9 | DECIDE-1…6 | Owner decision |

## 12. Runbook

- **Start:** `<broker> serve` in a terminal outside Fence. Ctrl-C revokes all action capability.
- **Add or change an action:** edit `.agent-actions.toml`, commit with `git commit -S`, run `<broker> verify`.
- **Agent needs a dependency:** the agent calls `request_fetch`; the `serve` terminal shows the request ID; run `<broker> fetch <id>`, review the coordinates, confirm.
- **New machine or key rotation:** export your new **public** key into the verifier `GNUPGHOME`, update the pin in `config.toml`, and sign the definitions file again with the new key.
- **After upgrading Fence, Podman, or bwrap:** run `<broker> conformance`.

## 13. Implementation milestones

1. **M1 Trust:** verifier + `verify` CLI; A3 green.
2. **M2 Fence engine:** `serve`, socket protocol, MCP shim, `list_actions` / `run_action` / `job_status`; P1–P8 green for Fence.
3. **M3 Container engine:** §8.9; P1–P8 + A1 + A2 green.
4. **M4 Fetch:** `request_fetch` + `<broker> fetch`.
5. **M5 Ops:** limits, logs, queueing; launcher integration (Q18 C).

## 14. References

- Fence: github.com/fencesandbox/fence (config reference, security model)
- bubblewrap: man bwrap(1); NEWS (overlay options added in 0.11.0)
- Podman: man podman-unshare(1), containers.conf(5), containers-storage.conf(5)
- Docker rootless prerequisites: docs.docker.com/engine/security/rootless/
- Testcontainers environments: java.testcontainers.org/supported_docker_environment/
- git: git(1) (`--no-replace-objects`), pretty-formats (`%G?`, `%GF`, `%GP`)
- systemd: systemd-run(1), systemd.resource-control(5)
- Linux: user_namespaces(7), unix(7)
- grill-me: github.com/mattpocock/skills
