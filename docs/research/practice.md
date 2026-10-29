# How teams run coding agents in sandboxes (practice, 2025–2026)

- **Checked:** 2026-10-29.
- **Scope:** real-world patterns, experience reports, incidents. Tools already compared in [prior-art.md](prior-art.md) are only referenced, not re-described.
- **Access gap:** the sandbox proxy refused almost every page fetch (simonwillison.net, docs.github.com, OpenAI docs, oddguan.com, adnanthekhan.com, docker.com, lasso.security). Only the Anthropic engineering post was read. Everything else comes from search-engine summaries of the cited page.
- **Tags:**
  - **(read)**: page read directly.
  - **(ss)**: search-summary only; the URL is the page the summary cites. Re-check before quoting in the spec.
  - **(anecdote)**: one person's report, forum post or blog, not vendor docs or a disclosed CVE.
- **Dates:** publication date where known; `n.d.` = undated docs page.

## Summary

1. **Nobody found runs builds + Docker/Testcontainers inside a strict *local* agent sandbox.** Practice splits into (a) loosen the local sandbox (exclude `docker`/`mvn`, widen domain lists), (b) move the whole agent into a VM/microVM/cloud box that has its own Docker daemon, or (c) give the agent a networked "setup phase" and an offline agent phase. actionbox's "keep the agent jail strict, run fixed jobs elsewhere" is uncommon; the closest relatives are Stripe's curated tool server and the Copilot/Codex setup-phase split.
2. **Domain allowlists are the dominant network control, and the most-attacked one.** Every major allowlist has a published bypass or abuse: github.com, `*.azure.net`, api.anthropic.com, npm, DNS, a null-byte hostname bug, an empty-list-means-allow-all bug. actionbox's "jobs fully offline, fetches by coordinates only" sidesteps the whole class.
3. **The 2026 attack trend is "trust handoff", not sandbox breakouts.** The agent writes a file that an unsandboxed component (IDE, hook engine, CI cache, release workflow) later runs. Pillar's seven escapes (2026-07) and Clinejection (2026-03) are the reference cases. This is the biggest risk for actionbox to design against.
4. **Credential proxies are now standard in vendor sandboxes** (Claude Code web git proxy, Docker Sandboxes, Cloudflare). actionbox doesn't need one for jobs, since jobs hold no credentials, but the host-side fetch is where actionbox's credentials live.
5. **Approval prompts and classifiers are widely described as best-effort.** Anthropic calls auto mode a convenience, not a security boundary. Cursor's allowlist silently didn't apply in sandbox mode. Copilot let the agent write its own auto-approve setting.

| Pattern | Builds/tests | Docker/Testcontainers | Known failure mode | Distance from actionbox |
|---|---|---|---|---|
| Loosen local sandbox + allowlists | Yes, with network | Only by excluding `docker` (host root) | Allowlist exfil; exclusion bugs | Far: actionbox exists to avoid this |
| Devcontainer / VM per agent | Yes | VM: own daemon; devcontainer: socket mount or none | Firewall upkeep; socket = host root | Medium: whole-agent boundary |
| Remote/cloud sandboxes | Yes, full stack | microVM daemon (sbx, Modal) or none (Codex cloud) | Setup phase unrestricted; allowlist abuse | Medium |
| Setup phase online, agent phase offline | Yes | Rarely | Setup scripts = arbitrary code with network | Close: like actionbox fetch, but owner-scripted |
| CI as executor | Yes, on runners | Yes, on runners | Workflow injection, cache poisoning | Medium: same "fixed jobs" idea, coarser |
| Curated tool broker / MCP gateway | Via tools | Via tools | Toxic flows across tools | Closest |
| Credential proxy | n/a | n/a | Proxy parser bugs | Not needed for jobs; applies to fetch |
| Egress proxy + logging | n/a | n/a | No content inspection; plain-HTTP gaps | Jobs offline; log broker events instead |
| Human approval / classifier | n/a | n/a | Fatigue; self-approval; misclassification | actionbox: approval only for signing + fetch |

