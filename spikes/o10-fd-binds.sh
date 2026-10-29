#!/usr/bin/env bash
# O10 spike: do fds survive `podman unshare` into bwrap --bind-fd / --ro-bind-fd (R26)?
# Run on the host as the owner, no sudo. Optional: BWRAP=/path/to/bwrap.
set -euo pipefail

MIN_BWRAP=0.13.1
BWRAP_REV=362936aea0d5c6e70f7af75d2d44b5a914cb4ba2 # tag v0.13.1

WORK=$(mktemp -d "${TMPDIR:-/tmp}/o10-spike.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

PASSES=0
FAILS=0
pass() { echo "PASS  $*"; PASSES=$((PASSES + 1)); }
fail() { echo "FAIL  $*"; FAILS=$((FAILS + 1)); }
info() { echo "INFO  $*"; }
section() { printf '\n== %s\n' "$*"; }

at_least_min() { [[ -n $1 && $(printf '%s\n%s\n' "$MIN_BWRAP" "$1" | sort -V | head -n1) == "$MIN_BWRAP" ]]; }
version_ok() { at_least_min "$("$1" --version 2>/dev/null | awk '{print $2}')"; }

nix_cmd() { nix --extra-experimental-features 'nix-command flakes' "$@"; }

# nixpkgs ships bubblewrap 0.12.0 (unstable + 26.05), so override src to v0.13.1
NIX_EXPR=$(cat <<EOF
let pkgs = (builtins.getFlake "nixpkgs").legacyPackages.\${builtins.currentSystem};
in pkgs.bubblewrap.overrideAttrs (_: {
  version = "$MIN_BWRAP";
  src = builtins.fetchGit {
    url = "https://github.com/containers/bubblewrap";
    ref = "refs/tags/v$MIN_BWRAP";
    rev = "$BWRAP_REV";
  };
})
EOF
)

print_bwrap_instructions() {
  cat <<EOF >&2
No bwrap >= $MIN_BWRAP found. Either:

  1) Nix (no system change):
     mkdir -p ~/.local/share/actionbox
     nix --extra-experimental-features 'nix-command flakes' build --impure \\
       --out-link ~/.local/share/actionbox/bwrap --expr '$NIX_EXPR'
     BWRAP=~/.local/share/actionbox/bwrap/bin/bwrap $0

  2) Local build (Ubuntu 22.04 meson 0.61 >= required 0.49):
     sudo apt install meson ninja-build gcc pkg-config libcap-dev python3
     git clone --branch v$MIN_BWRAP https://github.com/containers/bubblewrap
     cd bubblewrap && meson setup _build -Dman=disabled -Dtests=false -Dselinux=disabled
     ninja -C _build && BWRAP=\$PWD/_build/bwrap $0
EOF
}

