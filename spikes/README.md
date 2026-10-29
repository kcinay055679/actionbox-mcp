# Spikes

Throwaway host scripts that close open items in `spec.md` §11. Run on the host as the owner, no sudo. Each script works in its own `mktemp -d` dir and removes it on exit.

## `o10-fd-binds.sh` (closes O10, part of O3)

Question: do the broker's fds survive `podman unshare` into bwrap `--bind-fd` / `--ro-bind-fd` (R26, §8.3 "Symlinks", §8.8)?

```bash
./spikes/o10-fd-binds.sh                     # finds or builds bwrap >= 0.13.1 via nix
BWRAP=/path/to/bwrap ./spikes/o10-fd-binds.sh
```

Needs `podman`, `python3`, and either `nix` or a `BWRAP` >= 0.13.1. nixpkgs only has bubblewrap 0.12.0, so the script builds v0.13.1 via an `overrideAttrs` (pinned to the tag's commit). Without nix it prints the manual meson build steps and exits 2.

| Check | Proves |
|---|---|
| versions | podman, bwrap path and version, kernel, `uid_map`/`gid_map` inside `podman unshare`, whether the pause process already ran |
| 1a / 1b | an inherited `O_RDONLY` (bash `exec 7<`) and `O_PATH\|O_NOFOLLOW` (python helper, same flags as the broker) dir fd is still open inside `podman unshare` |
| 2a | `--bind-fd` from a host-opened fd works through `podman unshare`; the write lands in the original dir; bwrap doesn't leak the fd into the sandbox |
| 2b / 2c | same with an `O_PATH` fd; `--ro-bind-fd` is readable and denies writes |
| 3a | R7 race: fd opened, dir renamed, symlink to `victim` put in its place, then bound via fd => write lands in the renamed original, not in `victim` |
| 3b | info only: a path-based `--bind` of the same path follows the symlink into `victim` (the race that `--bind-fd` closes) |
| 4 | fallback (§8.8 `engine-exec`): fd opened inside `podman unshare`, then `--bind-fd` works. Always runs |

Outcome:
- all PASS => O10 closed, R26 works as specified, no `engine-exec` needed.
- 1a/1b FAIL, 4 PASS => fds don't survive; implement the `engine-exec` fallback.
- 1 PASS, 2 FAIL => fds survive but bwrap can't bind them across podman's user/mount namespace; check bwrap's error and use the fallback.

Note: the first `podman unshare` in the script starts the pause process if none runs, so checks 1–4 always go through podman's "join the pause process" path (the one the broker hits in practice). The no-pause re-exec path keeps fds too, per source, but isn't tested separately: killing the pause process would disturb running containers.
