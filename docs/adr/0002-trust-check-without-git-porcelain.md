# Trust check uses raw objects and gpgv, not git porcelain

The agent can write `.git/objects`, `.git/refs`, `.gitattributes` and the working tree. Git doesn't re-hash objects on read, so the agent can forge a tree or blob behind a correctly signed commit. Git porcelain also runs commands configured in the repo (`textconv`, `diff.external`, the signing program).

The broker therefore does the check itself:

- uses git only to resolve object IDs, with `GIT_NO_REPLACE_OBJECTS=1`, `GIT_CONFIG_NOSYSTEM=1` and `GIT_CONFIG_GLOBAL=/dev/null`;
- re-hashes the commit, tree and blob;
- verifies the commit signature with `gpgv` against the pinned keyring;
- compares the working-tree definitions file byte-for-byte with the verified blob.

## Considered Options

- `git log --format=%G?` plus `git diff --quiet`, hardened with `-c` overrides (the original §8.4). Rejected: every repo-level setting that git honors would have to be overridden, and forged objects aren't detected at all. Reproduced on git 2.55.0 ([trust-attacks.md](../research/trust-attacks.md)):
  - A forged blob behind a signed commit passed both `%G?` and `git diff --quiet`.
  - `-c gpg.program=gpg` doesn't cover SSH- or X.509-armored signatures. Git then runs the repo's `gpg.ssh.program`, and a fake one got `%G?`=G.
  - `git diff --quiet` runs a repo-configured textconv driver, which can make a real edit look clean.
