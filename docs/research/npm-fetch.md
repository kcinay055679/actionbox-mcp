# npm fetch guards (O12)

- **Checked:** 2026-10-28.
- **Target:** npm **v12.2.1** (latest v12 tag, 2026-10-14). Source: shallow clone of `github.com/npm/cli` at that tag. Its bundled deps (`node_modules/`) are checked into the repo, so pacote 22.0.0, npm-pick-manifest 12.0.0, make-fetch-happen 16.0.1 and cacache 21.0.1 are cited at the same tag.
- **Base URL:** `B = https://github.com/npm/cli/blob/v12.2.1`. Every `[src]` below is `B/<path>#L<n>`.
- **[obs-11]:** run locally with npm 11.19.0 against a scratch cache (one small registry fetch, no installs). The code paths involved match v12.2.1; only the defaults differ.

## 1. Config definitions (npm 12.2.1)

Source: `B/workspaces/config/lib/definitions/definitions.js`.

| Name | Type / values | Unit | Default v12 | Default v11.19 | Introduced | Line |
|---|---|---|---|---|---|---|
| `min-release-age` | `[null, Number]`, hint `<days>` | **days** (Number, so fractions work; `0` → off) | `null` (off) | `null` | **11.10.0** | L1541 |
| `before` | `[null, Date]` | absolute date | `null` | `null` | old | L337 |
| `min-release-age-exclude` | `[Array, String]`, names/globs | – | `[]` | `[]` | 11.17.0 | L1577 |
| `allow-git` | `all \| none \| root` | – | **`none`** | `all` | 11.9.0 | L227 |
| `allow-remote` | `all \| none \| root` | – | **`none`** | `all` | 11.14.0 | L247 |
| `allow-file` | `all \| none \| root` | – | `all` | `all` | 11.14.0 | L212 |
| `allow-directory` | `all \| none \| root` | – | `all` | `all` | 11.14.0 | L197 |
| `allow-scripts` | `[String, Array]`, package list | – | `''` | `''` | 11.16.0 | L270 |

