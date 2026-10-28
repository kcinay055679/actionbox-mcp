# actionbox: Design & Implementation Spec

- **Status:** Design complete. Remaining open items are verification only (§11).
- **Date:** 2026-10-28
- **Owner:** Yanick Minder
- **Origin:**
  - Requirements session (grill-me method, Q1–Q21)
  - Refinement session (grill-with-docs, R1–R24)
- **Related:**
  - [README.md](README.md): user-facing contract
  - [GLOSSARY.md](GLOSSARY.md): terms
  - [docs/adr/](docs/adr/): ADR 0001 (single engine), ADR 0002 (trust check)

Terms (owner, project, action, job, engine, broker, shim, fetch request, fetch, trusted commit, writes dir, protected path, project cache) are used as defined in GLOSSARY.md.

**Evidence tags:**

| Tag | Meaning |
|---|---|
| **[obs-H]** | Observed on the target host |
| **[obs-C]** | Observed in a test container (Ubuntu 24.04, kernel 6.18, Podman 4.9.3, bwrap 0.9.0, git 2.43.0, Fence 0.1.67) |
| **[src]** | Read from upstream source at the named tag |
| **[doc]** | Documented by the named source |
| **[inf]** | Inference, not tested |
| **⚠** | Unverified; closes via the named test |

---

## 1. Problem

Claude Code runs inside **Fence**, a bubblewrap-based sandbox, with a deliberately defensive policy: no Docker, no Maven, no network beyond what's allowed. Some agent tasks still need those tools: running tests, building, linting, Testcontainers.

The goal is a **fixed, per-project set of actions** that the agent can invoke and that run on the host **without weakening the sandbox**.

## 2. Goals / Non-goals

**Goals**

- The agent invokes only actions the owner defined and signed, with typed params.
- Each job runs isolated: no secrets, no network, write access only where declared.
- Container-based tests (Testcontainers + Postgres) work.
- Minimal host changes: one package, `podman`.

**Non-goals**

- Protecting against malicious third-party dependencies (threat C, §3).
- Making agent-written code safe to run **outside** actionbox. That remains the owner's review boundary.
- Multi-user or team-shared deployment.

## 3. Threat model

| ID | Threat | In scope |
|---|---|---|
| A | Accidents: destructive commands, runaway processes | Yes |
| B | Prompt injection: the agent is steered into exfiltration or persistence | Yes |
| C | Compromised dependency executing during a build | **No** |

**Key consequence:** an action is code execution by design. `mvn test` runs test code the agent can write. Under B, every job must therefore be isolated from secrets, network and host persistence paths.

**The signed action list selects what runs; the engine is the security boundary.**

## 4. Target environment

| Item | Value | Evidence |
|---|---|---|
| OS | Pop!_OS (Ubuntu 22.04 base), Sway/Regolith | [obs-H] |
| cgroups | v2; user manager delegates `cpuset cpu io memory pids` | [obs-H] |
| Podman | 3.4.4, runtime crun 0.17; Docker-compat API reports 1.40 | [obs-H], [src] podman v3.4.4 `version/version.go` |
| bubblewrap | 0.6.1 (no overlay support; that needs ≥ 0.11) | [obs-H], [doc] bwrap NEWS |
| util-linux | 2.37.2 (no `unshare --map-auto`) | [obs-H] |
| `/dev/net/tun` | `crw-rw-rw-` | [obs-H] |
| subuid | `100000:65536` (inferred from the observed uid_map) | [obs-H] |
| Docker | Rootless, used for the owner's normal work; untouched by this design | [stated] |
| Signing | GnuPG commit signing | [stated] |
| Fence (host) | 0.1.67, same as the test container | [obs-H] |
| Agent's Fence config | `extends: "@base"`, `allowWrite: ["."]` (repo `fence.jsonc`) | [obs-H] |
| Nix | Installed (`/nix` present) | [obs-H] |

## 5. Decision log

### 5.1 Requirements session (Q1–Q21)

