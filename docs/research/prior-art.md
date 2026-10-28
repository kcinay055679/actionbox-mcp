# Prior art for actionbox

- **Checked:** 2026-10-28.
- **Sources:** primary sources only: upstream repos, READMEs, docs source and man pages.
- **Access gaps:** a sandbox proxy blocked several doc sites. Where it did, the docs' source files in their GitHub or GitLab repos were read instead.
- **Tags:**
  - **(unverified)**: the claim's primary source was not read.
  - **(search-summary)**: the claim comes from a search engine's summary of the official page, not from the page itself.
- **Scope:** "No tool does X" is a negative finding from a limited search.

## Summary

**Closest matches. No single tool covers the whole problem.**

| Tool | What overlaps | Main gap |
|---|---|---|
| nono | Per-tool ephemeral sandbox; Sigstore-verified instruction files | Tools are agent CLIs, not owner-defined actions with typed params; offline jobs and nested podman not shown |
| Docker Sandboxes (`sbx`) | microVM per agent with a private Docker daemon, so Testcontainers works | Sandboxes the whole agent; network by domain policy, not offline; needs KVM |
| Codex CLI Linux sandbox | Same bwrap pattern: `--ro-bind / /`, writable roots, `.git` re-bound read-only | Sandboxes arbitrary agent commands; no action list |
| Sequoia git (`sq-git`) | Signed-commit authentication from a trust root, which acts as a rollback guard | Requires *every* commit on the path to be signed |
| `mise mcp`, task-mcp (just) | MCP tools that run predefined tasks; gated by `mise trust` or an allow-agent group | No sandbox; trust is a local hash or an in-file marker; shell execution |
| JFrog Curation waivers | A human approves package + version | Commercial network proxy; jobs would need network |

**What is novel in actionbox.** No existing tool was found that combines:

1. A per-repo action list, trusted through a signed commit with a pinned key and a rollback guard, re-verified on every request.
2. Regex-typed params substituted into argv with no shell.
3. Per-action writes dirs, with protected paths re-bound read-only.
4. Offline jobs that still run Testcontainers, through rootless podman nested in an unprivileged bwrap.
5. Agent-requested, owner-approved, coordinate-only fetches into per-project caches.

Each piece has precedent. The combination, and the split into an untrusted shim and an enforcing broker, does not.

**Reuse recommendations**

1. **Upgrade bwrap to ≥ 0.12, ideally 0.13.1.**
   - 0.12.0 fixes a symlink escape (GHSA-pxhw-h44j-8pfx). The target host has 0.6.1.
   - `--bind-fd` / `--ro-bind-fd` (0.10.0, backported to 0.6.3) can replace the R7 post-mount `(dev, inode)` recheck: open each path `O_PATH|O_NOFOLLOW`, check the fd, then pass the fd to bwrap.
   - `--size` caps the tmpfs mounts.
2. **Keep the hand-rolled engine.**
   - Fence, srt and Landlock all block nested rootless podman.
   - systemd `PrivateUsers` maps no subuid range.
   - Fallback engines, if ever needed: crun with an OCI spec (multi-range newuidmap) or nsjail (`-U`/`-G`).
3. **Re-test the docker-java `api.version=1.40` pin (R10).** Testcontainers ≥ 2.0.3 falls back from API 1.44 to 1.32 by itself, so the pin may be unnecessary (unverified on Podman 3.4).
4. **Maven cache layout.** `maven.repo.local.tail` (Maven ≥ 3.9.0) could serve the fetched cache read-only behind a per-job writable head. That would stop jobs from writing the project cache.
5. **npm cache.** npm documents its cache as "strictly a cache… no guarantee" that entries stay available. Consider storing tarballs from `npm pack` instead of relying on `npm cache add`.
6. **Host fetch guards.** npm 12 `min-release-age`, `allow-git=none`, `allow-remote=none` and Socket `sfw` can harden the host-side fetch cheaply.
7. **Trust check: keep the custom one (ADR 0002).**
   - `sq-git` requires every commit on the path to be signed by an authorized key.
   - actionbox lets the agent make unsigned commits, so `sq-git` would reject normal histories.
   - gittuf needs RSL refs and is beta.