locate_bwrap() {
  if [[ -n ${BWRAP:-} ]]; then
    version_ok "$BWRAP" && return 0
    echo "BWRAP=$BWRAP is not >= $MIN_BWRAP" >&2
    exit 2
  fi
  if command -v nix >/dev/null; then
    local nixpkgs_version
    nixpkgs_version=$(nix_cmd eval --raw nixpkgs#bubblewrap.version 2>/dev/null || true)
    info "nixpkgs#bubblewrap version: ${nixpkgs_version:-unknown}"
    if at_least_min "$nixpkgs_version"; then
      BWRAP=$(nix_cmd build --no-link --print-out-paths nixpkgs#bubblewrap)/bin/bwrap
      version_ok "$BWRAP" && return 0
    fi
    info "building bubblewrap v$MIN_BWRAP via nix override (first run takes a minute)"
    if nix_cmd build --impure --out-link "$WORK/bwrap" --expr "$NIX_EXPR" >&2; then
      BWRAP=$(readlink -f "$WORK/bwrap")/bin/bwrap
      version_ok "$BWRAP" && return 0
    fi
  fi
  print_bwrap_instructions
  exit 2
}

# usage: with_opath_fd FD PATH CMD... ; runs CMD with an O_PATH|O_NOFOLLOW dir fd at FD, like the broker
with_opath_fd() {
  python3 -I -c '
import os, sys
fd = os.open(sys.argv[2], os.O_PATH | os.O_NOFOLLOW | os.O_DIRECTORY)
os.dup2(fd, int(sys.argv[1]), inheritable=True)
os.execvp(sys.argv[3], sys.argv[3:])
' "$@"
}

for tool in podman python3; do
  command -v "$tool" >/dev/null || { echo "missing: $tool" >&2; exit 2; }
done
locate_bwrap
BWRAP=$(readlink -f "$BWRAP")

# --ro-bind / / is ro, so /mnt/* dests need a tmpfs to be creatable
SANDBOX=("$BWRAP" --ro-bind / / --dev /dev --unshare-pid --proc /proc --tmpfs /mnt)

REPO=$WORK/repo
VICTIM=$WORK/victim
mkdir -p "$REPO/target" "$REPO/opath" "$REPO/ro" "$REPO/race" "$REPO/fallback" "$VICTIM"
echo hello >"$REPO/ro/f"

section "versions"
podman --version
"$BWRAP" --version
echo "bwrap path: $BWRAP"
echo "kernel: $(uname -r)"
PAUSE_PID_FILE=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/libpod/tmp/pause.pid
if [[ -f $PAUSE_PID_FILE ]] && kill -0 "$(cat "$PAUSE_PID_FILE")" 2>/dev/null; then
  info "pause process running: podman unshare joins via the constructor shortcut (no fork)"
else
  info "no pause process: first podman unshare re-execs via clone and creates one"
fi
echo "uid_map inside podman unshare:"
podman unshare -- cat /proc/self/uid_map
echo "gid_map inside podman unshare:"
podman unshare -- cat /proc/self/gid_map

section "check 1: inherited fds survive podman unshare"
exec 7<"$REPO/target"
fds_inside=$(podman unshare -- ls -l /proc/self/fd)
echo "$fds_inside"
if [[ $(podman unshare -- readlink /proc/self/fd/7 2>/dev/null || true) == "$REPO/target" ]]; then
  pass "1a  O_RDONLY fd 7 visible inside podman unshare"
  FD_SURVIVES=1
else
  fail "1a  O_RDONLY fd 7 not inherited by podman unshare"
  FD_SURVIVES=0
fi
if [[ $(with_opath_fd 8 "$REPO/opath" podman unshare -- readlink /proc/self/fd/8 2>/dev/null || true) == "$REPO/opath" ]]; then
  pass "1b  O_PATH fd 8 visible inside podman unshare"
else
  fail "1b  O_PATH fd 8 not inherited by podman unshare"
  FD_SURVIVES=0
fi

section "check 2: bind-fd through podman unshare"
if podman unshare -- "${SANDBOX[@]}" --bind-fd 7 /mnt/w -- \
  sh -c 'touch /mnt/w/x && test ! -e /proc/self/fd/7' && [[ -f $REPO/target/x ]]; then
  pass "2a  --bind-fd (O_RDONLY fd): write landed in $REPO/target, fd not leaked into sandbox"
else
  fail "2a  --bind-fd (O_RDONLY fd)"
fi
if with_opath_fd 8 "$REPO/opath" podman unshare -- "${SANDBOX[@]}" --bind-fd 8 /mnt/w -- \
  sh -c 'touch /mnt/w/x' && [[ -f $REPO/opath/x ]]; then
  pass "2b  --bind-fd (O_PATH fd): write landed in $REPO/opath"
else
  fail "2b  --bind-fd (O_PATH fd)"
fi
if with_opath_fd 8 "$REPO/ro" podman unshare -- "${SANDBOX[@]}" --ro-bind-fd 8 /mnt/r -- \
  sh -c 'grep -q hello /mnt/r/f && ! touch /mnt/r/y 2>/dev/null' && [[ ! -e $REPO/ro/y ]]; then
  pass "2c  --ro-bind-fd (O_PATH fd): readable, write denied"
else
  fail "2c  --ro-bind-fd (O_PATH fd)"
fi
exec 7<&-

section "check 3: path swapped after fd open (R7 race)"
with_opath_fd 8 "$REPO/race" bash -c '
  set -euo pipefail
  repo=$1 victim=$2; shift 2
  mv "$repo/race" "$repo/race.orig"
  ln -s "$victim" "$repo/race"
  podman unshare -- "$@" --bind-fd 8 /mnt/w -- sh -c "touch /mnt/w/raced"
' bash "$REPO" "$VICTIM" "${SANDBOX[@]}" || true
if [[ -f $REPO/race.orig/raced && ! -e $VICTIM/raced ]]; then
  pass "3a  write landed in original dir (race.orig), not in symlink target"
else
  fail "3a  original: $(ls -A "$REPO/race.orig" 2>&1), victim: $(ls -A "$VICTIM" 2>&1)"
fi
podman unshare -- "${SANDBOX[@]}" --bind "$REPO/race" /mnt/w -- sh -c 'touch /mnt/w/by-path' || true
if [[ -f $VICTIM/by-path ]]; then
  info "3b  path-based --bind followed the swapped symlink into victim (the race --bind-fd closes)"
else
  info "3b  path-based --bind did not reach victim"
fi

section "check 4: fallback, fd opened inside the namespace (engine-exec)"
if podman unshare -- sh -c 'exec 7<"$1"; shift; exec "$@"' sh "$REPO/fallback" \
  "${SANDBOX[@]}" --bind-fd 7 /mnt/w -- sh -c 'touch /mnt/w/x' && [[ -f $REPO/fallback/x ]]; then
  pass "4   fd opened inside podman unshare, --bind-fd write landed in $REPO/fallback"
else
  fail "4   fallback --bind-fd"
fi

section "summary"
echo "passed: $PASSES, failed: $FAILS"
if ((FD_SURVIVES)) && ((FAILS == 0)); then
  echo "O10: fds survive podman unshare into bwrap; R26 works as specified, no engine-exec needed"
elif ((!FD_SURVIVES)); then
  echo "O10: fds do NOT survive podman unshare; use engine-exec fallback (§8.8) if check 4 passed"
else
  echo "O10: fds survive, but a bind check failed; inspect output above"
fi
((FAILS == 0))