| # | Question | Decision | Why | Rejected |
|---|---|---|---|---|
| Q1 | Threat model | A + B, not C | Owner choice | — |
| Q2 | Network and credentials for jobs | Offline, no credentials | Under B, a test can read anything the build can | Live network in tests |
| Q3 | Docker usage | Testcontainers + image build only | Docker socket = engine privileges; ad-hoc `run` unneeded | `compose up`, ad-hoc `run` |
| Q4 | Where definitions live | In repo, trusted only via a commit signed by the pinned key. Mechanism refined by **R5** | Travels with the repo, no extra infrastructure | Out-of-repo file; Fence write-deny |
| Q5 | Signing capability | Agent commits but cannot sign; GPG | Agent sockets unreachable (verified in Q17) | — |
| Q6 | Where the broker lives | Outside Fence, socket into the jail | stdio MCP servers inherit the jail; nested bwrap fails | MCP server inside Fence |
| Q7/Q8 | Network isolation vs. Testcontainers | Container engine runs **inside** the job sandbox | `--unshare-net` hides host-loopback ports; an in-sandbox engine shares the sandbox loopback | Host firewall (nftables), pasta forwarding, fixed ports |
| Q9 | Container engine | Podman (rootless), not Docker | Daemonless: inherits the sandbox's namespaces | Owner's rootless dockerd (sees real `$HOME`, has egress) |
| Q10 | Argument freedom | Typed params validated against the signed schema; argv arrays only | Covers "run one test" without exposing the CLI surface | Fixed-only; free-form |
| Q11 | Fetch approval | Out-of-band CLI on host | Claude Code's own settings may be agent-writable | Claude Code permission prompt; desktop dialog |
| Q12 | Caches | Per-project caches, never read outside jobs and fetches | A poisoned shared `~/.m2` would execute on the owner's next host build | Shared personal caches; read-only overlay (needs bwrap ≥ 0.11) |
| Q13/Q14 | Repo write scope and engine | Repo read-only, per-action writes dirs. ~~Fence engine for non-container actions, bwrap engine for container actions~~ superseded by **R3** | Allowlist fails closed | Repo read-write; deny-list only |
| Q15 | MCP surface | Generic tools, job IDs, capped output, one job per project | Long runs, stable permission rules | One MCP tool per action |
| Q16 | Fetching | **Owner-only, coordinate-based fetch on host**; jobs never get network | Never executes repo code on host; no credentials in jobs | Sandboxed fetch with network; signed-tree project fetch; "unchanged files" gate |
| Q17 | Socket exposure in agent's Fence | No change needed | Inventory found no reachable sockets [obs-H] | `denyRead` on `/run/user/1000` |
| Q18 | Broker lifecycle | Foreground `serve` now; later started by the Fence launcher | Visible kill switch, no persistent host config | systemd user socket activation |
| Q19 | Definitions format | Dedicated `.agent-actions.toml` | Needs fields mise lacks; mise tasks are shell strings | Reuse `mise.toml` tasks |
| Q20 | Resource limits | Timeout + `systemd-run --user --scope` limits per job | Covers cgroup-less containers inside the scope | Timeout only |

### 5.2 Refinement session (R1–R24)

| # | Question | Decision | Why | Rejected |
|---|---|---|---|---|
| R1 | Name | `actionbox` (binary, paths) | Repo name | — |
| R2 | Language | Go | Single static binary; MCP Go SDK | — |
| R3 | Engines | **One** engine, `podman unshare -- bwrap`, for all actions; `podman system service` only when `containers = true` (ADR 0001) | One conformance surface; Fence engine read the whole host; Podman can't run in Fence (E10) | Fence engine + bwrap engine |
| R4 | Project registration (was DECIDE-1) | Broker serves only projects registered in `config.toml`; any agent may call any registered project | Shim's `repo` is agent-controlled; all registered projects are owner-signed | Socket per project |
| R5 | Trust check (was DECIDE-3) | Raw objects, re-hash, `gpgv`, byte compare; no git porcelain (ADR 0002) | Git doesn't re-hash on read; repo config and `.gitattributes` can run commands | `git log %G?` + `git diff` with `-c` overrides |
| R6 | Rollback (was DECIDE-4) | Candidate commit must descend from the trusted commit; override with `verify --accept` | Simple, fails closed | Revocation list of retired blobs |
| R7 | Symlinks (was DECIDE-2) | `lstat` check before start **plus** a dev/inode check inside the sandbox after mounting | Agent can swap a dir for a symlink between check and mount | `lstat` only |
| R8 | Shim registration (was DECIDE-5) | User scope (`claude mcp add --scope user`); the shim holds no trust | Agent can reach `broker.sock` directly anyway | Treating `.mcp.json` as a control |
| R9 | Vocabulary | GLOSSARY.md; "allowlist" and "sandbox profile" avoided | Overloaded terms | — |
| R10 | Docker API version | Container jobs pin docker-java `api.version=1.40` | Testcontainers ≥ 2.0.2 sends 1.44; Podman 3.4 implements 1.40 but doesn't enforce the version [src] | Unpinned |
| R11 | Fetch plugin (was O7) | `maven-dependency-plugin:3.11.0`; owner's `settings.xml` mirrors/credentials apply | Latest release (2026-05-24) [src] | — |
| R12 | Protected paths (was O1) | Add Fence's built-in list, `.gitattributes`, all of `.git`; existing protected-name files at depth ≤ 3 inside writes dirs are re-bound read-only | Mirror Fence's mandatory protection [src] | Root-level only |
| R13 | Engine root filesystem | `--ro-bind / /` + explicit tmpfs and dev/proc mounts instead of `--dev-bind / /` | `--dev-bind / /` left `/var/tmp`, `/dev/shm`, `/media`, `/mnt` writable/readable | `--dev-bind / /` |
| R14 | User namespace | Every job runs through `podman unshare`, containers or not | One code path, one conformance run | bwrap alone for non-container jobs |
| R15 | Project IDs | Owner-chosen names (`[projects.<name>]`) | Explicit; path moves are config edits, not silent cache loss | sha256 of realpath |
| R16 | Live repo during jobs | Accepted: jobs see agent edits made during a run | Snapshot (worktree/stash) is slower and more complex | Snapshot per job |
| R17 | Fetch request lifecycle | Stored as JSON; `pending → fetched \| rejected \| expired (24h)`; new tool `fetch_status`; `fetch` without ID lists pending | Agent needs to poll; stale requests shouldn't pile up | — |
| R18 | Lock | `flock` on `projects/<name>/lock`; `run`/`fetch` work without `serve` | Crashed process releases the lock | Lock held in `serve` memory |
| R19 | Owner term | "Owner"; `invoker = "agent" \| "owner"` | "user" clashed with the invoker value | `invoker = "user"` |
| R20 | Job retention | `serve` prunes at startup and after each job: last 50 per project, max 14 days (configurable) | Bounded disk use | Manual cleanup |
| R21 | Job environment | `--clearenv` + explicit allowlist | An unset list leaks anything not listed (`GITHUB_TOKEN`, `AWS_*`) | Unset list |
| R22 | npm install scripts | No `--ignore-scripts` in examples; per-action choice | Scripts run inside the job sandbox, which is the threat-C containment | Always `--ignore-scripts` |
| R23 | Flag injection | Param values starting with `-` rejected unless `allow_leading_dash = true` | A whole-element placeholder could inject flags | Owner patterns only |
| R24 | MCP library | `github.com/modelcontextprotocol/go-sdk`, stdio | Official SDK | — |

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
| E10 | Second Fence for jobs | [obs-C] | `.git/hooks` write blocked by default; declared dir writable; egress blocked. Podman inside Fence **fails** in all variants (newuidmap; setgroups; read-only `/run`) |
| E11 | Fence `allowUnixSockets` on Linux | [obs-C] | **No-op**: all sockets reachable with or without it. Only `denyRead` hides a socket; an `allowRead` carve-out inside a denied dir doesn't restore it |
| E12 | `$XDG_RUNTIME_DIR` sockets from the agent's Fence | [obs-H] | Fence rewrites the variable to an empty dir; `/run/user/1000` doesn't exist inside |
| E13 | Full socket inventory inside the agent's Fence + positive control | [obs-H] | No reachable sockets; a control socket under `~` is reachable |
| E14 | `pg-spike.sh` iterations | [obs-H] | See below |
| E15 | Host versions and man pages | [obs-H] | `fence --version` 0.1.67; `podman` 3.4.4; `bwrap` 0.6.1; `containers.conf(5)`: `log_driver` in `[containers]`, `events_logger` and `cgroup_manager` in `[engine]` |
| E16 | Upstream source reading | [src] | Fence v0.1.67 dangerous paths and config keys; Testcontainers 2.0.5 API version logic; Podman v3.4.4 compat API; maven-dependency-plugin 3.11.0 `get` goal (details in §8) |