Notes:
- There is no `minimum-release-age`. The name is `min-release-age`. Its `flatten` turns it into `flatOptions.before = now - age * 86400000 ms`, unless `before` is set in the same source (L1568–L1574). Across sources the usual precedence applies: cli > env > project > user > global. So a CLI flag overrides `before` set in `~/.npmrc`.
- "Introduced" means the first tag whose `definitions.js` contains the key, checked at `raw.githubusercontent.com/npm/cli/<tag>/…` for v11.9.0–v11.18.0. The v12 defaults come from CHANGELOG 12.0.0: "allow-git and allow-remote now default to "none"" (`B/CHANGELOG.md#L109`, PR #9466).
- `allow-remote=none` does not block a tarball on the configured registry's host (L254–L259). It targets URL dependencies.
- `allow-scripts` is irrelevant for `cache add`, which runs no lifecycle scripts. In v12, dependency install scripts are default-deny anyway (`B/CHANGELOG.md#L112`).
- **CLI and env.** Every definition works as `--name=value`, e.g. `--min-release-age=7` and `--allow-git=none`. Every definition can also be set through `npm_config_<name_with_underscores>`, e.g. `npm_config_min_release_age=7` ([doc] `B/docs/lib/content/using-npm/config.md#L33-L40`; [obs-11] env → `7`). `envExport: false` on `min-release-age`, `-exclude` and `allow-scripts` only stops npm from *re-exporting* them to lifecycle-script environments (`B/workspaces/config/lib/set-envs.js#L71-L73`). It does not stop npm from reading them.
- **Invalid values fail open.** `--min-release-age=7d` gives `npm warn invalid config … Must be one of: null, numeric value`. The value falls back to `null` (no age filter) and the command exits 0. `--allow-git=bogus` falls back to the default the same way. [obs-11] So actionbox must validate the config value itself, e.g. as a positive integer.

## 2. Does `npm cache add` honor them? Yes, for the spec itself

`cache add` (`B/lib/commands/cache.js#L164-L184`) calls `pacote.tarball.stream(spec, …, { ...flatOptions, _isRoot: true })` and then `pacote.manifest(spec, { ...flatOptions, fullMetadata: true, _isRoot: true })`. Both go through pacote:

- **allow-\*:** `FetcherBase.get` calls `canUse({ allow: opts.allowGit | allowRemote | allowFile | allowDirectory, isRoot: opts._isRoot })` for every spec type (`B/node_modules/pacote/lib/fetcher.js#L477-L520`). `none` throws `EALLOWGIT` / `EALLOWREMOTE` / …. `root` allows the spec, because `cache add` passes `_isRoot: true`. So use `none`, not `root`.
- **min-release-age:** `RegistryFetcher.manifest()` passes `before: this.before` to `pickManifest` (`B/node_modules/pacote/lib/registry.js#L129-L135`). For an exact version, `pickManifest` returns that version only if `time[ver] <= before`; otherwise it returns null (`B/node_modules/npm-pick-manifest/lib/index.js#L108-L111`), which throws `ETARGET` "No matching version found for x@1.2.3 with a date before …" (L197–L215). Setting `before` also forces `fullMetadata` (`fetcher.js#L78-L79`), so the packument carries `time`. [obs-11] `npm cache add is-number@7.0.0 --min-release-age=100000` → `npm error notarget`.
- **Gaps:**
  - `isBefore` lets a version through when the packument has no `time` entry for it (`npm-pick-manifest/lib/index.js#L17-L18`). This only matters for odd registries.
  - `cache add` ignores `min-release-age-exclude`, which is only used in arborist, `pack` and `outdated`. This is harmless here.
- **Not transitive.** `cache add` fetches exactly one tarball plus its packument. Dependencies are not resolved. This matches the per-coordinate design in §8.9 (iterative round trips). It does not match Maven's `transitive=true`.

`cache add` is therefore the right command. The alternatives are worse:
- `npm pack` writes a `.tgz` into the cwd.
- `npm install --package-lock-only` fetches packuments only, not tarballs.
- A full `npm install <spec>` in a temp dir resolves its own tree under the age window. That tree can differ from the project's lockfile, so it would not match what `npm ci` needs.

## 3. `npm ci --offline --cache <dir>` and a read-only cache (O11)

From the code:
- `--offline` → make-fetch-happen cache mode `only-if-cached` (`B/node_modules/npm-registry-fetch/lib/index.js#L207-L212`). A hit only responds; the store/revalidate branches are not reached (`B/node_modules/make-fetch-happen/lib/cache/index.js#L6-L37`).
- With a lockfile that has `resolved` and `integrity`, pacote reads the tarball straight by digest: `cacache.get.stream.byDigest` (`fetcher.js#L194-L204`, L296–L302). This is a read only, with no index lookup and no tmp file.
- Cache writes happen only on errors: `EINTEGRITY` → `cacache.rm.content` and index invalidation (`make-fetch-happen/lib/cache/entry.js#L344-L356`, `fetcher.js#L345-L347`).
- npm's own writes *beside* `_cacache`, all non-fatal:
  - `mkdir -p <cache>`, error caught (`B/lib/npm.js#L134-L138`).
  - `<cache>/_logs/` when `logs-max > 0` (`B/lib/npm.js#L140-L145`, L465–L466). On failure npm prints "Log files were not written…".
  - `<cache>/_update-notifier-last-checked`, best-effort; skipped under CI or `--no-update-notifier` (`B/lib/cli/update-notifier.js#L15`, L95, L100–L114).

[obs-11]:
- Setup: built a lockfile for `is-number@7.0.0` from a cache filled by `cache add`, then ran `chmod -R a-w` on the cache.
- `npm ci --offline --cache <ro>`, run both with and without `--logs-max=0 --no-update-notifier`, gave "added 1 package", exit 0, and nothing written.
- Side finding: `npm install --package-lock-only --offline` *did* append an index line. Use `npm ci`, not `install`, in jobs.

**Conclusion:** a read-only npm cache works for `npm ci --offline` when every tarball is present. Pass `--logs-dir=<job tmp>` (or `--logs-max=0`) and `--no-update-notifier` so that nothing tries to write next to `_cacache`. Corrupted content then makes the job fail instead of self-healing, which is acceptable.

## 4. Integrity in the cacache index (R29 ledger)

- Index layout:
  - Files live under `<cache>/_cacache/index-v5/<h[0:2]>/<h[2:4]>/<h[4:]>`, where `h = sha256(key)` (`B/node_modules/cacache/lib/entry-index.js#L288-L300`).
  - Each line is `sha1(json)\tjson` and is append-only. The last line whose hash is valid wins (L115–L137, L252–L262).
  - The JSON has `key`, `integrity`, `time`, `size`, `metadata`.
- `integrity` is the SRI string of the stored body. For registry tarballs the algorithm follows `dist.integrity`, i.e. `sha512` (`B/node_modules/pacote/lib/remote.js#L34-L40`). Very old packages with only a `shasum` fall back to sha1.
- [obs-11] One `cache add is-number@7.0.0` wrote 3 entries:

  ```
  make-fetch-happen:request-cache:https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz  sha512-41Cifk…xng==  3730
  pacote:tarball:is-number@7.0.0                                                         sha512-41Cifk…xng==  3730
  make-fetch-happen:request-cache:https://registry.npmjs.org/is-number                   sha512-+8ovVS…ikg==  36183   (packument)
  ```

  The tarball integrity equals the lockfile `integrity`.
- **Reading it back:** `npm cache ls [<name>@<version>]` still exists in v12 (`B/lib/commands/cache.js#L209-L237`), but it prints **keys only**, without integrity. The docs say "There is currently no method exposed through npm to inspect or directly manage the contents of this cache. In order to access it, `cacache` must be used directly" (`B/docs/lib/content/commands/npm-cache.md`).
- **Ledger method:**
  1. Snapshot the `index-v5` lines before and after the fetch, the same way as the Maven tree diff.
  2. Parse the new lines and keep the entry whose key is `pacote:tarball:<spec>`, or the `make-fetch-happen:request-cache:…/-/<name>-<ver>.tgz` entry.
  3. Record `integrity` (sha512 SRI) and `size`.

  If the ledger must stay sha256-only, hash the content file instead. Its path `content-v2/sha512/<hex>` is derived from the same integrity.

## 5. Recommended §8.9 command

Run from the empty `mktemp -d` cwd. `<age>` is the validated integer from `npm_min_release_age`. Requires npm ≥ 11.14 for the flags to exist; on npm 12 the `allow-*` values equal the defaults and are kept explicit.

```
npm cache add <name>@<version> --cache <cache>/npm --min-release-age=<age> --allow-git=none --allow-remote=none --allow-file=none --allow-directory=none --logs-dir=<fetch tmp> --no-update-notifier
```

- `--cache <cache>/npm` → npm uses `<cache>/npm/_cacache` (`definitions.js#L433-L446`).
- `--logs-dir` keeps `_logs/` out of the project cache and out of the ledger diff.
- Before running, check `npm --version` (≥ 11.14, ideally 12.x): an older npm lacks these keys, so the guard would at best only warn and do nothing [inf].
- Failure modes stay `pending` with `detail`:
  - `ETARGET` "with a date before …" → the version is too new.
  - `EALLOW*` → the spec type is blocked (unreachable with the §8.9 regex).
