# actionbox

actionbox lets a sandboxed coding agent run a fixed set of build and test commands, such as `mvn test` or a linter, that the owner has defined and signed. Each run is isolated from secrets, the network and the rest of the host. The agent gets the tools it needs without the agent's sandbox being weakened.

The agent cannot invent commands. It picks an action from a list, supplies typed parameters, and receives the result.

## How it fits together

```text
 Owner ──signs──► .agent-actions.toml (in the repo)
                          │ verified on every request
 Agent ──MCP──► Shim ──► Broker ──► Job (isolated sandbox) ──► result
                          ▲
 Owner ──CLI (fetch, run, verify)
```

1. The agent calls an MCP tool on the **shim**.
2. The shim forwards the call to the **broker** on the host.
3. The broker verifies the signature on the project's **definitions file**.
4. The broker runs the requested **action** as a **job** in its own sandbox.
5. Dependencies enter only through **fetches** that the owner approves.

## Concepts

### Owner and agent

The **owner** is the single person who defines actions, signs them and approves fetches. The **agent** is the coding agent running in its own sandbox. It can list and run actions and request dependencies, and nothing more.

### Project

A repository registered in the broker's config. Unregistered repositories are never served. Any agent may call any registered project, since everything in it is signed by the owner.

### Definitions file

`.agent-actions.toml` in the project root declares all actions. It is trusted only if all of these hold:

- Its content is identical to the version in a commit signed by the owner's pinned key.
- That commit and its contents are genuine, not forged.
- That commit descends from the last trusted commit. This stops the agent from checking out an older version that still contains a since-removed action.

If any check fails, every request for the project fails and nothing runs.

To change actions: edit the file, then make a signed commit. Returning to older definitions requires an explicit `actionbox verify --accept <commit>`.

### Action

A named command with typed parameters. Each action declares:

- **Invoker:** who may start it, `agent` or `owner`.
- **Command:** an argv array, which may contain `{param}` placeholders.
- **Writes dirs:** the only project directories it may modify.
- **Containers:** whether it needs a container engine.
- **Limits:** optional timeout and memory limits, capped by the owner's config.

Parameter handling:

- Values must fully match their declared pattern.
- Values are never word-split or interpreted by a shell.
- An omitted optional parameter removes its whole argument.

### Job

A single run of a single action. Each project runs one job at a time; further jobs queue in arrival order. Every job has its own log and scratch directory, plus hard limits on time, memory, processes and CPU.

A job **can**:

- Read the project.
- Write to its writes dirs and the project cache.
- Start containers, if the action allows it. They are reachable on the job's own localhost, so Testcontainers works.

A job **cannot**:

- Reach any network, external or host localhost.
- Read the home directory, keys, credentials or the owner's own caches.
- Write protected paths, even inside a writes dir. Protected paths are `.git`, the definitions file, IDE, shell and tool config, and the Maven wrapper.
- See host sockets or the owner's environment variables.
- Leave anything behind outside its writes dirs, the project cache and its job dir.

Jobs see the project live, not a snapshot. If the agent edits files while a job is running, the job sees the edits.

### Fetch

Jobs run offline, so missing dependencies go through the owner:

1. The agent files a fetch request with exact coordinates.
2. The owner reviews the request.
3. The owner runs it on the host with their own credentials.
4. The result lands in the project cache.

No repository files are involved in a fetch.

Expect a few rounds: the build fails offline, the agent requests the missing artifact, the owner fetches it, and the agent retries.

### Project cache

Per-project storage for Maven artifacts, npm packages and container images. Only jobs and fetches use it, never the owner's normal builds, so a poisoned cache cannot spread.

### Broker and shim

The **broker** runs on the host in a terminal. It is the only component that enforces anything.

The **shim** is the MCP server inside the agent's sandbox. It only forwards calls and holds no trust. The agent could talk to the broker directly and would gain nothing.

## Agent API (MCP tools)

### `list_actions() → [{name, description, params}]`

Returns the project's actions that the agent may start. If the definitions file is untrusted, this returns an error, not an empty list.

### `run_action(name, params) → {job_id, state, exit_code?, output_tail?}`

Starts a job. It is rejected without running anything if any of these apply:

- The project is not registered.
- The definitions file is not trusted.
- The action is unknown, or only the owner may start it.
- A parameter is unknown, doesn't fully match its pattern, exceeds 256 bytes, contains NUL, or starts with `-` without permission.

The call waits up to 30 seconds. If the job hasn't finished by then, it returns `running` and the agent polls `job_status`.

### `job_status(job_id) → {state, exit_code, output_tail, log_path}`

- `state` is one of: `queued`, `running`, `succeeded`, `failed`, `timed_out`, `killed`, `rejected`.
- `output_tail` holds the last 200 lines, up to 16 KiB.
- The full log is readable at `log_path`.

Output is untrusted text: it is capped but not sanitized.

### `request_fetch(ecosystem, coordinates[]) → {request_id, state}`

`ecosystem` is `maven`, `npm` or `image`. Only exact coordinates are accepted:

| Ecosystem | Accepted format |
|---|---|
| Maven | `groupId:artifactId:version[:packaging[:classifier]]` |
| npm | Package name with an exact version |
| Image | Registry host plus an `@sha256` digest |

Version ranges, tags and URLs are rejected.

### `fetch_status(request_id) → {state}`

`state` is one of: `pending`, `fetched`, `rejected`, `expired`. A request expires after 24 hours.

## Owner CLI

| Command | Description |
|---|---|
| `actionbox serve` | Runs in the foreground and shows jobs and fetch requests. Ctrl-C revokes all agent capabilities. |
| `actionbox verify [project]` | Shows trust status. `--accept <commit>` overrides the rollback guard. |
| `actionbox run <action>` | Runs any action, including owner-only ones. Works without `serve`. |
| `actionbox fetch [id]` | Without an ID, lists pending requests. With an ID, shows every coordinate, asks for confirmation, and fetches. |
| `actionbox conformance` | Verifies the isolation guarantees on this machine. Run it after upgrading sandbox tooling. |

## Out of scope

- **Malicious dependencies:** they are contained within the job, not prevented.
- **Agent-written code run outside actionbox:** for example an IDE import or the owner's own builds. Code review is the only control there.
- **Build outputs:** jars and images are untrusted until the owner rebuilds them.
- **Prompt injection through job output:** output is capped, not filtered.
- **Multi-user setups.**