E14 iterations:

| Step | Problem | Fix / result |
|---|---|---|
| (a) | Script pasted into zsh | No word-splitting |
| (b) | `--map-auto` unavailable | Not used |
| (c) | `/run/user/1000` hidden | Private `XDG_RUNTIME_DIR` |
| (d) | Runroot mismatch | Shared runroot |
| (e) | — | **PASS**: Postgres ready, `PORT-REACHABLE`, uid_map `0 1000 1 \| 1 100000 65536`, `ifaces: lo` |

## 7. Architecture

```text
 Host (owner's UID, outside any sandbox)
 ┌──────────────────────────────────────────────────────────────────────┐
 │  owner ── actionbox fetch / run / verify ──┐   (flock per project)   │
 │                                            ▼                         │
 │  actionbox serve ◄── ~/.local/state/actionbox/broker.sock            │
 │     │ verify definitions: raw objects + gpgv (pinned keyring)        │
 │     │ flock projects/<name>/lock, systemd-run --user --scope         │
 │     └─► podman unshare -- bwrap --ro-bind / / ... -- job-init argv   │
 │                                   └─ podman system service           │
 │                                      (only if containers = true)     │
 │                                                                      │
 │  Agent's Fence (unchanged policy)                                    │
 │   └─ Claude Code ─stdio─► actionbox mcp (shim) ─► broker.sock        │
 └──────────────────────────────────────────────────────────────────────┘
```

## 8. Specification

### 8.1 Components

| Component | Runs where | Purpose |
|---|---|---|
| `actionbox serve` | Host, foreground terminal | Accepts socket requests, verifies trust, runs jobs, shows events and fetch requests, prunes jobs |
| `actionbox mcp` | Inside the agent's Fence (spawned by Claude Code) | Shim: stdio MCP server; forwards to `broker.sock`. Holds no trust |
| `actionbox run <action>` | Host | Runs any action, including owner-only ones; works without `serve` |
| `actionbox fetch [id]` | Host | Without an ID: lists pending fetch requests. With an ID: shows, validates, confirms, executes |
| `actionbox verify [project] [--accept <commit>]` | Host | Prints trust status; `--accept` sets the trusted commit explicitly |
| `actionbox conformance [project]` | Host | Runs §9 probes against the engine |
| `actionbox job-init` | Inside the job sandbox (internal) | Post-mount checks, optional Podman service, runs argv |