## Patterns

### 1. Loosen the local sandbox + domain allowlist

**Who.** The default for Claude Code (srt), Codex CLI and Cursor users who need builds.
- Anthropic says sandboxing cut permission prompts by 84 % internally. Network goes through a proxy outside the sandbox that limits domains and asks the user about new ones. [Anthropic eng, 2025-10-20](https://www.anthropic.com/engineering/claude-code-sandboxing) (read)
- Codex workspace-write keeps network off unless `network_access = true`. [Codex docs, n.d.](https://developers.openai.com/codex/agent-approvals-security) (ss)

**Builds/tests/Docker.**
- Claude Code's docs tell users to put `docker` in `excludedCommands`, which runs it **unsandboxed on the host**. With `allowUnsandboxedCommands: false`, that is the only way to allow Docker. [issue 19135 mirror, n.d.](https://claudeissues.com/issue/19135-docs-logic-conflict-between-excludedcommands-and-allowunsandboxedcommands-false) (ss)
- One practitioner excludes `docker *` and keeps it behind prompts. That works until the agent needs containers, and they moved to Docker Sandboxes. [Arrange Act Assert, n.d.](https://arrangeactassert.com/posts/docker-sandboxes-when-claude-code-needs-docker/) (ss, anecdote)
- Maven's HTTP client doesn't pick up the sandbox proxy automatically. One Codex guide uses setup scripts and offline Maven/Gradle. [D. Vaughan, 2026-03-30](https://codex.danielvaughan.com/2026/03/30/codex-cli-java-spring-boot-teams/) (ss, anecdote)

**Pain points.**
- `excludedCommands` is repeatedly reported as ignored, or still proxied/network-restricted. One open macOS report lists `mvn`, `mvnw`, `gh`, `git`. [#95813](https://claudeissues.com/issue/95813-sandbox-excludedcommands-has-no-effect-listed-commands-still-run-sandboxed), [#89931 (2026-08)](https://claudeissues.com/issue/89931-sandbox-excludedcommands-has-no-effect-excluded-commands-still-run-sandboxed-2-1), [#12150](https://claudeissues.com/issue/12150-bug-claude-code-seems-to-set-proxy-for-commands-in-the-excludedcommands-sandbox) (ss)
- Codex bwrap fails inside devcontainers (`NETLINK_ROUTE ... Operation not permitted`; workspace not writable). Port binding inside the sandbox fails (EPERM on 127.0.0.1:3000 for Playwright). [#14794](https://github.com/openai/codex/issues/14794), [#5041](https://github.com/openai/codex/issues/5041), [#376](https://github.com/openai/codex/issues/376) (ss)
- Cursor: the command allowlist is silently ignored when "auto-run in sandbox" is on (staff-confirmed). Egress blocking is "best-effort" on Windows. [Cursor forum, 2026](https://forum.cursor.com/t/command-allowlist-is-silently-ignored-when-auto-run-in-sandbox-is-enabled/152136) (ss, anecdote)

**Incidents/attacks.** See [Known attacks](#known-attacks-and-incidents) rows A1–A6. Anthropic's own docs warn that allowing broad domains like github.com may enable exfiltration and that domain fronting can bypass filtering. [sandboxing docs, n.d.](https://docs.claude.com/en/docs/claude-code/sandboxing) (ss)

**vs actionbox.** This is the setup actionbox replaces. Excluding `mvn`/`docker` hands the agent host execution. Reports show the exclusion mechanism itself is flaky.

### 2. Devcontainer / container / VM per agent

**Who.**
- Anthropic's reference devcontainer (iptables default-DROP + ipset allowlist; see prior-art).
- Community wrappers: ExitBox (rootless Podman + Squid allowlist, caps dropped) [pkg.go.dev, n.d.](https://pkg.go.dev/github.com/cloud-exit/exitbox@v0.1.5) (ss) and cs-sandbox (rootless Podman or Firecracker) [pkg.go.dev, n.d.](https://pkg.go.dev/github.com/codesweep-ai/sandbox) (ss).
- Vagrant VM chosen over Docker/Firejail to avoid Docker-in-Docker. [news.hada.io, 2026](https://news.hada.io/topic?id=26002) (ss, anecdote)
- INNOQ: devcontainer + forward-proxy allowlist, policy mounted read-only so the agent can't edit it. [INNOQ blog, 2026-03](https://www.innoq.com/en/blog/2026/03/dev-sandbox-network/) (ss)

**Builds/tests/Docker.**
- Devcontainers either mount the host socket (= host root) or have no Docker at all.
- VMs and microVMs (Docker `sbx`) give the agent its own daemon. A Level1Techs user calls sbx "a real hypervisor boundary, separate kernel, isolated Docker daemon". [forum, 2026](https://forum.level1techs.com/t/docker-sandboxes-sbx/249634) (ss, anecdote)
- Docker-socket proxies are an alternative: Tecnativa, DockGate, and a Kantega Go proxy that only allows named volumes and isolated networks and touches only containers it created. The Kantega write-up says this was enough for Testcontainers. [Kantega/Medium, n.d.](https://medium.com/@larma/containerizing-claude-ef722831fb10) (ss, anecdote)
- Testcontainers' Ryuk needs socket access, and hardened user-namespace or SELinux setups break it. The usual fix is `TESTCONTAINERS_RYUK_DISABLED=true` on rootless Podman. [Lemmy thread](https://lemmy.johnnei.org/post/238876), [oneuptime 2026-03-18](https://oneuptime.com/blog/post/2026-03-18-fix-podman-socket-connection-issues/markdown) (ss, anecdote)

**Pain points.**
- Every new registry needs a firewall edit. DNS, port 22 and localhost stay open in common variants of the firewall script. [devcon4 README](https://readme.hex.pm/devcon4/0.3.0) (ss)
- sbx: unclear GA status, no automatic port publishing, live workspace mount (the agent edits your files, and `.git/hooks` changes don't show in `git diff`). [Docker forum](https://forums.docker.com/t/what-is-the-current-release-status-of-docker-sandboxes-early-access-experimental-or-ga/151749), [Cloud Native Now](https://cloudnativenow.com/contributed-content/the-questions-every-team-asks-about-docker-sandboxes/), [Level1Techs](https://forum.level1techs.com/t/docker-sandboxes-sbx/249634) (ss, anecdote)
- OpenHands OSS mounts the host Docker socket into the controller. A self-hosting guide treats that as host root and advises a disposable VPS. OpenHands Enterprise instead runs a per-sandbox daemon under a hardened runtime with user-namespace remapping. [OpenHands docs, n.d.](https://docs.openhands.dev/enterprise/docker-in-sandbox), [SSD Nodes](https://www.ssdnodes.com/learn/lang/de/self-host-openhands-vps) (ss)

**vs actionbox.** Same trade as prior-art: the whole agent gets one boundary and a network policy. actionbox keeps the agent in Fence and gives only jobs a container runtime, with no network. The `.git/hooks` remark matters for actionbox: the agent can write hooks that run when *you* next use git (see Gaps).

### 3. Remote / cloud sandboxes (background agents)

**Who.**
- **Stripe Minions.** Unattended agents on the same warmed cloud devboxes humans use, in a QA environment "isolated from production data, real user information, and arbitrary network access", so they run "with full permissions and no confirmation prompts". Human review on every PR; 1–2 CI rounds. [Stripe via ByteByteGo, 2026-02-09](https://blog.bytebytego.com/p/how-stripes-minions-ship-1300-prs) (ss)
- **Ramp Inspect.** Each session gets a Modal sandbox with Postgres, Redis, Temporal and RabbitMQ, started from scheduled filesystem snapshots. [Modal blog, 2026-02](https://modal.com/blog/how-ramp-built-a-full-context-background-coding-agent-on-modal) (ss)
- **Codex cloud / Claude Code web / Copilot cloud agent.** Vendor-hosted containers.

**Builds/tests/Docker.**
- Full service stacks are pre-provisioned (Ramp) or come from the devbox image (Stripe).
- Docker inside Codex cloud failed for one user with `unshare: operation not permitted`. [OpenAI forum, 2025](https://community.openai.com/t/codex-docker-in-docker-in-environment-setup/1272369) (ss, anecdote)
- Docker blog demo (2026-08-21): an agent in sbx inside GitHub Actions runs a Testcontainers/PostgreSQL suite, with a guardrail "do not weaken or delete existing tests". [Docker blog](https://www.docker.com/blog/running-ai-agents-in-github-actions-with-docker-sandboxes/) (ss)

**Pain points.** Warm-up cost (hence warm pools and snapshots), and keeping the environment the same as production CI.

**vs actionbox.** These rely on an *environment* that holds nothing worth stealing, so the agent can do anything inside it. actionbox applies the same idea per job on a laptop that does hold secrets.

### 4. Network only in a setup phase, offline agent phase

**Who.**
- **Codex cloud.** The setup script has network access, the agent phase is offline by default, and secrets reach only the setup script. [Codex cloud docs, n.d.](https://learn.chatgpt.com/docs/cloud/internet-access) (ss), [Willison, 2025-06](https://simonwillison.net/tags/async-coding-agents/) (ss)
  - A WorkOS analysis says no documented setting restricts the setup phase. [WorkOS, n.d.](https://workos.com/blog/agent-sandbox-egress-defaults) (ss)
- **Copilot cloud agent.** `copilot-setup-steps.yml` runs before the agent. The firewall does **not** apply to setup steps or MCP servers. The default allowlist covers OS package repos, container registries and language registries. [GitHub docs, n.d.](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/customize-the-agent-firewall) (ss)

**vs actionbox.** This is the nearest mainstream analogue of actionbox fetches, but coarser: setup is arbitrary owner-written code with full network, run once. actionbox fetches are per request, by exact coordinates, approved one by one. Lesson from both vendors: the *owner-controlled* phase is where the network lives, and it should not read agent-writable files (see Gaps G1).

### 5. CI as executor

**Who.** Copilot cloud agent runs on ephemeral Actions runners; GitHub recommends single-use self-hosted runners. [GitHub docs, n.d.](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/customize-the-agent-environment) (ss). Stripe Minions push and use CI as the test oracle (see 3). GitHub Agentic Workflows added sbx as a runtime in 2026-07. [Docker blog, 2026-08-21](https://www.docker.com/blog/running-ai-agents-in-github-actions-with-docker-sandboxes/) (ss)

**Builds/tests/Docker.** Native: runners have Docker.

**Pain points / incidents.** CI is a privileged environment with publish tokens and shared caches. **Clinejection** (row A8) shows an AI step in CI poisoning the Actions cache that a release workflow later restored. **s1ngularity** (A9) began with a workflow injection.

**vs actionbox.** Same "fixed, owner-defined jobs" idea, but CI jobs usually have network, secrets and shared caches. actionbox jobs have none of the three. Clinejection is a direct warning for actionbox's per-project caches (G2).

### 6. Curated tool broker / MCP gateway

**Who.**
- **Stripe Toolshed.** Central internal MCP server with ~500 tools; each agent gets a curated subset. Blueprints mix deterministic steps (lint, push) with agentic ones. [ByteByteGo on Stripe, 2026-02](https://blog.bytebytego.com/p/how-stripes-minions-ship-1300-prs) (ss)
- **ToolHive.** Containers per MCP server, registry curation, OIDC gateway, OTel traces. [Help Net Security, 2026-09-07](https://www.helpnetsecurity.com/2026/09/07/toolhive-open-source-mcp-server-security/) (ss)
- **Gateway practice.** One audit-log entry per tool call: tool, args, result, latency, credential. Some gateways hash-chain the log. [dev.to/Bifrost](https://dev.to/kuldeep_paul/mcp-access-governance-across-teams-tenants-and-third-party-integrations-1ike), [Glama mcp-gateway](https://glama.ai/mcp/servers/PetrefiedThunder/mcp-gateway) (ss, vendor)

**Incidents.** GitHub MCP "toxic flow" (A10). A broker that offers both read-private and write-public tools is itself the exfil path.

**vs actionbox.** actionbox *is* this pattern, narrowed to build actions with signing, typed params and per-job sandboxes. No public tool broker found verifies its tool list against a signed commit. actionbox exposes no write-external tool, so no toxic flow exists inside actionbox itself. It can still combine with the agent's *other* MCP servers (G6).

### 7. Credential proxy / secret injection

**Who.**
- **Claude Code web.** The sandboxed git client holds a scoped credential. A proxy checks the request (for example, push only to the configured branch) before attaching the real token. [Anthropic eng, 2025-10-20](https://www.anthropic.com/engineering/claude-code-sandboxing) (read)
- **Docker sbx.** A host proxy overwrites auth headers; the sandbox only holds a sentinel. [Docker docs, n.d.](https://docs.docker.com/ai/sandboxes/security/credentials/) (ss)
- Cloudflare Sandbox and Formal (proxy hides secrets from Claude Code) do the same. [Cloudflare, n.d.](https://blog.cloudflare.com/sandbox-ga), [Formal, n.d.](https://joinformal.com/blog/using-proxies-claude-code) (ss)
- fnox v1.43.0: credential proxy, output redaction and an audit log that records which secrets were read, without their values. [release notes](https://newreleases.io/project/github/jdx/fnox/release/v1.43.0) (ss)

**Pain points.** Formal notes that Claude Code passes the whole terminal environment into its sandbox. [Formal](https://joinformal.com/blog/using-proxies-claude-code) (ss)

**vs actionbox.** Jobs get no credentials at all, which is stronger. The agent process itself (in Fence) still inherits whatever env Fence passes (G5).

### 8. Egress proxy + logging

**Who.**
- Docker sbx logs allowed and blocked connections live. Raw TCP/UDP/ICMP are blocked; filtering is by hostname only. [DZone, n.d.](https://dzone.com/articles/docker-sandbox-isolation-story) (ss)
- Copilot's firewall reports blocked requests in the PR (ss, same GitHub docs as above).
- Hamza: a reverse proxy between the agent and the model API that masks secrets in *prompts*. It argues Codex's built-in output redaction never inspects outgoing payloads. [D. Vaughan, 2026-08-19](https://codex.danielvaughan.com/2026/08/19/egress-gates-hamza-proxy-codex-cli-secrets-masking-presidio-audit-enterprise-data-protection/) (ss, anecdote)

**Limits.**
- Hostname proxies don't inspect content, and srt filtered CONNECT but not plain HTTP (per one write-up). [Claude Almanac](https://claude-almanac.pages.dev/docs/security-sandbox), [jiangren](https://jiangren.com.au/en/learn/ai-engineer/claude-code-sandboxing) (ss)
- Destination allowlists miss DNS-subdomain encoding and secrets posted to trusted endpoints. [lavx](https://news.lavx.hu/article/why-network-allow-lists-alone-can-t-prevent-credential-exfiltration) (ss)

**vs actionbox.** Jobs have no egress to log. The equivalent artifact is a broker event log: actions run, params, fetches requested and approved (G4).

### 9. Human approval / classifier

**Who.**
- **Claude Code auto mode.** A classifier screens each tool call. Anthropic says it "may still allow some risky actions" and still recommends isolation. Falls back to manual after 3 consecutive or 20 total blocks. [claude.com, 2026-03](https://claude.com/blog/auto-mode) (ss)
- **Meta "Agents Rule of Two".** At most two of: untrusted input, sensitive data, external state change/communication. [Meta AI, 2025-10-31](https://ai.meta.com/blog/practical-ai-agent-security/) (ss)
- **Willison.** Using AI to detect attacks "is not a useful security defense"; sandboxing is the only credible answer, and network control is the hard part. [2025-10-22](https://simonwillison.net/2025/Oct/22/living-dangerously-with-claude/) (ss)

**Failure modes.**
- An agent writing its own approval settings (A5).
- Allowlisted "safe" commands used for DNS exfil (A1, A3).
- `git show` trusted by name with unchecked args in Codex (A7).
- Shell built-ins bypassing Cursor's allowlist (CVE-2026-22708). [D. Vaughan, 2026-06-23](https://codex.danielvaughan.com/2026/06/23/agent-security-paradox-cursor-allowlist-bypass-cve-2026-22708-codex-cli-sandbox-architecture-defence/) (ss)

**vs actionbox.**
- Approval is limited to two low-frequency, high-value points: signing the definitions and approving fetches.
- Per Rule of Two, a job has untrusted input (agent code) but no sensitive data and no external comms: 1 of 3.
- A fetch has an external effect but only for owner-approved coordinates.
- The Codex `git show` bug is exactly what argv templates with full-match regex params prevent. Keep the "can't start with `-`" rule.

## Known attacks and incidents

| # | Date | Target | What happened | Channel | Status | Source |
|---|---|---|---|---|---|---|
| A1 | 2025 (fixed 1.0.4) | Claude Code, CVE-2025-55284 | Injected file made the agent run auto-approved `ping`/`nslookup`/`dig` with secrets in DNS names | DNS via allowlisted cmds | Researcher PoC | [vibegraveyard](https://vibegraveyard.ai/story/claude-code-dns-data-exfiltration/) (ss) |
| A2 | 2025-11-26 fix | Claude Code sandbox, CVE-2025-66479 | Empty domain list (meant "deny all") parsed as allow-all | Network proxy bug | PoC, A. Guan | [Penligent](https://www.penligent.ai/hackinglabs/claude-code-sandbox-bypass/) (ss) |
| A3 | 2025-08-24 found, 2025-11-18 pub. | Cline | DNS exfil via "safe" `ping`; markdown image rendering to untrusted domains | DNS, image URLs | PoC | [Mindgard](https://mindgard.ai/disclosures/cline-bot-ai-coding-agent-data-exfiltration-via-prompt-injection-and-dns), [Embrace The Red](https://embracethered.com/blog/posts/2025/cline-vulnerable-to-data-exfiltration) (ss) |
| A4 | 2026-05-20 disclosed | Claude Code sandbox v2.0.24–2.1.89 | SOCKS5 hostname with null byte passed suffix match (`evil\0.github.com`); silently patched 2.1.90, 2026-04-01 | Allowlist parser | PoC | [oddguan](https://oddguan.com/blog/second-time-same-sandbox-anthropic-claude-code-network-allowlist-bypass-data-exfiltration/), [SecurityWeek](https://www.securityweek.com/anthropic-silently-patches-claude-code-sandbox-bypass/) (ss) |
| A5 | 2025-08 (Patch Tuesday) | Copilot agent mode, CVE-2025-53773 | Injection made agent write `chat.tools.autoApprove` into `.vscode/settings.json` → RCE | Self-modifying config | PoC | [Embrace The Red](https://embracethered.com/blog/posts/2025/github-copilot-remote-code-execution-via-prompt-injection) (ss) |
| A6 | 2025-06 to 08 | Codex web; Claude Code Interpreter | Allowlisted `*.azure.net` (anyone can host `*.cloudapp.azure.net`); allowlisted api.anthropic.com used with *attacker's* API key to upload 30 MB via Files API | Allowlisted domain | PoC | [Willison 2025-08-15](https://simonwillison.net/2025/Aug/15/the-summer-of-johann/), [Embrace The Red ~2025-10-30](https://embracethered.com/blog/posts/2025/claude-abusing-network-access-and-anthropic-api-for-data-exfiltration) (ss) |
| A7 | 2026-07-20 | Cursor, Codex, Gemini CLI, Antigravity | 7 "trust handoff" escapes: agent writes files that unsandboxed IDE/hook/git/venv components later run. Cursor hooks CVE-2026-48124; shared Docker-socket exposure; Codex `git show` arg bypass (fixed 0.95.0) | Host-side readers of workspace files | Fixed mostly | [BleepingComputer](https://www.bleepingcomputer.com/news/security/cursor-codex-gemini-cli-antigravity-hit-by-sandbox-escapes/), [CSA note 2026-07-22](https://labs.cloudsecurityalliance.org/research/csa-research-note-ai-coding-agent-sandbox-escapes-20260722-c/) (ss) |
| A8 | 2025-12 to 2026-03 | Cline release pipeline ("Clinejection") | Issue title injected AI triage workflow → poisoned Actions cache → nightly publish workflow restored it → npm/VSCE/OVSX tokens. cline@2.3.0 published by attacker (installed OpenClaw) | Shared build cache | **In the wild** | [Khan](https://adnanthekhan.com/posts/clinejection), [Willison 2026-03-06](https://simonwillison.net/2026/Mar/6/clinejection/), [CSA 2026-03-10](https://labs.cloudsecurityalliance.org/research/csa-research-note-clinejection-prompt-injection-cicd-cache-p/) (ss) |
| A9 | 2025-08-26 | Nx ("s1ngularity") | Malicious postinstall ran local `claude --dangerously-skip-permissions`, `gemini --yolo`, `q --trust-all-tools` to find secrets; uploaded to public GitHub repos; >1,000 victims | Package install on dev host | **In the wild** | [Wiz](https://wiz.io/blog/s1ngularity-supply-chain-attack), [Socket](https://socket.dev/blog/nx-packages-compromised) (ss) |
| A10 | 2025-05-26 | GitHub MCP server | Public issue injected agent → read private repo → leaked via PR to public repo | Tool broker toxic flow | PoC; no CVE (architectural) | [Invariant Labs](https://invariantlabs.ai/blog/mcp-github-vulnerability) (ss) |
| A11 | 2025-07-13 to 19 | Amazon Q VS Code ext 1.84.0 | Malicious PR merged a wiper prompt into a signed release; payload malformed | Agent prompt supply chain | **Shipped**, no damage | [OECD AIM](https://oecd.ai/en/incidents/2025-07-23-581e), [TechRadar](https://www.techradar.com/pro/amazon-ai-coding-agent-hacked-to-inject-data-wiping-commands) (ss) |
| A12 | 2025-08 | Devin | $500 of tests: poisoned issue/web page → RCE + several exfil channels | Agent with network | PoC | [vibegraveyard](https://vibegraveyard.ai/story/devin-ai-prompt-injection-no-protection/), [Willison 2025-08-15](https://simonwillison.net/2025/Aug/15/the-summer-of-johann/) (ss) |
| A13 | n.d. | OpenClaw sandbox (Lasso) | Exfil through allowlisted `git`/`gh`/`npm` to github.com despite binary-scoped egress | Allowlisted tool + domain | PoC | [Lasso](https://www.lasso.security/blog/sandboxed-ai-agents-attack-surface) (ss) |

**Patterns across incidents.**
1. Every domain allowlist was abused: github.com, npm, azure, api.anthropic.com, DNS.
2. The in-the-wild cases (A8, A9, A11) are supply-chain: package installs and CI caches, not live chat sessions.
3. 2026 escapes go *around* the sandbox through files a trusted host component later reads (A5, A7, A8).

## Gaps and ideas for actionbox

Ranked by expected value.

| # | Idea | Evidence | Suggested spec change |
|---|---|---|---|
| G1 | **No host-side component reads agent-writable files** (trust-handoff rule) | A7, A5, A8, Copilot/Codex setup phase | State as an invariant. Broker and fetch parse only the signed definitions blob (from the git object, not the worktree) and their own config. Fetch must not read `.mvn/`, `.npmrc`, `package.json`, `settings.xml` or lockfiles from the repo. Add a conformance/unit test that fetch runs with the project dir unreadable |
| G2 | **Jobs never write anything a later job or the owner trusts** | A8 (cache poisoning) | Maven cache is already read-only to jobs. Extend explicitly: npm cache and the container image store are read-only too (or per-job overlay), images resolved by digest only, no shared writable tmp between jobs. Mention Clinejection in the threat model |
| G3 | **README's "agent-written code you run outside actionbox" → name the concrete handoffs** | A7, A5, Level1Techs `.git/hooks` note | List in "does not protect against": `.git/hooks`, `.vscode/tasks.json`/`settings.json`, `.idea` run configs, `.envrc` (direnv), `mise.toml`/`.tool-versions`, `.mvn/extensions.xml` and `mvnw` wrapper when *you* build, IDE Maven/Gradle import. Recommend Fence write-denies for `.git/hooks`, `.git/config`, `.vscode`, `.idea`, `.envrc`, `.mcp.json`, `.claude/` (srt already denies some; check Fence) |
| G4 | **Append-only broker event log** | sbx live egress log; MCP gateway practice; fnox audit | Log every `run_action` (action, params, definitions commit, exit code, duration), every fetch request/decision with coordinates and file hashes. Optionally hash-chain it. Cheap; supports post-incident review |
| G5 | **Fetch = the only egress; treat coordinates as outbound data** | A6, A13, PromptArmor npm-as-upload | Fetch only from registries in owner config, never URLs, git or tarball deps from coordinates. Show coordinates verbatim and flag unusual ones (long or high-entropy names, new groupIds, versions released recently) in `actionbox fetch`. Note: coordinates are an agent-to-registry channel (low bandwidth, the attacker needs registry log access) |
| G6 | **Fetch runs no package code on the host** | A9 (Nx postinstall), Shai-Hulud (2025-11) | `--ignore-scripts` / no lifecycle scripts, and no Maven plugin execution, during fetch (check npm-fetch.md covers this). Fetch env scrubbed except the registry credential |
| G7 | **Combination with other MCP servers** | A10 toxic flows, Rule of Two | Threat-model note: actionbox output is untrusted text. If the agent also has a write-external MCP server (GitHub, Slack), output injection becomes a toxic flow. Recommend users run actionbox sessions without such servers, or accept the risk |
| G8 | **Agent env inherited from terminal** | Formal | Doc: start Claude Code/Fence with a scrubbed env; actionbox can't fix this but `conformance` could warn if the shim sees variables matching `*TOKEN*`/`*KEY*` |
| G9 | Secret scanning of job output | Docker MCP gateway, Codex redaction | **Low value, skip for now.** Jobs hold no secrets beyond project files the agent can already read. Revisit if actions ever get credentials |
| G10 | Prompt-injection filtering of output | Willison: detection "not a useful security defense" | **Don't.** Keep the cap and the existing doc line |

**Validated choices, with outside evidence.**
- Fully offline jobs: avoids A1–A4, A6 and A13 entirely.
- No `excludedCommands` route: that route is buggy and runs on the host.
- Argv templates with full-match params and no leading `-`: avoids the Codex `git show` bug.
- Signed definitions with a rollback guard: avoids A5-style self-approval.
- Approval only at signing and fetch: avoids prompt fatigue.
- Container runtime private to each job, no host socket: avoids the shared Docker-socket escape in A7 and the OpenHands socket-mount problem.

## Not found

- No public experience report of Testcontainers inside a *local* bwrap/Landlock agent sandbox without excluding Docker. Only microVM (sbx), socket proxies, or CI.
- No Lobsters/HN thread surfaced via search on this exact combination. Hacker News threads were not searchable through the tools available.
- No first-party Ona/Coder engineering posts on agent network policy (only third-party summaries).
