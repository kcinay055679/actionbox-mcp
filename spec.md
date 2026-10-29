# actionbox: Design & Implementation Spec

- **Status:** Design complete. Remaining open items are verification only (§11).
- **Date:** 2026-10-28
- **Owner:** Yanick Minder
- **Origin:**
  - Requirements session (grill-me method, Q1–Q21)
  - Refinement session (grill-with-docs, R1–R24)
  - Prior-art review (R25–R29)
  - Practice review (R30–R36)
- **Related:**
  - [README.md](README.md): user-facing contract
  - [GLOSSARY.md](GLOSSARY.md): terms
  - [docs/adr/](docs/adr/): ADR 0001 (single engine), ADR 0002 (trust check)
  - [docs/research/prior-art.md](docs/research/prior-art.md): existing tools
  - [docs/research/practice.md](docs/research/practice.md): how people run agents in practice, known attacks

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
| bubblewrap (host package) | 0.6.1: lacks the symlink-escape fix (GHSA-pxhw-h44j-8pfx, fixed in 0.12.0) and `--bind-fd` (0.10.0) | [obs-H], [doc] bwrap NEWS |
| bubblewrap (required, R25) | ≥ 0.13.1, path set in `config.toml` (`bwrap_path`); host package stays untouched | [doc] bwrap NEWS |
| Maven (required, R28) | ≥ 3.9.0 for `maven.repo.local.tail` | [src] maven `DefaultRepositorySystemSessionFactory` |
| npm (fetch guards, R29) | ≥ 11.14 on the host (`min-release-age` since 11.10, `allow-*` since 11.14); 12.x defaults `allow-git` / `allow-remote` to `none` | [src] npm/cli v12.2.1 `definitions.js` |
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
| R7 | Symlinks (was DECIDE-2) | `lstat` check before start **plus** a dev/inode check inside the sandbox after mounting. Primary mechanism replaced by **R26**; inode check stays as backstop | Agent can swap a dir for a symlink between check and mount | `lstat` only |
| R8 | Shim registration (was DECIDE-5) | User scope (`claude mcp add --scope user`); the shim holds no trust | Agent can reach `broker.sock` directly anyway | Treating `.mcp.json` as a control |
| R9 | Vocabulary | GLOSSARY.md; "allowlist" and "sandbox profile" avoided | Overloaded terms | — |
| R10 | Docker API version | ~~Container jobs pin docker-java `api.version=1.40`~~ superseded by **R27** | Testcontainers ≥ 2.0.2 sends 1.44; Podman 3.4 implements 1.40 but doesn't enforce the version [src] | Unpinned |
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

### 5.3 Prior-art review (R25–R29)

| # | Question | Decision | Why | Rejected |
|---|---|---|---|---|
| R25 | bwrap version | Require bwrap ≥ 0.13.1, configured via `bwrap_path`. nixpkgs ships 0.12.0 (2026-10), so install via a Nix `overrideAttrs` pinned to the v0.13.1 rev, or a meson build (see [spikes/](spikes/)) | 0.6.1 lacks the 0.12.0 symlink-escape fix in the core of the engine | Host package 0.6.1 |
| R26 | Mount race (R7) | Broker opens each bound project path `O_PATH\|O_NOFOLLOW` (via `openat2` `RESOLVE_NO_SYMLINKS` from the repo dirfd), checks the fd, passes it with `--bind-fd` / `--ro-bind-fd`; post-mount inode check stays as backstop | Removes the check/mount race at the source; same pattern Flatpak adopted for CVE-2024-42472 | Inode check only |
| R27 | Docker API pin | No pin by default; Testcontainers ≥ 2.0.3 falls back to 1.32 itself. Add `api.version=1.40` only if A2 fails | Avoid config that may be unnecessary | Pin 1.40 (R10) |
| R28 | Project cache in jobs | Maven: project cache mounted **read-only** as `maven.repo.local.tail`, writable per-job head in the job dir. npm: cache stays writable, behavior observed (O11) | Jobs can't poison the Maven cache; npm cache gives "no guarantee" of keeping entries [doc] | Writable Maven cache; verdaccio now |
| R29 | Fetch guards | Each fetch records the sha256 of every fetched file next to its coordinate in a per-project fetch ledger; npm fetches set `min-release-age` and keep `allow-git` / `allow-remote` at `none` | Ledger makes the cache auditable (Nix fixed-output idea); cheap npm supply-chain guards | No record |