- Language: Go, as a single static binary.
- MCP: `github.com/modelcontextprotocol/go-sdk`, stdio transport.

### 8.2 Host paths

The agent's Fence must **not** have write access to any of these.

| Path | Content |
|---|---|
| `~/.config/actionbox/config.toml` | Pinned fingerprint, projects, toolchain paths and env, limit caps and defaults, retention |
| `~/.local/share/actionbox/keyring.gpg` | Verifier keyring for `gpgv`: the owner's **public** key only |
| `~/.local/share/actionbox/projects/<name>/m2/` | Maven project cache |
| `~/.local/share/actionbox/projects/<name>/npm/` | npm project cache |
| `~/.local/share/actionbox/projects/<name>/containers/{store,run}/` | Podman graphroot + runroot (always used as a pair) |
| `~/.local/share/actionbox/projects/<name>/lock` | `flock` target: one job or fetch at a time per project |
| `~/.local/state/actionbox/broker.sock` | Socket (agent needs connect only) |
| `~/.local/state/actionbox/trust/<name>.json` | Trusted commit (rollback guard) |
| `~/.local/state/actionbox/fetch/<request_id>.json` | Fetch requests |
| `~/.local/state/actionbox/jobs/<name>/<job>/` | `log.txt`, `meta.json` (agent needs read), `xdg/`, `tmp/`, `home/` |

`config.toml` sketch:

```toml
pinned_fingerprint = "<40 hex, primary key>"

[projects.pcts]
path = "/home/yminder/projects/pcts"

[toolchain]
ro_paths = ["/home/yminder/.sdkman/candidates", "/home/yminder/.local/share/mise/installs"]
env      = { JAVA_HOME = "/home/yminder/.sdkman/candidates/java/current" }
path     = ["/home/yminder/.sdkman/candidates/maven/current/bin"]

[limits]            # caps; actions may only lower them
timeout = "30m"
memory_max = "8G"
tasks_max = 4096
cpu_quota = "400%"

[retention]
jobs_per_project = 50
max_age = "14d"
```

### 8.3 Definitions file: `.agent-actions.toml`

```toml
schema_version = 1

[actions.test]
description = "Run backend tests, optionally one class"
invoker     = "agent"                  # agent | owner
kind        = "run"                    # run | coordinate-fetch
containers  = true                     # starts podman system service in the job
argv        = ["mvn", "-o", "-B", "test", "-Dtest={test_class}"]
writes      = ["target"]
timeout     = "20m"
memory_max  = "6G"
[actions.test.params.test_class]
pattern     = '[A-Za-z0-9_.#*]+'       # broker anchors as ^(...)$
optional    = true
description = "Surefire -Dtest filter"

[actions.lint]
description = "Frontend lint"
invoker     = "agent"
argv        = ["npx", "--offline", "eslint", "src"]
writes      = []

[actions.npm-ci]
description = "Install node_modules from the project cache"
invoker     = "agent"
argv        = ["npm", "ci", "--offline"]   # install scripts run inside the job sandbox (R22)
writes      = ["node_modules"]

[actions.format]
description = "Apply formatter to sources"
invoker     = "agent"
argv        = ["mvn", "-o", "-B", "spotless:apply"]
writes      = ["src", "target"]

[actions.fetch]
description = "Fetch coordinates requested by the agent"
invoker     = "owner"
kind        = "coordinate-fetch"
```

**Rules. The broker rejects the whole file on any violation.**

**`argv`**
- An array of strings. A `{param}` placeholder may appear inside an element.
- A substituted value is never split.
- If an optional param is omitted, every element containing its placeholder is **dropped**.

**`containers`**
- Bool, default `false`. Replaces the former `engine` field.

**Param values**
- Must fully match `^(pattern)$`, be ≤ 256 bytes, and contain no NUL.
- May not start with `-` unless the param sets `allow_leading_dash = true` (R23).
- Unknown params are rejected.

**`writes`**
- Project-relative **directories**.
- Not allowed: absolute paths, `..`, globs, a protected path, or an ancestor or descendant of a protected path.

**Symlinks (R7)**
- Before start, the broker `lstat`s every component of the project root, of each writes dir and of each protected path. It refuses if any component is a symlink.
- It creates missing writes dirs, then records each bound path's `(dev, inode)` in the job's `meta.json`.
- After mounting, `job-init` re-stats each mount point inside the sandbox. It aborts the job if any `(dev, inode)` differs.

**Limits**
- Optional per action, clamped to the caps in `config.toml`.

**Protected paths (R12)**
- Never writable; re-bound read-only after the writes binds.
- At project root:
  - `.git`, `.agent-actions.toml`, `.gitattributes`, `.gitmodules`, `.gitconfig`
  - `.idea`, `.vscode`, `.envrc`, `.mcp.json`, `.claude`
  - `mvnw`, `mvnw.cmd`, `.mvn`, `.npmrc`
  - `.bashrc`, `.bash_profile`, `.zshrc`, `.zprofile`, `.profile`, `.ripgreprc`
