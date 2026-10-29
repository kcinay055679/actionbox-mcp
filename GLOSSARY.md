# actionbox

Lets a sandboxed coding agent run a fixed, owner-signed set of build and test commands on the host, each inside its own isolated sandbox.

## Language

### Trust

**Owner**:
The single person who signs definitions and approves fetches. The only party actions are trusted by.
_Avoid_: user (ambiguous with the agent's user), admin

**Pinned key**:
The owner's GPG key whose signature makes definitions trusted.

**Definitions file**:
The `.agent-actions.toml` in a project; the source of all actions.
_Avoid_: config, manifest, mise tasks

**Trusted commit**:
The most recent commit whose definitions file passed verification; later definitions must descend from it.
_Avoid_: LAST, accepted commit

### Projects and actions

**Project**:
A repository the owner registered with the broker. Unregistered repositories are never served.
_Avoid_: allowlisted repo, workspace

**Action**:
A named, signed command with typed parameters, declared in a definitions file.
_Avoid_: task, sandbox profile, allowlist entry

**Invoker**:
Who may start an action: the agent or only the owner.

**Writes dir**:
A project-relative directory an action may modify; everything else in the project is read-only to it.

**Protected path**:
A project path that is never writable by any action, even inside a writes dir.

### Execution

**Job**:
One run of one action, with its own log, scratch space and resource limits.
_Avoid_: run, execution, task

**Engine**:
The sandbox that isolates a job from secrets, network and host persistence paths.
_Avoid_: runner, profile

**Broker**:
The host process that verifies trust, runs jobs and records fetch requests. The only component that enforces anything.
_Avoid_: server, daemon

**Shim**:
The MCP server inside the agent's sandbox that forwards tool calls to the broker. Holds no trust.
_Avoid_: client, proxy

### Dependencies

**Fetch request**:
The agent's request for specific dependency coordinates, waiting for the owner.

**Fetch**:
The owner reviewing and executing a fetch request on the host, filling the project's caches.
_Avoid_: download, install

**Project cache**:
Per-project dependency and image storage that fetches fill and jobs only read, never the owner's normal builds.

### Audit and risk

**Event log**:
The tamper-evident record of every job and every fetch decision.
_Avoid_: audit trail, history

**Handoff file**:
A file the agent can write that something outside any sandbox later runs, such as IDE config, `.envrc` or `mvnw`.
_Avoid_: dangerous file