8. **Param schema: keep the custom one.** just's `[arg(pattern=)]` is the closest precedent and also full-match anchored. Every task runner still goes through a shell.
9. **Isolation checklist.** Docker MCP Gateway's defaults are a useful one to compare against: `no-new-privileges`, CPU/memory caps, read-only binds under trusted roots, secret scanning of tool arguments and responses.

## 1. Agent sandboxes

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| Fence v0.1.67 | Wraps any command: bwrap + seccomp + Landlock; domain-allowlist proxy; `allowWrite`; dangerous-path protection | Nested podman: seccomp blocks `mount`/`pivot_root`, `unshare` is on the default deny list, and Fence can't nest itself. No action model | Keep as the agent-side jail (ADR 0001). Usable as a Go library, but it can't host podman | [seccomp](https://github.com/fencesandbox/fence/blob/main/internal/sandbox/linux_seccomp.go), [config](https://github.com/fencesandbox/fence/blob/main/docs/configuration.md), [troubleshooting](https://github.com/fencesandbox/fence/blob/main/docs/troubleshooting.md) |
| Anthropic srt v0.0.81 (beta) | bwrap + seccomp; netns removed, HTTP/SOCKS proxy with `allowedDomains`; mandatory write denies (`.idea`, `.vscode`, rc files, `.mcp.json`) | Blocks user/mount-namespace creation by default; the README names rootless podman as failing. `allowNestedUserNamespaces: true` voids the write denies for that command. TypeScript; no resource limits | No. Its threat write-up on nested user namespaces is a useful reference | [README](https://github.com/anthropic-experimental/sandbox-runtime/blob/main/README.md) |
| Claude Code sandbox (built on srt) | `excludedCommands` run fully *unsandboxed* on the host; `allowUnsandboxedCommands` (default true) lets the agent retry unsandboxed | Exclusion means host execution; no typed params, signing or job sandbox | No. actionbox is the safe alternative to excluding `mvn`/`docker`. Set `allowUnsandboxedCommands: false` | [sandboxing docs](https://code.claude.com/docs/en/sandboxing) (search-summary) |
| OpenAI Codex CLI rust-v0.165.0 | Linux: bwrap + seccomp by default (Landlock is legacy); `--ro-bind / /` + writable roots; `.git`, resolved gitdir and `.codex` re-bound read-only; network syscalls blocked | Sandboxes arbitrary agent commands; nested containers not addressed (unverified) | Design reference only: same pattern as R12/R13 | [linux-sandbox README](https://github.com/openai/codex/blob/main/codex-rs/linux-sandbox/README.md) |
| Docker Sandboxes (`sbx`) | microVM per agent with its own Docker daemon; deny-by-default proxy; credentials injected at the proxy | Whole-agent VM; network by domain policy, not offline; no action model; needs KVM and Ubuntu ≥ 24.04; some features paid. Testcontainers only shown in Docker-staff demos | No. An alternative to Fence + actionbox, not a building block | [architecture](https://github.com/docker/docs/blob/main/content/manuals/ai/sandboxes/architecture.md), [install](https://github.com/docker/docs/blob/main/content/manuals/ai/sandboxes/install.md), [TC demo](https://github.com/shelajev/testcontainers-sbx-demo) |
| Docker MCP Toolkit / Gateway | Each MCP server runs in a container: `no-new-privileges`, CPU/memory caps, `--block-network`, secret scanning, read-only binds | Isolates MCP servers, not build jobs; egress open by default; no per-repo signed actions | No. Use its defaults as a checklist | [mcp-gateway](https://github.com/docker/mcp-gateway), [security.md](https://github.com/docker/mcp-gateway/blob/main/docs/security.md) |
| container-use (Dagger) v0.4.2, experimental, last release 2025-08 | MCP server giving each agent a container + git branch | Arbitrary commands; no fixed actions, typed params or offline guarantee; needs a Dagger engine | No | [repo](https://github.com/dagger/container-use) |
| Claude Code devcontainer | Container + iptables default-DROP + ipset allowlist | Whole-agent; no Docker inside; no action model | No | [devcontainer.json](https://github.com/anthropics/claude-code/blob/main/.devcontainer/devcontainer.json), [init-firewall.sh](https://github.com/anthropics/claude-code/blob/main/.devcontainer/init-firewall.sh) |
| nono (pre-1.0) | Per-tool ephemeral sandboxes with explicit file/network/env/credential grants; proxy credential injection; Sigstore checks of skills/hooks/instruction files; audit log | Not owner-defined build actions with regex params; offline jobs, nested podman and fetch approval not shown (unverified); mechanism is Landlock/Seatbelt per its docs (unverified) | Closest in spirit; worth a closer look, but probably no podman nesting | [README](https://github.com/always-further/nono) |
| Gemini CLI sandbox | Whole CLI runs in docker/podman/Seatbelt/runsc/lxc | Whole-agent; no action model | No | [sandbox.md](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/sandbox.md) |
| E2B (Firecracker) | microVM per sandbox; nftables egress filtering by domain; self-host option | General code-exec SDK; no action model; needs KVM | Overkill | [e2b-dev/infra](https://github.com/e2b-dev/infra) |
| microsandbox (beta) | Local microVMs running OCI images; documents running Docker inside | No action model; needs KVM; beta | Possible alternative job backend (unverified for Testcontainers) | [repo](https://github.com/microsandbox/microsandbox) |
| Daytona | Dev sandboxes | Open-source repo unmaintained since 2026-06; development moved private | No | [repo](https://github.com/daytonaio/daytona) |
| gVisor / Kata / Firecracker | Isolation primitives | No action model; nested containers need extra setup | No | [gvisor](https://github.com/google/gvisor), [kata](https://github.com/kata-containers/kata-containers), [firecracker](https://github.com/firecracker-microvm/firecracker) |

## 2. MCP servers exposing predefined commands

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| `mise mcp` (mise v2026.10.21) | `run_task`, `list_commands`, `install_tool`; `run_task` refuses an untrusted config; `MISE_SAFE=1` disables it | No sandbox; tasks run "with your account's access"; trust is a local hash or auto-trust; shell scripts | No | [docs/mcp.md](https://github.com/jdx/mise/blob/main/docs/mcp.md) |
| task-mcp 0.5.0 (just) | Exposes only recipes in `[group('allow-agent')]` | The marker sits in the justfile, which the agent can edit; no signing; shell; no sandbox (unverified beyond the README) | No | [README](https://docs.rs/crate/task-mcp/0.5.0/source/README.md) |
| Other just MCP servers (promptexecution, toolprint, millsaj) | List and run recipes | No trust or sandbox mentioned (unverified) | No | [millsaj](https://lobehub.com/mcp/millsaj-just-runner-mcp), [toolprint](https://awesome.ecosyste.ms/projects/github.com%2Ftoolprint%2Fjust-mcp) |
| Taskfile MCP servers (ankostis, PaddyAlton) | One tool per task | No trust or sandbox (unverified); no official go-task server | No | [ankostis](https://pkg.go.dev/github.com/ankostis/mcp-taskfile-server), [PaddyAlton](https://github.com/PaddyAlton/gotask-mcp) |
| mcp-server-commands (g0t4) | `run_process` in shell or argv mode | Arbitrary execution; only per-call approval | No | [repo](https://github.com/g0t4/mcp-server-commands) |
| Allowlisted shell servers (mcp-unix-shell, cmdshellmcp) | Binary allowlist | No argument validation; cmdshellmcp's own docs say allowlisting bash/python is bypassable | No | [mcp-unix-shell](https://mcpservers.org/en/servers/gamunu/mcp-unix-shell), [cmdshellmcp](https://mcpservers.org/servers/ag88/cmdshellmcp) (directory pages) |

## 3. Task runners with typed params

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| just 1.59.0 | Recipe params; `[arg("x", pattern=REGEX)]` (since 1.45.0, full-match); `[positional-arguments]` | Values substituted textually into shell lines, so quoting pitfalls remain; no trust or signing | Borrow the `pattern=` semantics only | [README](https://github.com/casey/just/blob/master/README.md) |
| mise tasks / usage spec | Args/flags with required, `choices`, variadic | No regex; values passed as env vars or Tera into shell; variadic args need `eval` | Possible vocabulary for the param schema; not a drop-in | [task-arguments.md](https://github.com/jdx/mise/blob/main/docs/tasks/task-arguments.md) |
| Taskfile v3.54.0 | `requires.vars[]` with `enum` | No regex; runs through a shell interpreter (mvdan/sh, unverified) | No | [schema.json](https://github.com/go-task/task/blob/main/website/src/public/schema.json) |
| make | Variables | Untyped; `$(SHELL)`; no trust | No | general knowledge (not re-checked) |

None executes argv without a shell, and none signs task definitions. This confirms Q19.

## 4. Trust of in-repo config

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| Sequoia git `sq-git` 0.6.0 | Authorizes a commit if a path from the trust root exists where each commit is signed by a key its parent's `openpgp-policy.toml` allows; `--trust-root`, external `--policy-file` | Every commit on the path must be signed, which conflicts with unsigned agent commits. Authorizes commits, not "file == blob". No trust-root ratchet. Large Rust dependency. Merge and revocation semantics unverified (spec page unreachable) | Only if all commits get signed; otherwise no | [README](https://gitlab.com/sequoia-pgp/sequoia-git/-/blob/main/README.md), [intro](https://gitlab.com/sequoia-pgp/sequoia-git/-/blob/main/book/src/introduction.md), [cli](https://gitlab.com/sequoia-pgp/sequoia-git/-/blob/main/src/cli/mod.rs) |
| gittuf v0.17.0 (beta, OpenSSF) | Signed hash-chained Reference State Log; path rules (`file:…`); GPG/SSH/Sigstore keys; `verify-ref` | Needs RSL and policy refs maintained in the repo the agent can write; freeze attacks out of scope; beta format | No (heavy for one file + one key) | [repo](https://github.com/gittuf/gittuf), [design](https://github.com/gittuf/gittuf/blob/main/docs/design-document.md) |
| `git verify-commit`, `gpg.ssh.allowedSignersFile` | Signature check; `valid-after`/`valid-before` | Porcelain (ADR 0002); no ancestry or rollback guard; no re-hashing | No | [gpg.adoc](https://github.com/git/git/blob/master/Documentation/config/gpg.adoc) |
| sigstore gitsign | Keyless OIDC signing; Rekor log | Online verification by default (offline "experimental"); identity-based, not a pinned key | No | [README](https://github.com/sigstore/gitsign/blob/main/README.md) |
| direnv allow | SHA-256 of path + content; any edit revokes | Local trust on first use (TOFU), no signature or history | UX analogue only | [rc.go](https://github.com/direnv/direnv/blob/master/internal/cmd/rc.go) |
| mise trust / paranoid | Content-hash trust in paranoid mode | Non-paranoid mode auto-trusts on `mise run`; local TOFU | No | [paranoid.md](https://github.com/jdx/mise/blob/main/docs/paranoid.md), [trust.md](https://github.com/jdx/mise/blob/main/docs/cli/trust.md) |
| Nix `accept-flake-config` | Prompt before applying a flake's `nixConfig` | Prompt only, no signatures | No | [settings.hh](https://github.com/NixOS/nix/blob/master/src/libflake/include/nix/flake/settings.hh) |
| VS Code Workspace Trust | Restricted mode blocks tasks, terminal and agents | Path-based, not content-based | No | [workspace-trust.md](https://github.com/microsoft/vscode-docs/blob/main/docs/editing/workspaces/workspace-trust.md) |
| Claude Code project `.mcp.json` approval | Per-server user approval | By name, not content hash (unverified) | No; R8 already treats it as no control | [mcp docs](https://code.claude.com/docs/en/mcp) (unverified) |

## 5. Rootless containers in unprivileged sandboxes / Testcontainers

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| Testcontainers + Podman doc | `DOCKER_HOST` to the podman socket; `TESTCONTAINERS_RYUK_DISABLED=true` for rootless; alternative runtimes "not actively tested" | Nothing on nested or sandboxed setups | Already followed (§8.7) | [supported_docker_environment](https://github.com/testcontainers/testcontainers-java/blob/main/docs/supported_docker_environment/index.md) |
| Ryuk | Reaper; "must be started as a privileged container" | Disabled, cleanup happens only at JVM shutdown, not on `kill -9` | Teardown of the bwrap/unshare tree covers it | [configuration.md](https://github.com/testcontainers/testcontainers-java/blob/main/docs/features/configuration.md) |
| Testcontainers 2.0.3+ API fallback | API 1.44 default (2.0.2); falls back to 1.32 when the ping fails (2.0.3, PR #11346); latest 2.0.5 (2026-04-20) | — | May make the R10 pin unnecessary (unverified on Podman 3.4 compat API 1.40) | [PR #11346](https://github.com/testcontainers/testcontainers-java/pull/11346), [podman v3.4.4 version.go](https://github.com/containers/podman/blob/v3.4.4/version/version.go) |
| `podman unshare` / storage options | Maps the caller to uid 0 plus the subuid range via newuidmap; `vfs` recommended for nested setups; `ignore_chown_errors` for single-UID setups | Upstream nested examples use `--privileged` | Confirms the E5→E9 choice | [podman-unshare.1](https://github.com/containers/podman/blob/main/docs/source/markdown/podman-unshare.1.md), [troubleshooting](https://github.com/containers/podman/blob/main/troubleshooting.md), [storage.conf.5](https://github.com/containers/storage/blob/main/docs/containers-storage.conf.5.md), [podman image README](https://github.com/containers/image_build/blob/main/podman/README.md) |
| docker:dind-rootless | Rootless dockerd in a container | Requires `--privileged` | No | [rootless tips](https://github.com/docker/docs/blob/main/content/manuals/engine/security/rootless/tips.md) |
| Sysbox v0.7.1 | Unprivileged containers that run Docker/k8s | Root-installed runtime; changes the host trust model | No (only for container-per-job) | [README](https://github.com/nestybox/sysbox/blob/master/README.md) |
| gVisor Docker-in-runsc | Docker 27–29 inside runsc | Needs runsc + Docker host, `--iptables=false`, host-network nested containers | No | [docker-in-gvisor](https://github.com/google/gvisor/blob/master/g3doc/user_guide/tutorials/docker-in-gvisor.md) |
| Testcontainers Cloud | Remote container runtime | Needs network and credentials | No | [tcc.md](https://github.com/docker/docs/blob/main/content/manuals/tcc.md) |
| Docker Sandboxes | See §1: a private daemon per microVM runs Testcontainers | Not offline; KVM | No | see §1 |

## 6. Offline builds with approved dependency fetching

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| Maven `dependency:go-offline` (plugin 3.11.0) | Pre-resolves deps | Known gaps: plugin deps, surefire providers, missing poms (MDEP-82/198/785) | No. Justifies the iterative fetch loop | [#625](https://github.com/apache/maven-dependency-plugin/issues/625), [#1168](https://github.com/apache/maven-dependency-plugin/issues/1168), [#1293](https://github.com/apache/maven-dependency-plugin/issues/1293) |
| Maven chained local repo | `maven.repo.local.tail` (3.9.0+, read-only tails), `.head` (3.9.10+) | No approval | **Yes**: cache as a read-only tail, per-job writable head | [DefaultRepositorySystemSessionFactory](https://github.com/apache/maven/blob/maven-3.9.x/maven-core/src/main/java/org/apache/maven/internal/aether/DefaultRepositorySystemSessionFactory.java), [local-repository.md](https://github.com/apache/maven-resolver/blob/master/src/site/markdown/local-repository.md) |
| Maven Remote Repository Filtering | Restricts which repo serves which groupIds | Its own doc says it is not a security allowlist | No | [RRF](https://github.com/apache/maven-resolver/blob/master/src/site/markdown/remote-repository-filtering.md) |
| Gradle `--offline`, `GRADLE_RO_DEP_CACHE` | Offline builds; shared read-only cache | No approval | If Gradle is added | [dependency_caching](https://docs.gradle.org/current/userguide/dependency_caching.html) (search-summary) |
| npm 12 | `--offline`; `min-release-age`, `allow-git`/`allow-remote` default `none`, `allowScripts` | `npm cache add` is "primarily intended to be used internally"; the cache has "no guarantee" of keeping entries | Add the policies to the host fetch; reconsider the cache as the store | [npm-cache.md](https://github.com/npm/cli/blob/latest/docs/lib/content/commands/npm-cache.md), [definitions.js](https://github.com/npm/cli/blob/latest/workspaces/config/lib/definitions/definitions.js) |
| pnpm (docs v12) | `pnpm fetch` from the lockfile, then `--offline`; `minimumReleaseAge`, `strictDepBuilds`, `blockExoticSubdeps` | No human approval; lockfile is agent-writable | No (pnpm-only) | [fetch.md](https://github.com/pnpm/pnpm.io/blob/main/docs/cli/fetch.md), [supply-chain-security.md](https://github.com/pnpm/pnpm.io/blob/main/docs/supply-chain-security.md) |
| Verdaccio | Private npm proxy/cache | No approval; offline jobs couldn't reach it except via unix socket or file store (unverified) | No | [README](https://github.com/verdaccio/verdaccio/blob/master/README.md) |
| Nix fixed-output derivations | Only hash-pinned outputs get network | Requires Nix | Idea only: approved fetch = coordinate + expected hash (as image `@sha256` already does) | [content-address.md](https://github.com/NixOS/nix/blob/master/doc/manual/source/store/derivation/outputs/content-address.md) |
| Bazel `vendor` | Vendored external repos; offline builds | Bazel-only; `--distdir` not checked | No | [vendor.mdx](https://bazel.googlesource.com/bazel/+/HEAD/docs/external/vendor.mdx) |
| Sonatype Repository Firewall | Quarantine at the proxy; waivers in a dashboard | Commercial; policy-driven, not per-request approval; network proxy | No | [firewall-quarantine](https://help.sonatype.com/en/firewall-quarantine.html) (search-summary) |
| JFrog Curation | Waiver request per package + version + repo; manual approval; API | Commercial; needs Artifactory; jobs need network | No. Closest approval model | [createwaiverrequest](https://docs.jfrog.com/security/reference/createwaiverrequest.md) |
| Socket Firewall `sfw` | Blocks known malware during `npm`/`pip` installs | Malware-only; Maven needs Enterprise | Optional wrapper around the host npm fetch | [socket-firewall-free](https://docs.socket.dev/docs/socket-firewall-free) (search-summary) |
| srt proxy, agent-leash, NVIDIA OpenShell | Domain or registry allowlists, interactive per-domain approval | Domain-level, not coordinate-level; jobs get network | No | [srt](https://github.com/anthropic-experimental/sandbox-runtime/blob/main/README.md), [agent-leash](https://pypi.org/project/agent-leash/0.11.0/) (search-summary) |

## 7. Sandbox wrappers and primitives

Question for each: can it express a read-only root, per-dir rw binds, no network, clearenv and resource limits, *and* host nested rootless podman?

| Tool (version) | Covers | Lacks vs actionbox | Reuse? | Source |
|---|---|---|---|---|
| bubblewrap 0.13.1 (2026-10-14) | Current engine. 0.12.0 fixes a symlink escape during setup (GHSA-pxhw-h44j-8pfx); `--bind-fd` (0.10.0); overlays (0.11.0); `--size`; `--userns` | No limits or seccomp policy of its own. Don't use `--disable-userns`: it breaks podman | **Yes, upgrade** from 0.6.1 | [NEWS](https://github.com/containers/bubblewrap/blob/main/NEWS.md), [bwrap.xml](https://github.com/containers/bubblewrap/blob/main/bwrap.xml), [releases](https://github.com/containers/bubblewrap/releases) |
| systemd-run / systemd.exec (~v259) | Scopes: resource control only. Services: `ProtectHome`, `PrivateNetwork` and similar | Namespacing in user services needs `PrivateUsers=`, which maps no subuid range, so no podman; `managed` mode from a user manager unverified | Keep for limits only (Q20) | [systemd.exec](https://github.com/systemd/systemd/blob/main/man/systemd.exec.xml), [systemd.scope](https://github.com/systemd/systemd/blob/main/man/systemd.scope.xml) |
| Landlock LSM (ABI v1–v12) | Filesystem, TCP (v4), UDP (v10), scoping, namespace rules (v12) | Filesystem-restricted threads "cannot modify filesystem topology", which breaks podman | Extra layer for non-container jobs only | [landlock.rst](https://github.com/torvalds/linux/blob/master/Documentation/userspace-api/landlock.rst) |
| landrun v0.1.17 | Landlock CLI: ro/rw paths, TCP, empty env | No namespaces or limits; breaks podman | No | [repo](https://github.com/Zouuup/landrun) |
| nsjail 3.6 | Binds, tmpfs, net namespace, rlimits, cgroups, Kafel seccomp, custom uid maps via newuidmap | C++, protobuf config; no bind-fd; podman inside unverified | Fallback engine | [README](https://github.com/google/nsjail/blob/master/README.md) |
| crun 1.30.1 / youki v0.7.0 | OCI spec: ro root, binds, explicit env, empty netns, rlimits, seccomp, cgroups; multi-range newuidmap | Generate `config.json`; host `/` as a read-only rootfs unverified | Cleanest replacement for `podman unshare` + flags, if ever needed | [crun.1](https://github.com/containers/crun/blob/main/crun.1.md), [linux.c](https://github.com/containers/crun/blob/main/src/libcrun/linux.c) |
| firejail 0.9.80 | `--read-only`, `--net=none`, rlimits, seccomp | Setuid root binary; no clearenv; no subuid map | No | [firejail.1](https://github.com/netblue30/firejail/blob/master/src/man/firejail.1.in) |
| bubblejail 0.10.4 | bwrap front end for desktop apps | No limits; desktop-oriented | No | [repo](https://github.com/igo95862/bubblejail) |
| hakoniwa v1.8.0 | Rust: ro rootfs, binds, env, pasta, rlimits, cgroups, Landlock, seccomp | Maps only the current user by default; subuid mapping unverified | No | [README](https://github.com/souk4711/hakoniwa/blob/main/README.md) |
| bst (Arista) | Unprivileged CI isolation; subuid mapping | Maintenance status and limits unverified | Maybe; not evaluated | [repo](https://github.com/aristanetworks/bst) |
| minijail (last tag linux-v18, 2023) | ChromeOS jail library/CLI | Details unverified; no benefit over bwrap | No | [repo](https://github.com/google/minijail) |
| birdcage 0.8.1 | Rust sandbox crate | Archived | No | [repo](https://github.com/phylum-dev/birdcage) |
| syd/sydbox 3.59.0 | seccomp-unotify sandbox | Niche; nested podman unverified | No | [crate](https://crates.io/crates/syd) |
| Flatpak | bwrap runner; drove `--bind-fd` (CVE-2024-42472) | Tied to apps and runtimes | Copy the bind-fd pattern | bwrap 0.10.0 release note above |