- Inside each writes dir: any **existing** entry with a protected name at depth ≤ 3 is re-bound read-only too, mirroring Fence's `DefaultMaxDangerousFileDepth = 3` [src] fence v0.1.67 `internal/sandbox/dangerous.go`.

### 8.4 Trust verification (R5, R6, ADR 0002)

Runs on every request, before anything executes. Inputs:

- the project path,
- `PIN`: the primary-key fingerprint,
- `KEYRING`,
- `TRUSTED`: the trusted commit from `trust/<name>.json`, may be empty.

**Git invocation.** Git is used only for plumbing that returns object IDs or raw object bytes, and always with:

```bash
GIT_NO_REPLACE_OBJECTS=1 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
GIT_ATTR_NOSYSTEM=1 git -C "$REPO" -c core.fsmonitor=false -c core.hooksPath=/dev/null ...
```

Nothing git returns is trusted until re-hashed.

**Steps:**

1. **Preconditions.** Fail if `.git/info/grafts` exists or the repo uses the SHA-256 object format. That format isn't supported yet; fail closed.
2. **Candidate.** `C = git rev-list -1 HEAD -- .agent-actions.toml`. This is a hint only.
3. **Commit object.** Read `git cat-file commit C`.
   - Check that `sha1("commit <len>\0" + body) == C`.
   - Split off the `gpgsig` header to get the signed payload and the signature.
4. **Signature.** Run `gpgv --keyring KEYRING --status-fd 1 sig payload`.
   - Require `GOODSIG` and a `VALIDSIG` line whose primary-key fingerprint field equals `PIN`.
   - No other status may indicate expiry or revocation.
   - `gpgv` reads no `gpg.*` git config and no agent sockets [doc] gpgv(1).
5. **Tree.** Take the `tree` ID from the verified commit body.
   - Read it with `git cat-file tree`, re-hash it, and find the root entry `.agent-actions.toml`.
   - The mode must be `100644`; a symlink (`120000`) fails.
6. **Blob.** Read the blob with `git cat-file blob` and re-hash it.
7. **Working file.** Open `$REPO/.agent-actions.toml` with `O_NOFOLLOW` and require a regular file.
   - Require its bytes to equal the blob byte-for-byte. Otherwise fail with "uncommitted edits".
8. **Rollback (R6).** If `TRUSTED` is set and `C != TRUSTED`:
   - Walk parents from `C`, re-hashing every commit object read.
   - Stop when `TRUSTED` is found (pass) or after 10,000 commits / at roots (fail with "rollback").
9. **Parse.** Parse **the verified blob**, never the working file, and validate it against §8.3.
10. **Record.** Store `C` as `TRUSTED`.

Notes:

- `verify --accept <commit>` runs steps 3–7 for `<commit>` and sets it as `TRUSTED` without the ancestry check.
- In the agent's Fence, `.git/config` and `.git/hooks` are already write-protected (`allowGitConfig` false by default) [src] fence v0.1.67 `internal/sandbox/linux.go`. The `-c` overrides are defense in depth.
- `.git/objects`, `.git/refs`, `.gitattributes` and `.git/info/*` stay agent-writable. That's why steps 3–7 re-hash and avoid porcelain.
- SHA-1 collision resistance is assumed. This is a residual risk (§10).

### 8.5 Socket protocol

- Unix stream socket, newline-delimited JSON, one request → one response.
- The broker checks the peer UID via `SO_PEERCRED` == its own UID [doc] unix(7).
- Every request carries `cwd`, the shim's working directory.
  - The broker maps `realpath(cwd)` to the registered project whose path is equal to it or a parent of it.
  - No match → `rejected: unregistered project`.
  - The shim is untrusted. Any registered project is reachable (R4).
- Requests: `list_actions`, `run_action`, `job_status`, `request_fetch`, `fetch_status`. Fields as in §8.6.

### 8.6 MCP tools (served by `actionbox mcp`)

| Tool | Input | Output |
|---|---|---|
| `list_actions` | — | `[{name, description, params:{name:{pattern, optional, allow_leading_dash, description}}}]`. Only `invoker="agent"` actions; untrusted definitions → error |
| `run_action` | `{name, params}` | Waits ≤ 30 s, then `{job_id, state, exit_code?, output_tail?}` |
| `job_status` | `{job_id}` | `{state: queued\|running\|succeeded\|failed\|timed_out\|killed\|rejected, exit_code, output_tail, log_path}` |
| `request_fetch` | `{ecosystem: maven\|npm\|image, coordinates: [..]}` | `{request_id, state: pending}`. Formats validated at request time (§8.9) |
| `fetch_status` | `{request_id}` | `{state: pending\|fetched\|rejected\|expired, detail?}` |