### 5.4 Practice review (R30–R36)

Source: [docs/research/practice.md](docs/research/practice.md). Most of its claims come from search-engine summaries; re-check before citing.

| # | Question | Decision | Why | Rejected |
|---|---|---|---|---|
| R30 | What the broker reads | **Invariant:** broker and fetch read only the verified definitions blob, the owner's config and owner files under `$HOME` (`settings.xml`, `.npmrc` for credentials). Never any agent-writable project file (`.mvn/`, `.npmrc`, `package.json`, lockfiles, …) | 2026 attacks hand off agent-written files to a process outside the sandbox | — |
| R31 | State shared between jobs | Jobs never write state a later job relies on. npm cache read-only (closes O11's write half). Container store mounted as an overlay: project store read-only lower, `$JOB` upper, discarded after the job. Fallback if the overlay fails (O13): writable store, plus an image-digest comparison after every job | Clinejection (2026-03) poisoned a shared cache to reach a privileged later run | Writable shared caches |
| R32 | Agent-written files run outside actionbox | Listed explicitly in README and §10; §8.10 recommends Fence write-denies for them | They are the main remaining escape path | Leave implicit |
| R33 | Audit trail | Append-only, hash-chained event log of every job and fetch decision | Cheap; needed after an incident | No log |
| R34 | Fetch hardening | Fetch only from registries in config; flag unusual coordinates at the y/N prompt; nothing executes during fetch; minimal environment | Fetch is the only network path left | — |
| R35 | Combined MCP servers | Residual risk: job output plus an outward-writing MCP server (GitHub, Slack) can leak data; don't combine them in one session | GitHub MCP attack (2025-05) | — |
| R36 | Secrets in the agent's environment | `conformance` warns about token- or key-like variables in the shim's environment | Cheap check of the agent-side setup | Hard failure |

Not adopted: secret scanning of job output (jobs hold no secrets) and prompt-injection filtering (detection isn't a security boundary).

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
| E17 | `spikes/o10-fd-binds.sh` (2026-10-29): Podman 3.4.4, bwrap 0.13.1 (Nix override), kernel 7.1.1, pause process running | [obs-H] | 7/7 PASS: `O_RDONLY` and `O_PATH` fds survive `podman unshare`; `--bind-fd` / `--ro-bind-fd` work through it; fd not leaked into the sandbox; after a dir→symlink swap the write lands in the original dir, while a path-based `--bind` follows the symlink into the victim dir |

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
| `~/.local/share/actionbox/projects/<name>/m2/` | Maven project cache (read-only in jobs, written by fetches only) |
| `~/.local/share/actionbox/projects/<name>/ledger.jsonl` | Fetch ledger: one line per fetched file, `{request_id, ecosystem, coordinate, path, integrity, size, fetched_at}`; `integrity` is an SRI string (`sha256-…` for Maven files and images, npm's own `sha512-…`) |
| `~/.local/share/actionbox/projects/<name>/npm/` | npm project cache |
| `~/.local/share/actionbox/projects/<name>/containers/{store,run}/` | Podman graphroot + runroot (always used as a pair) |
| `~/.local/share/actionbox/projects/<name>/lock` | `flock` target: one job or fetch at a time per project |
| `~/.local/state/actionbox/broker.sock` | Socket (agent needs connect only) |
| `~/.local/state/actionbox/events.jsonl` | Event log (R33): append-only, one JSON object per line, each with `prev` = sha256 of the previous line |
| `~/.local/state/actionbox/trust/<name>.json` | Trusted commit (rollback guard) |
| `~/.local/state/actionbox/fetch/<request_id>.json` | Fetch requests |
| `~/.local/state/actionbox/jobs/<name>/<job>/` | `log.txt` (agent needs read), `job.json` (broker: action, params, state, exit code; survives broker restarts), `meta.json` (engine: bind `(dev, inode)` list, podman socket), `xdg/`, `tmp/`, `home/`, `ovl/` |

`config.toml` sketch:

```toml
pinned_fingerprint = "<40 hex, primary key>"
bwrap_path = "/home/yminder/.nix-profile/bin/bwrap"   # ≥ 0.13.1 (R25)
npm_min_release_age = 7                               # integer days, fetch only (R29); validated by actionbox, npm ignores bad values

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

[fetch]             # R34
npm_registry    = "https://registry.npmjs.org/"   # default; https only, no credentials in the URL
image_registries = ["docker.io", "ghcr.io"]      # default empty: image fetches fail closed
env_passthrough = []    # extra variables a fetch may see; default none

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

**Symlinks (R7, R26)**
- The broker opens the project root, then each writes dir and each existing protected path relative to it, with `openat2(…, O_PATH|O_NOFOLLOW, RESOLVE_NO_SYMLINKS)`. Any symlink component fails the job before start.
- It creates missing writes dirs the same way (`mkdirat` on the parent fd).
- bwrap binds **from those fds** (`--bind-fd FD DEST`, `--ro-bind-fd FD DEST`). It mounts via `/proc/self/fd/N`, then compares the fd's `(dev, inode)` with the mounted dest and dies with "Race condition binding dirfd" on a mismatch. It closes the fd afterwards, so it doesn't leak into the job [src] bubblewrap v0.13.1 `bubblewrap.c` L1150–1308. A path swapped after the check is therefore never mounted.
- Backstop: each fd's `(dev, inode)` is recorded in `meta.json`. After mounting, `job-init` re-stats each mount point and aborts on a mismatch. This duplicates bwrap's own check and stays because it's cheap.

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
GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
GIT_ATTR_NOSYSTEM=1 git -C "$REPO" -c core.fsmonitor=false -c core.hooksPath=/dev/null ...
```

- `GIT_NO_REPLACE_OBJECTS=1` does **not** disable grafts; `GIT_GRAFT_FILE=/dev/null` does (trust-attacks b, c).
- Nothing git returns is trusted until re-hashed.
- Each failure has a fixed reason string (in quotes below). The fixture manifest `testdata/trust/cases.json` uses the same strings.

**Steps:**

1. **Preconditions.**
   - `.git/info/grafts` exists → "grafts present".
   - The repo uses the SHA-256 object format → "unsupported object format". That format isn't supported yet; fail closed.
2. **Candidate.** `C = git rev-list -1 HEAD -- .agent-actions.toml`.
   - This is a hint only. Shallow, commit-graph and forged objects can steer it, which is harmless because `C` is fully verified below.
   - No commit touches the file → "missing definitions".
3. **Commit object.** Read `git cat-file commit C`.
   - Check that `sha1("commit <len>\0" + body) == C` → else "hash mismatch".
   - Remove exactly the header named `gpgsig`, including its continuation lines, to get the signed payload.
   - Fail with "signature status" if there is no `gpgsig` header, more than one, or the signature isn't OpenPGP-armored. SSH and X.509 signatures are never handed to any program.
4. **Signature.** Run `gpgv --keyring <absolute KEYRING path> --status-fd 1 sig payload`. Require all of:
   - exit code 0,
   - exactly one `NEWSIG`, one `GOODSIG` and one `VALIDSIG`,
   - `VALIDSIG` field 10 (primary-key fingerprint) == `PIN`, compared case-insensitively. Field 1 is the signing (sub)key.

   "foreign key" is checked first: any `NO_PUBKEY`, or any `VALIDSIG` whose field 10 != `PIN`. This covers a signature by an unknown key (gpgv exits 2 with `ERRSIG` + `NO_PUBKEY` and no `VALIDSIG`), and an owner signature combined with a foreign one.
   - none of `BADSIG`, `ERRSIG`, `EXPSIG`, `EXPKEYSIG`, `REVKEYSIG`, `KEYEXPIRED`, `KEYREVOKED`, `NO_PUBKEY`.

   Any other violation → "signature status". Expired and revoked keys still exit 0 with a `VALIDSIG` line; only `EXPKEYSIG` / `REVKEYSIG` reveal them (trust-attacks g). `gpgv` reads no `gpg.*` git config and no agent sockets, and `--keyring` doesn't also load `trustedkeys.gpg`.
5. **Tree.** Take the `tree` ID from the verified commit body.
   - Read it with `git cat-file tree` and re-hash it → else "hash mismatch".
   - Find the root entry `.agent-actions.toml`. Missing → "missing definitions".
   - Mode `120000` → "symlink". Any other mode than `100644` → "file mode".
6. **Blob.** Read the blob with `git cat-file blob` and re-hash it → else "hash mismatch".
7. **Working file.** Open `$REPO/.agent-actions.toml` with `O_NOFOLLOW|O_NONBLOCK` (a FIFO must not block). A symlink → "symlink"; not a regular file → "file mode"; missing → "uncommitted edits".
   - Require its bytes to equal the blob byte-for-byte → else "uncommitted edits".
8. **Rollback (R6).** If `TRUSTED` is set and `C != TRUSTED`:
   - Walk parents from `C` using the parent IDs inside each re-hashed commit body, read with `cat-file`. Never use `rev-list`, which follows forged parents, grafts and `.git/shallow`.
   - A re-hash failure → "hash mismatch".
   - Stop when `TRUSTED` is found (pass), or fail with "rollback" after 10,000 commits or at the roots.
9. **Parse.** Parse **the verified blob**, never the working file, and validate it against §8.3 → else "invalid definitions".
10. **Record.** Store `C` as `TRUSTED`.

Notes:

- `verify --accept <commit>` runs steps 1 and 3–7 for `<commit>` and sets it as `TRUSTED` without the ancestry check.
- In the agent's Fence, `.git/config` and `.git/hooks` are already write-protected (`allowGitConfig` false by default) [src] fence v0.1.67 `internal/sandbox/linux.go`. The `-c` overrides are defense in depth.
- `.git/objects`, `.git/refs`, `.gitattributes` and `.git/info/*` stay agent-writable. That's why steps 3–7 re-hash and avoid porcelain.
- SHA-1 collision resistance is assumed. This is a residual risk (§10).
- Every attack behind this design was reproduced on git 2.55.0 / gpg 2.2.27; a throwaway reference implementation of the steps above got the expected result on all fixture cases. See [docs/research/trust-attacks.md](docs/research/trust-attacks.md).

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
   | `PATH` | `[toolchain].path` first, then `/usr/local/bin:/usr/bin:/bin`, so toolchain `mvn`/`java` win over system ones |
   | `HOME` | `$JOB/home` |
   | `LANG` | `C.UTF-8` |
   | `TERM` | `dumb` |
   | `TMPDIR` | `/tmp` |
   | `MAVEN_OPTS` | `-Dmaven.repo.local=$JOB/m2 -Dmaven.repo.local.tail=<cache>/m2` (R28) |
   | `npm_config_cache` | `<cache>/npm` [doc] npm config |
   | `[toolchain].env` | e.g. `JAVA_HOME` |
   | Container jobs only | `XDG_RUNTIME_DIR`, `CONTAINERS_CONF`, `CONTAINERS_STORAGE_CONF`, `DOCKER_HOST`, `TESTCONTAINERS_RYUK_DISABLED=true` |

   | npm extras | `npm_config_logs_dir=$JOB/tmp`, `npm_config_update_notifier=false` (read-only cache, R31) |

6. **Event log (R33).** Append to `events.jsonl` before and after each job: `{ts, kind: "job_start"|"job_end", project, job_id, action, params, definitions_commit, state, exit_code, prev}`. Fetch decisions are logged the same way (§8.9).
   - Writes are serialized with a `flock` on the log file.
   - `prev` is the sha256 of the previous line's bytes; the first line uses 64 zeros.
   - `actionbox verify --log` re-checks the chain.
   - Limits of a plain hash chain:
     - An edited line N shows up as a `prev` mismatch on line N+1.
     - An edited or deleted **last** line can't be detected. `serve` therefore prints the chain head (sha256 of the last line) when it stops, so the owner has an outside anchor.
   - A log whose last line is truncated (no trailing newline) refuses further appends instead of chaining onto the damage. Logging stops until the owner repairs the file.
7. **Retention (R20).** After each job, and at `serve` startup, prune `jobs/<name>/`. Keep the newest `jobs_per_project` and drop anything older than `max_age`. The event log is never pruned.

### 8.8 Engine (R3, R13, R14, R25, R26, ADR 0001)

```bash
podman unshare -- "$BWRAP_PATH" \
  --ro-bind / / \
  --dev /dev --dev-bind /dev/net/tun /dev/net/tun --proc /proc \
  --tmpfs /run --tmpfs /tmp --tmpfs /var/tmp --tmpfs /dev/shm \
  --tmpfs /media --tmpfs /mnt --tmpfs "$HOME" \
  <if it exists on the host: --tmpfs /nix/var/nix/daemon-socket> \
  --unshare-net --unshare-pid --unshare-ipc --new-session --die-with-parent \
  --clearenv <--setenv for each §8.7 variable> \
  <for each toolchain ro_path T: --ro-bind T T> \
  --ro-bind <actionbox binary> <actionbox binary> \
  --ro-bind-fd $FD_REPO "$REPO" \
  <for each writes dir D: --bind-fd $FD_D "$REPO/D"> \
  <for each existing protected path P (root + depth ≤ 3 in writes): --ro-bind-fd $FD_P "$REPO/P"> \
  --ro-bind "$CACHE/m2" "$CACHE/m2" \
  --ro-bind "$CACHE/npm" "$CACHE/npm" \
  <containers only: --overlay-src "$CACHE/containers" \
                    --overlay "$JOB/ovl/upper" "$JOB/ovl/work" "$CACHE/containers"> \
  --bind "$JOB" "$JOB" \
  --chdir "$REPO" \
  -- actionbox job-init --meta "$JOB/meta.json" -- <argv>
```

**Mounts**
- `--ro-bind / /` replaces `--dev-bind / /` (R13). Persistence paths (`/var/tmp`, `/dev/shm`), external drives (`/media`, `/mnt`) and the Nix daemon socket are hidden behind tmpfs.
- **Order matters:** later mounts shadow earlier ones [doc] bwrap(1). Protected paths come **after** writes dirs.
- `/dev/net/tun` is needed by `slirp4netns` for container networking inside the sandbox. ⚠ O3
- The broker opens the fds and passes them through `podman unshare` to bwrap; they survive (E17). Not covered by E17: the first `podman unshare` without a running pause process (re-exec path), which source reading says keeps fds too; conformance runs exercise it.
- The project cache's `m2/` is read-only; a job's resolved artifacts land in `$JOB/m2` and are discarded with the job dir (R28).
- The npm cache is read-only; `npm ci --offline` reads tarballs by integrity and needs no writes (docs/research/npm-fetch.md). Jobs must use `npm ci`, not `npm install`, which appends index lines.
- The container store (graphroot + runroot) is an overlay (R31, bwrap ≥ 0.11 `--overlay`): the project store is the read-only lower layer, writes land in `$JOB/ovl/upper` and are discarded with the job dir. Whether Podman's vfs driver and its locks work on overlayfs inside `podman unshare` is unverified. ⚠ O13. Fallback: plain `--bind` of the store, plus a digest comparison of all images (`podman images --digests`) before and after the job; any change fails the job and is logged.

- The actionbox binary is re-bound read-only: under the `$HOME` tmpfs it would otherwise be hidden and `job-init` couldn't start.
- `--tmpfs` over a path that doesn't exist fails on a read-only root, so the Nix socket dir is only hidden when present.
- The engine creates missing project cache dirs (`m2/`, `npm/`, `containers/{store,run}`) before binding; `--ro-bind` fails on a missing source.
- Container env: `XDG_RUNTIME_DIR=$JOB/xdg`, `CONTAINERS_CONF=$JOB/containers.conf`, `CONTAINERS_STORAGE_CONF=$JOB/storage.conf`, `DOCKER_HOST=unix://$JOB/xdg/podman.sock`. The overlay target is the project's `containers/` dir, so graphroot and runroot keep their host paths.
- The outer commands (`systemd-run`, `podman unshare`) get only `PATH`, `HOME`, `USER`, `LOGNAME`, `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS` from the broker's environment.
- **Exit mapping:** `timeout`'s 124 → `timed_out`. 137 counts as `timed_out` only once the timeout has passed (kill-after); otherwise it and other signal exits → `killed`. `exit_code` is reported only for `succeeded`/`failed`. `job-init` exits 125 on a mount mismatch, with its message in the log.

**`job-init`**
1. Re-stats every recorded mount point and aborts on a `(dev, inode)` mismatch (R7).
2. If `containers = true`:
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
- Not yet verified: the R13 root layout, `--unshare-pid/ipc`, `--new-session`, `--clearenv`, toolchain binds, the API service under Podman 3.4, non-container jobs under `podman unshare`, and bwrap 0.13.1 beyond `--bind-fd` (E17). ⚠ O3, covered by §9.

### 8.9 Coordinate fetch (owner-only, R11, R17)

Fetches run on the host, in an empty `mktemp -d` cwd, with the owner's normal credentials and **no repo files involved** (R30).

**Hardening (R34)**
- **Registries:**
  - npm: `--registry` from `[fetch].npm_registry`.
  - Images: the registry host must be in `[fetch].image_registries`.
  - Maven: the repositories from the owner's `settings.xml`, an owner file.
- **Nothing executes:**
  - `npm cache add` runs no package scripts.
  - Maven runs only the pinned `dependency:get` goal in an empty directory, with no project and no extensions.
  - `podman pull` runs no image content.
- **Environment:** fetch processes start from an empty environment plus `PATH`, `HOME`, `LANG` and `[fetch].env_passthrough`.
- **Unusual coordinates** are flagged next to the y/N prompt but don't block. They are:
  - a groupId, npm scope or image repository never fetched for this project (per `ledger.jsonl`),
  - a name longer than 50 characters or mostly digits/random-looking,
  - an npm version younger than twice `npm_min_release_age`.

| Ecosystem | Accepted format (anchored regex) | Command |
|---|---|---|
| Maven | See below | `mvn -B -ntp org.apache.maven.plugins:maven-dependency-plugin:3.11.0:get -Dartifact=<coord> -Dmaven.repo.local=<cache>/m2` |
| npm | See below | `npm cache add <spec> --cache <cache>/npm --min-release-age=<days> --allow-git=none --allow-remote=none --allow-file=none --allow-directory=none --logs-dir=<fetch tmp> --no-update-notifier` |
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
  4. runs argv arrays, never a shell, and logs the decision and result to `events.jsonl` (R33),
  5. appends every file the fetch added to the project cache, with its integrity, to `ledger.jsonl` (R29):
     - Maven: sha256 of the files under `m2/` that changed during the fetch (snapshot of the tree before and after),
     - npm: `integrity` and `size` of the `pacote:tarball:<spec>` entry among the new `_cacache/index-v5` lines (before/after snapshot); `npm cache ls` prints keys only,
     - image: the manifest digest, which is already part of the coordinate.

Expect iterative round trips: an offline build fails → the agent requests the missing artifact → the owner fetches → the agent retries.

### 8.10 Agent-side requirements (agent's Fence config)

- The broker socket lives under `~/.local/state/actionbox/`. Connecting through Fence's read-only mount works (E13 control).
- None of the §8.2 paths may be in the agent's `allowWrite`.
- Keep `allowGitConfig` unset (false). This keeps `.git/config` write-protected for the agent [src].
- No `allowUnixSockets` needed; it's a no-op on Linux (E11).
- The agent's Fence must be able to read `~/.local/state/actionbox/jobs/` for `log_path`.
- **Recommended write-denies (R32).** These files are written by the agent but run by something outside any sandbox. Fence already protects some by default (`.vscode`, `.idea`, `.git/hooks`, `.git/config`, `.mcp.json`, shell rc files). Add the rest to `denyWrite`:
  - `.envrc` (direnv),
  - `mise.toml`, `.mise.toml`, `.tool-versions` (mise),
  - `.mvn/`, `mvnw`, `mvnw.cmd` (Maven extensions and wrapper, run on IDE import),
  - `.claude/` (Claude Code settings and hooks),
  - `.github/workflows/` (CI with secrets), if the agent shouldn't change CI at all.
- **One outward-writing MCP server per session at most (R35):** don't give the same agent session actionbox together with an MCP server that can write outside, such as GitHub or Slack.

## 9. Conformance & acceptance tests

`actionbox conformance` runs P1–P16 in the engine twice, once with `containers = false` and once with `containers = true`. Run it before first use and after any Podman, bwrap or kernel upgrade.

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
| P9 | Harness swaps a writes dir for a symlink to `$HOME` between broker check and mount | The original dir is mounted (fd); `$HOME` is never writable |
| P13 | Job writes into the project cache's `m2/` | Fail; the artifact lands in `$JOB/m2` |
| P14 | Job writes into the project cache's `npm/` | Fail |
| P15 | Container job pulls/tags/removes images and writes container layers | Project store byte-identical after the job (overlay) or job fails on digest change (fallback) |
| P16 | Environment of the shim (run from the agent's Fence) | Warning for token- or key-like variable names (`*TOKEN*`, `*SECRET*`, `*KEY*`, `AWS_*`, `GH_*`); not a failure (R36) |
| P10 | Environment dump | Only the §8.7 variables; a harness-set `CANARY_TOKEN` absent |
| P11 | Write `/var/tmp/x`, `/dev/shm/x`; list `/media`, `/mnt` | Writes don't persist after the job; mounts empty |
| P12 | Resource limits: memory hog, fork bomb, sleep past timeout | `killed` / `timed_out`, host unaffected |
| A1 | Containers: Postgres via Podman CLI (`pg-spike.sh`) | Ready + port reachable ✅ [obs-H] (old layout; re-run on §8.8) |
| A2 | Containers: a real Testcontainers 2.x test from a project, no API pin (R27) | Passes offline; if not, retry with `api.version=1.40` |
| A3 | Trust: the cases below | Only signed, genuine, clean, descendant passes |
| A4 | Params: leading `-` without `allow_leading_dash`; unknown param; NUL; 257 bytes | Rejected, nothing runs |
| A5 | Fetch with the project directory unreadable (`chmod 000`) | Fetch succeeds: proves R30 |
| A6 | Tamper with one line of `events.jsonl` | `actionbox verify --log` reports the first broken line |

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
- the definitions file as a symlink,
- an expired key, a revoked key,
- an SSH-armored signature with a repo `gpg.ssh.program`,
- a SHA-256 repo,
- the definitions file with mode `100755`.

`testdata/trust/gen-fixtures.sh OUTDIR` builds all of these repos (23 cases, needs git, gpg ≥ 2.2 and python3). It writes `cases.json` with the expected result, reason string, pin, keyring, trusted commit and, where relevant, a marker file that must not exist after verification.

## 10. Residual risks

- **Agent-written code run outside actionbox** (IntelliJ Maven import, `mvnw`, the owner's own runs) executes with full privileges. Review is the only control.
- **Build outputs** (jars, images) produced by jobs are untrusted until the owner rebuilds them.
- **Prompt injection through output**: capped, not prevented. The agent already reads untrusted repo content.
- **Kernel attack surface**: unprivileged user namespaces (bwrap, `podman unshare`).
- **Cross-project calls (R4)**: any agent can run agent-invokable actions of any registered project.
- **Handoff files (R32)**: files the agent writes and something outside a sandbox later runs. Examples: `.vscode/`, `.idea/`, `.envrc`, `mise.toml`, `.mvn/extensions.xml`, `mvnw`, `.git/hooks`, `.git/config`, `.claude/`, CI workflows, and the build itself on IDE Maven/Gradle import. Mitigated only by the agent's Fence write-denies (§8.10) and owner review. Practice shows this is the main 2026 escape class (docs/research/practice.md).
- **Combined MCP servers (R35)**: job output read by the agent, plus another MCP server that can write outside (GitHub, Slack), is a way to leak data.
- **Live repo (R16)**: a job may see a mix of two states of the code if the agent edits during the run. This affects correctness only, not isolation.
- **SHA-1**: trust relies on SHA-1 collision resistance of the object IDs referenced from the signed commit.
- **Threat C**: a malicious dependency fetched by coordinates runs inside the job only, which is the intended containment. If the owner uses the same dependency in normal builds, that's ordinary supply-chain risk.

## 11. Open items (verification only)

| ID | Item | How to close |
|---|---|---|
| O2 | Testcontainers 2.x ↔ Podman 3.4 compat API, unpinned (R27) | A2 with a real project |
| O3 | R13 root layout, `--unshare-pid/ipc`, `--clearenv`, `/dev/net/tun` bind, non-container jobs under `podman unshare`, bwrap 0.13.1 | Conformance P1–P16 + A1 re-run |
| O5 | Go implementation of §8.4 against the A3 fixtures. The attacks themselves are reproduced and the algorithm passed with a throwaway reference implementation ([trust-attacks.md](docs/research/trust-attacks.md)) | A3 in Go (M1) |
| O8 | Shared rootless pause process between host fetches and jobs | Observe during A1/A2; [inf] harmless, since bwrap adds its own mount namespace |
| O11 | npm cache as the store: entries evicted (writes are settled, R31) | Observe during M4; move to verdaccio or a tarball dir if it bites |
| O13 | Container store as bwrap overlay under `podman unshare`: vfs driver, locks, runroot on overlayfs; unprivileged overlay needs kernel ≥ 5.11 | P15; fallback digest check (§8.8) |

Closed in the refinement session:

| ID | Item | Resolution |
|---|---|---|
| O1 | Fence's dangerous-file list | R12 |
| O4 | Host Fence version | 0.1.67, E15 |
| O6 | `containers.conf` key names | E15 |
| O7 | maven-dependency-plugin version | R11 |
| O9 | DECIDE-1…6 | R4, R7, R5, R6, R8, and R3 (DECIDE-6 moot) |
| O10 | fds through `podman unshare` into bwrap (R26) | E17: all checks pass, no `engine-exec` fallback needed |
| O12 | npm flag names and units | [docs/research/npm-fetch.md](docs/research/npm-fetch.md): `cache add` honors all guards; `min-release-age` is integer days |

## 12. Runbook

- **Install bwrap ≥ 0.13.1:** build it with the Nix override from `spikes/o10-fd-binds.sh` into a persistent out-link, `nix build --impure --out-link ~/.local/share/actionbox/bwrap --expr '…'`, and set `bwrap_path = "~/.local/share/actionbox/bwrap/bin/bwrap"` (expanded). The spike's own out-link lives in its temp dir and is garbage-collected later.
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

1. **M1 Trust:** §8.4 verifier + `verify` CLI; A3 green. ✅ 2026-10-29: `internal/trust`, `internal/verify`, `internal/definitions`, `internal/config`, `actionbox verify`; all 23 A3 fixture cases pass.
2. **M2 Engine without containers:**
   - `serve`, socket protocol, shim;
   - `list_actions` / `run_action` / `job_status`;
   - `job-init` inode check, env allowlist, limits, lock;
   - bwrap ≥ 0.13.1, fd binds;
   - event log (R33);
   - P1–P16 green with `containers = false`; A4, A6 green.
   - 🟡 2026-10-29: code done (`internal/{protocol,job,client,shim,broker,engine,eventlog}`, `serve`, `mcp`, `job-init`, `verify --log`); unit tests green, `serve` + `mcp` smoke-tested in the sandbox (list, trust rejection, unknown action, event log). Open: `actionbox conformance` (P1–P16) and a first real job on the host.
3. **M3 Containers:** Podman service in `job-init`, store overlay (O13); P1–P16 green with `containers = true`; A1 + A2 green.
4. **M4 Fetch:** `request_fetch`, `fetch_status`, `actionbox fetch`, fetch ledger, npm guards, registry allowlist and coordinate flags (R34); A5 green.
5. **M5 Ops:** retention, event display, launcher integration (Q18).

## 14. References

| Topic | Sources |
|---|---|
| Fence | github.com/fencesandbox/fence (formerly Use-Tusk/fence), v0.1.67: `internal/sandbox/dangerous.go`, `internal/sandbox/linux.go`, `internal/config/config.go`, `docs/configuration.md` |
| bubblewrap | man bwrap(1); NEWS (`--bind-fd` 0.10.0, overlays 0.11.0, GHSA-pxhw-h44j-8pfx fixed 0.12.0, 0.13.1 2026-10-14) |
| Maven | `maven.repo.local.tail` (3.9.0+), maven-resolver `local-repository.md` |
| npm | npm/cli `definitions.js` (`min-release-age`, `allow-git`, `allow-remote`), `npm-cache.md` |
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
