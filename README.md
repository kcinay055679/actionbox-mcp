# actionbox

actionbox lets a sandboxed coding agent run a fixed set of build and test commands, such as `mvn test` or a linter, that you have defined and signed. Each run is isolated from your secrets, the network and the rest of your machine. The agent gets the tools it needs, and its own sandbox stays as strict as it is.

> **Status:** design phase. Nothing described here is implemented yet. [spec.md](spec.md) is the implementation spec.

## Why

Claude Code runs inside [Fence](https://github.com/fencesandbox/fence) with a defensive policy: no Docker, no Maven, no network beyond what's allowed. That stops it from running tests, builds or Testcontainers.

Loosening the sandbox would expose your secrets to anything the agent writes or gets talked into. actionbox keeps the sandbox closed and offers a small set of signed actions instead.

## What you need

| Requirement | Notes |
|---|---|
| Linux with cgroups v2 and unprivileged user namespaces | Tested on Pop!_OS 22.04 |
| systemd user session | Enforces resource limits per job |
| Podman (rootless) with a subuid/subgid range | Only for actions that start containers |
| bubblewrap ≥ 0.13.1 | Older versions lack a symlink-escape fix; install from Nix or build locally |
| GnuPG and a signing key | You sign the actions file with git |
| Maven ≥ 3.9 / npm 12 | Only if your actions use them |
| Claude Code inside Fence | Or any MCP client inside a sandbox that can reach a Unix socket |

## How you use it

### One-time setup

1. Install the `actionbox` binary.
2. Export your **public** GPG key into actionbox's keyring and set your key fingerprint in `~/.config/actionbox/config.toml`.
3. Register each project by name and path in the same config.
4. Tell actionbox where your toolchains live (JDK, Maven, Node).
5. Register the MCP server for Claude Code:

   ```bash
   claude mcp add --scope user actionbox -- actionbox mcp
   ```

6. Run `actionbox conformance` once to check the isolation guarantees on your machine.

### Define actions for a project

Add `.agent-actions.toml` to the project root and commit it **signed**:

```toml
schema_version = 1

[actions.test]
description = "Run backend tests, optionally one class"
invoker     = "agent"
containers  = true
argv        = ["mvn", "-o", "-B", "test", "-Dtest={test_class}"]
writes      = ["target"]
timeout     = "20m"
[actions.test.params.test_class]
pattern     = '[A-Za-z0-9_.#*]+'
optional    = true
```

```bash
git commit -S -m "add test action"
actionbox verify
```

To change actions, edit the file, make another signed commit, and run `actionbox verify`.

### Day to day

1. Start `actionbox serve` in a terminal **outside** the sandbox. It shows every job and dependency request. Pressing Ctrl-C takes away everything the agent can run.
2. Work with the agent as usual. It sees the actions as MCP tools and runs them itself.
3. When a build fails because a dependency is missing, the agent files a fetch request and `serve` shows it. You review and approve it:

   ```bash
   actionbox fetch            # list pending requests
   actionbox fetch <id>       # show coordinates, confirm, download
   ```

   The agent then retries.

4. After upgrading Podman, bubblewrap or the kernel, run `actionbox conformance` again.

## How it works

```text
 You ──signs──► .agent-actions.toml (in the repo)
                         │ verified on every request
 Agent ──MCP──► Shim ──► Broker ──► Job (isolated sandbox) ──► result
                         ▲
 You ──CLI (serve, fetch, run, verify)
```

1. The **agent** calls an MCP tool on the **shim**, a small MCP server that runs inside the agent's sandbox.
2. The shim forwards the call over a Unix socket to the **broker** on your host.
3. The broker checks that the project's **definitions file** matches a commit signed by your key.
4. It starts the requested **action** as a **job** in a fresh sandbox.
5. Dependencies enter only through **fetches** that you approve.

### Trust

The broker serves only projects you registered. A definitions file is trusted only if all of these hold:

- its content is identical to a commit signed by your key,
- that commit and its contents are genuine (the broker re-checks git's object hashes itself),
- that commit descends from the last trusted one.

The last check stops the agent from checking out an old version that still contains an action you removed. Going back on purpose, for example to an older branch, needs `actionbox verify --accept <commit>`.

If any check fails, nothing runs.

### Actions and parameters

An action is a command as a list of arguments, with typed parameters:

- Parameter values must fully match the pattern you declared.
- They can't start with `-` unless you allow it.
- They are never passed through a shell.

The agent can only pick an action and fill in its parameters; it can't change the command.

### Jobs and isolation

Each job runs in its own sandbox: a read-only view of the system, an empty home directory, no network and a clean environment. Each project runs one job at a time.

| A job can | A job can't |
|---|---|
| Read the project | Reach the network, external or your localhost |
| Write the directories the action declares | Read your home dir, keys, credentials or your own caches |
| Read the project's dependency cache | Write `.git`, the actions file, IDE, shell or tool config, even inside its writable dirs |
| Start containers (if the action allows it) that it reaches on its own localhost, so Testcontainers works | See host sockets or your environment variables |
| | Leave anything behind outside its writable dirs and job dir |
| | Exceed its time, memory, process or CPU limits |

Jobs see the project live, so if the agent edits files while a job is running, the job sees those edits.

### Dependencies

Jobs are offline. Each project has its own cache of Maven artifacts, npm packages and container images, separate from your normal `~/.m2` or `~/.npm`. Jobs can read the Maven cache but not write it.

The agent can only *request* dependencies, and only by exact coordinates: a Maven `g:a:v`, an npm package with an exact version, or an image with a digest. You approve each fetch. It runs on your host with your credentials, never touches repo files, and records a hash of every downloaded file.

## Reference

### MCP tools (what the agent sees)

| Tool | Purpose |
|---|---|
| `list_actions()` | Actions the agent may run, with their parameters |
| `run_action(name, params)` | Starts a job; waits up to 30 s, then returns a job ID to poll |
| `job_status(job_id)` | State, exit code, the last 200 lines of output, path to the full log |
| `request_fetch(ecosystem, coordinates)` | Asks you to fetch dependencies (`maven`, `npm`, `image`) |
| `fetch_status(request_id)` | `pending`, `fetched`, `rejected` or `expired` (after 24 h) |

### CLI (what you use)

| Command | Purpose |
|---|---|
| `actionbox serve` | Runs the broker in the foreground; Ctrl-C revokes everything |
| `actionbox verify [project] [--accept <commit>]` | Shows trust status or accepts an older definitions commit |
| `actionbox run <action>` | Runs any action yourself, including owner-only ones |
| `actionbox fetch [id]` | Lists or approves fetch requests |
| `actionbox conformance` | Checks the isolation guarantees on this machine |

## What actionbox does not protect against

- **Malicious dependencies:** they run inside the job, which contains them, but nothing prevents them.
- **Agent-written code you run outside actionbox:** for example an IDE Maven import or your own builds. Review is the only control there.
- **Build outputs:** jars and images built in jobs are untrusted until you rebuild them.
- **Prompt injection through job output:** output is capped, not filtered.
- **Multiple users:** actionbox is built for one person.

## Further reading

- [spec.md](spec.md): implementation spec, decision log, tests
- [GLOSSARY.md](GLOSSARY.md): terms
- [docs/adr/](docs/adr/): architecture decisions
- [docs/research/prior-art.md](docs/research/prior-art.md): related tools and what we reuse