- `output_tail`: the last 200 lines, ≤ 16 KiB. The full log is at `log_path`, which the agent's Fence must be able to read.
- Output is **untrusted** text. It's capped but not sanitized.
- **Registration (R8):** `claude mcp add --scope user actionbox -- actionbox mcp`. The shim holds no trust: the agent can connect to `broker.sock` directly (E13 control). Where the shim is registered is therefore a convenience choice, not a security control.

### 8.7 Job execution

1. Verify trust (§8.4). Validate the action, invoker and params (§8.3).
2. `flock` `projects/<name>/lock` (R18). Waiting jobs are served in arrival order by `serve`; fetches and `run` take the same lock.
3. Create the job dir: `xdg/` (mode 0700), `tmp/`, `home/`, `log.txt`, `meta.json`. Do the symlink checks and record the inodes (§8.3).
4. Wrap the job:

   ```bash
   systemd-run --user --scope --quiet --collect \
     -p MemoryMax=… -p TasksMax=… -p CPUQuota=… -- \
   timeout --kill-after=30s <timeout> <engine command (§8.8)>
   ```

   [doc] systemd-run(1), systemd.resource-control(5). Defaults: 30 min, `MemoryMax=8G`, `TasksMax=4096`, `CPUQuota=400%`.
5. **Environment (R21).** The engine starts from `--clearenv` and sets only:

   | Variable | Value |
   |---|---|
   | `PATH` | System dirs + `[toolchain].path` |
   | `HOME` | `$JOB/home` |
   | `LANG` | `C.UTF-8` |
   | `TERM` | `dumb` |
   | `TMPDIR` | `/tmp` |
   | `MAVEN_OPTS` | `-Dmaven.repo.local=<cache>/m2` |
   | `npm_config_cache` | `<cache>/npm` [doc] npm config |
   | `[toolchain].env` | e.g. `JAVA_HOME` |
   | Container jobs only | `XDG_RUNTIME_DIR`, `CONTAINERS_CONF`, `CONTAINERS_STORAGE_CONF`, `DOCKER_HOST`, `TESTCONTAINERS_RYUK_DISABLED=true` |

6. **Retention (R20).** After each job, and at `serve` startup, prune `jobs/<name>/`. Keep the newest `jobs_per_project` and drop anything older than `max_age`.

### 8.8 Engine (R3, R13, R14, ADR 0001)

```bash
podman unshare -- bwrap \
  --ro-bind / / \
  --dev /dev --dev-bind /dev/net/tun /dev/net/tun --proc /proc \
  --tmpfs /run --tmpfs /tmp --tmpfs /var/tmp --tmpfs /dev/shm \
  --tmpfs /media --tmpfs /mnt --tmpfs "$HOME" \
  --tmpfs /nix/var/nix/daemon-socket \
  --unshare-net --unshare-pid --unshare-ipc --new-session --die-with-parent \
  --clearenv <--setenv for each §8.7 variable> \
  <for each toolchain ro_path T: --ro-bind T T> \
  --ro-bind "$REPO" "$REPO" \
  <for each writes dir D: --bind "$REPO/D" "$REPO/D"> \
  <for each protected path P (root + depth ≤ 3 in writes): --ro-bind-try "$REPO/P" "$REPO/P"> \
  --bind "$CACHE" "$CACHE" --bind "$JOB" "$JOB" \
  --chdir "$REPO" \
  -- actionbox job-init --meta "$JOB/meta.json" -- <argv>
```

**Mounts**
- `--ro-bind / /` replaces `--dev-bind / /` (R13). Persistence paths (`/var/tmp`, `/dev/shm`), external drives (`/media`, `/mnt`) and the Nix daemon socket are hidden behind tmpfs.
- **Order matters:** later mounts shadow earlier ones [doc] bwrap(1). Protected paths come **after** writes dirs.
- `/dev/net/tun` is needed by `slirp4netns` for container networking inside the sandbox. ⚠ O3

**`job-init`**
1. Re-stats every recorded mount point and aborts on a `(dev, inode)` mismatch (R7).
2. If `containers = true`:
   - writes `$HOME/.docker-java.properties` with `api.version=1.40` (R10, ⚠ O2),
   - starts `podman system service --time=0 unix://$XDG_RUNTIME_DIR/podman.sock`,
   - waits for `/_ping`.
3. Runs argv, then stops the service.

**Container config files**
- `storage.conf`: `[storage] driver="vfs"`, `graphroot=<cache>/containers/store`, `runroot=<cache>/containers/run`. It must be **identical** for host fetches and jobs (E14d).
- `containers.conf`:
  - `[containers] log_driver="k8s-file"`
  - `[engine] events_logger="file"`, `cgroup_manager="cgroupfs"`
  - Key names verified on Podman 3.4 (E15).

**Verification status**
- Verified core: E9, E14 (`podman unshare` + bwrap, vfs, shared runroot, Postgres with `--user 999`).
- Not yet verified: the R13 root layout, `--unshare-pid/ipc`, `--new-session`, `--clearenv`, toolchain binds, the API service under Podman 3.4, and non-container jobs under `podman unshare`. ⚠ O3, covered by §9.

### 8.9 Coordinate fetch (owner-only, R11, R17)

Fetches run on the host, in an empty `mktemp -d` cwd, with the owner's normal credentials and **no repo files involved**.

| Ecosystem | Accepted format (anchored regex) | Command |
|---|---|---|
| Maven | See below | `mvn -B -ntp org.apache.maven.plugins:maven-dependency-plugin:3.11.0:get -Dartifact=<coord> -Dmaven.repo.local=<cache>/m2` |
| npm | See below | `npm cache add <spec> --cache <cache>/npm` |
| Image | See below | `podman --root <store> --runroot <run> --storage-driver=vfs pull <ref>` |

Accepted formats:

- **Maven** (`g:a:v[:packaging[:classifier]]`):

  ```
  [A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+:[A-Za-z0-9_.+-]+(:[a-z0-9-]+(:[A-Za-z0-9_.-]+)?)?
  ```

- **npm** (exact versions only):

  ```
  (@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*@[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?
  ```

- **Image** (registry host + digest required):

  ```
  [a-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+@sha256:[a-f0-9]{64}
  ```

Maven notes:
- The `get` goal accepts 3–5 colon-separated tokens and runs without a project. [src] maven-dependency-plugin 3.11.0 `GetMojo.java`
- Repos come from Central plus active `settings.xml` profiles. Mirrors, proxies and server auth apply. `-DremoteRepositories` is not used.
- `transitive` defaults to true.

Fetch requests:
- A request is stored at `fetch/<request_id>.json` with `project`, `ecosystem`, `coordinates`, `created`, `state`.
- States: `pending → fetched | rejected | expired`. A request expires after 24 h. A failed fetch keeps the request `pending`, with `detail`.

`actionbox fetch`:
- `actionbox fetch` with no ID lists pending requests.
- `actionbox fetch <id>`:
  1. prints every coordinate,
  2. asks y/N (`N` → `rejected`),
  3. takes the project lock,
  4. runs argv arrays, never a shell.

Expect iterative round trips: an offline build fails → the agent requests the missing artifact → the owner fetches → the agent retries.

### 8.10 Agent-side requirements (agent's Fence config)

- The broker socket lives under `~/.local/state/actionbox/`. Connecting through Fence's read-only mount works (E13 control).
- None of the §8.2 paths may be in the agent's `allowWrite`.
- Keep `allowGitConfig` unset (false). This keeps `.git/config` write-protected for the agent [src].
- No `allowUnixSockets` needed; it's a no-op on Linux (E11).
- The agent's Fence must be able to read `~/.local/state/actionbox/jobs/` for `log_path`.

## 9. Conformance & acceptance tests

`actionbox conformance` runs P1–P12 in the engine twice, once with `containers = false` and once with `containers = true`. Run it before first use and after any Podman, bwrap or kernel upgrade.

| ID | Probe | Expected |
|---|---|---|
| P1 | Write a file in a declared writes dir | Success |
| P2 | Write a file in the repo outside writes dirs | Fail |
| P3 | Write `.git/hooks/x`, `.mvn/x`, `.idea/x`, `.gitattributes`; write a pre-existing `<writes>/sub/.bashrc` | Fail |
| P4 | Read `~/.ssh/*`, `~/.gnupg/*`, `~/.m2/settings.xml`, `~/.npmrc`, `~/.docker/config.json` | Fail / absent |
| P5 | TCP connect to an external host | Fail |
| P6 | TCP connect to a host-loopback service started by the test harness | Fail |
| P7 | Socket inventory (E13 method) | Only `$JOB/xdg/podman.sock` (containers) or nothing |
| P8 | Writes dir that is a symlink | Broker rejects before start |
| P9 | Harness swaps a writes dir for a symlink to `$HOME` between broker check and mount | `job-init` aborts |
| P10 | Environment dump | Only the §8.7 variables; a harness-set `CANARY_TOKEN` absent |
| P11 | Write `/var/tmp/x`, `/dev/shm/x`; list `/media`, `/mnt` | Writes don't persist after the job; mounts empty |
| P12 | Resource limits: memory hog, fork bomb, sleep past timeout | `killed` / `timed_out`, host unaffected |
| A1 | Containers: Postgres via Podman CLI (`pg-spike.sh`) | Ready + port reachable ✅ [obs-H] (old layout; re-run on §8.8) |
| A2 | Containers: a real Testcontainers 2.x test from a project, `api.version=1.40` | Passes offline |
| A3 | Trust: the cases below | Only signed, genuine, clean, descendant passes |
| A4 | Params: leading `-` without `allow_leading_dash`; unknown param; NUL; 257 bytes | Rejected, nothing runs |

A3 cases:

- the E4 cases,
- a replace ref,
- grafts,
- rollback,
- a forged loose blob behind a signed commit,
- a forged tree,
- a forged intermediate commit on the ancestry path,
- repo `gpg.openpgp.program` set,
- a `.gitattributes` `textconv`,
- the definitions file as a symlink.

## 10. Residual risks

- **Agent-written code run outside actionbox** (IntelliJ Maven import, `mvnw`, the owner's own runs) executes with full privileges. Review is the only control.
- **Build outputs** (jars, images) produced by jobs are untrusted until the owner rebuilds them.
- **Prompt injection through output**: capped, not prevented. The agent already reads untrusted repo content.
- **Kernel attack surface**: unprivileged user namespaces (bwrap, `podman unshare`).
- **Cross-project calls (R4)**: any agent can run agent-invokable actions of any registered project.
- **Live repo (R16)**: a job may see a mix of two states of the code if the agent edits during the run. This affects correctness only, not isolation.
- **SHA-1**: trust relies on SHA-1 collision resistance of the object IDs referenced from the signed commit.
- **Threat C**: a malicious dependency fetched by coordinates runs inside the job only, which is the intended containment. If the owner uses the same dependency in normal builds, that's ordinary supply-chain risk.

## 11. Open items (verification only)

| ID | Item | How to close |
|---|---|---|
| O2 | Testcontainers 2.x ↔ Podman 3.4 compat API with `api.version=1.40` via `~/.docker-java.properties` | A2 with a real project |
| O3 | R13 root layout, `--unshare-pid/ipc`, `--clearenv`, `/dev/net/tun` bind, non-container jobs under `podman unshare` | Conformance P1–P12 + A1 re-run |
| O5 | Forged objects, replace refs, grafts, `gpg.openpgp.program`, textconv against §8.4 | A3 |
| O8 | Shared rootless pause process between host fetches and jobs | Observe during A1/A2; [inf] harmless, since bwrap adds its own mount namespace |

Closed in the refinement session:

| ID | Item | Resolution |
|---|---|---|
| O1 | Fence's dangerous-file list | R12 |
| O4 | Host Fence version | 0.1.67, E15 |
| O6 | `containers.conf` key names | E15 |
| O7 | maven-dependency-plugin version | R11 |
| O9 | DECIDE-1…6 | R4, R7, R5, R6, R8, and R3 (DECIDE-6 moot) |

## 12. Runbook

- **Start:** run `actionbox serve` in a terminal outside Fence. Ctrl-C revokes all agent capability.
- **Register a project:** add `[projects.<name>] path = "…"` to `config.toml`, then run `actionbox verify <name>`.
- **Add or change an action:** edit `.agent-actions.toml`, commit with `git commit -S`, run `actionbox verify`.
- **Switch to an older branch with older definitions:** run `actionbox verify --accept <commit>`.
- **Agent needs a dependency:**
  1. The agent calls `request_fetch`.
  2. `serve` shows the request.
  3. Run `actionbox fetch` to list pending requests, then `actionbox fetch <id>`, review the coordinates and confirm.
- **New machine or key rotation:**
  1. Export the new **public** key into `keyring.gpg` (`gpg --export <fpr> > keyring.gpg`).
  2. Update `pinned_fingerprint`.
  3. Re-sign the definitions file with the new key.
  4. Run `verify --accept`.
- **After upgrading Podman, bwrap or the kernel:** run `actionbox conformance`.

## 13. Implementation milestones

1. **M1 Trust:** §8.4 verifier + `verify` CLI; A3 green.
2. **M2 Engine without containers:**
   - `serve`, socket protocol, shim;
   - `list_actions` / `run_action` / `job_status`;
   - `job-init` inode check, env allowlist, limits, lock;
   - P1–P12 green with `containers = false`; A4 green.
3. **M3 Containers:** Podman service in `job-init`, `api.version` pin; P1–P12 green with `containers = true`; A1 + A2 green.
4. **M4 Fetch:** `request_fetch`, `fetch_status`, `actionbox fetch`.
5. **M5 Ops:** retention, event display, launcher integration (Q18).

## 14. References

| Topic | Sources |
|---|---|
| Fence | github.com/fencesandbox/fence (formerly Use-Tusk/fence), v0.1.67: `internal/sandbox/dangerous.go`, `internal/sandbox/linux.go`, `internal/config/config.go`, `docs/configuration.md` |
| bubblewrap | man bwrap(1); NEWS (overlay options added in 0.11.0) |
| Podman | man podman-unshare(1), containers.conf(5), containers-storage.conf(5); v3.4.4 `version/version.go` (compat API 1.40) |
| Testcontainers | 2.0.5 `DockerClientProviderStrategy.java` (API 1.44 with 1.32 fallback); PRs #11216, #11346; java.testcontainers.org/supported_docker_environment/ |
| docker-java | `api.version` configuration (`DefaultDockerClientConfig`) |
| maven-dependency-plugin | 3.11.0 `GetMojo.java` |
| git | git(1) (`GIT_NO_REPLACE_OBJECTS`, `GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL`), git-cat-file(1), gitformat-signature(5) |
| GnuPG | gpgv(1), `doc/DETAILS` (status lines `GOODSIG`, `VALIDSIG`) |
| systemd | systemd-run(1), systemd.resource-control(5) |
| Linux | user_namespaces(7), unix(7) |
| MCP | github.com/modelcontextprotocol/go-sdk |
| Methods | grill-me / grill-with-docs: github.com/mattpocock/skills |
