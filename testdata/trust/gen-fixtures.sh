#!/usr/bin/env bash
# A3 trust fixtures (spec §8.4, §9). Usage: gen-fixtures.sh OUTDIR
# Writes OUTDIR/<case>/ repos, OUTDIR/keys/*.gpg keyrings, OUTDIR/cases.json.
# Needs git, gpg (2.2+), python3. No network.
set -euo pipefail
# commit() runs inside $(...): without this a failed git commit is ignored
shopt -s inherit_errexit

[[ $# -eq 1 ]] || { echo "usage: $0 OUTDIR" >&2; exit 2; }
mkdir -p "$1"
OUT=$(cd "$1" && pwd)
[[ -z "$(ls -A "$OUT")" ]] || { echo "$OUT is not empty" >&2; exit 2; }
mkdir -p "$OUT/keys" "$OUT/bin" "$OUT/markers"
mkdir -m 700 "$OUT/gnupg"

# gpg-agent socket path max ~107 bytes: long OUTDIR => short symlink
export GNUPGHOME=$OUT/gnupg
GNUPG_LINK=
if (( ${#GNUPGHOME} > 80 )); then
  GNUPG_LINK=$(mktemp -u "${TMPDIR:-/tmp}/agf.XXXXXX")
  ln -s "$GNUPGHOME" "$GNUPG_LINK"
  export GNUPGHOME=$GNUPG_LINK
fi
cleanup() {
  gpgconf --kill gpg-agent 2>/dev/null || true
  if [[ -n $GNUPG_LINK ]]; then rm -f "$GNUPG_LINK"; fi
}
trap cleanup EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=Fixture GIT_AUTHOR_EMAIL=fixture@test
export GIT_COMMITTER_NAME=Fixture GIT_COMMITTER_EMAIL=fixture@test
export GIT_AUTHOR_DATE='2026-01-01T00:00:00Z' GIT_COMMITTER_DATE='2026-01-01T00:00:00Z'

GOOD_TOML='schema_version = 1

[actions.lint]
description = "Lint"
invoker     = "agent"
argv        = ["true"]
writes      = []
'
GOOD_TOML_V2="${GOOD_TOML}
[actions.test]
description = \"Test\"
invoker     = \"agent\"
argv        = [\"true\"]
writes      = []
"
EVIL_TOML='schema_version = 1

[actions.lint]
description = "Lint"
invoker     = "agent"
argv        = ["sh", "-c", "echo forged"]
writes      = []
'

# --- keys --------------------------------------------------------------------

fpr_of() { gpg --list-keys --with-colons "$1" 2>/dev/null | awk -F: '/^fpr/ { print $10; exit }'; }

gen_key() { # uid usage expiry [extra gpg args...]
  local uid=$1 usage=$2 expiry=$3; shift 3
  gpg --batch --passphrase '' "$@" --quick-gen-key "$uid" ed25519 "$usage" "$expiry" 2>/dev/null
}

gen_key 'Owner <owner@test>' sign never
gen_key 'Foreign <foreign@test>' sign never
gen_key 'Subowner <subowner@test>' cert never
gpg --batch --passphrase '' --quick-add-key "$(fpr_of subowner@test)" ed25519 sign never 2>/dev/null
gen_key 'Expired <expired@test>' sign 1d --faked-system-time 20250101T000000
gen_key 'Revoked <revoked@test>' sign never

OWNER=$(fpr_of owner@test)
FOREIGN=$(fpr_of foreign@test)
SUBOWNER=$(fpr_of subowner@test)
EXPIRED=$(fpr_of expired@test)
REVOKED=$(fpr_of revoked@test)

# signs while the expired key was still valid
printf '#!/bin/sh\nexec gpg --faked-system-time 20250101T120000 "$@"\n' > "$OUT/bin/gpg-past"
chmod +x "$OUT/bin/gpg-past"

# --- helpers -----------------------------------------------------------------

CASES=()

add_case() { # name expect reason pin keyring trusted marker
  local trusted=null marker=null
  [[ -n $6 ]] && trusted="\"$6\""
  [[ -n $7 ]] && marker="\"markers/$7\""
  CASES+=("$(printf '  {"name": "%s", "repo": "%s", "expect": "%s", "reason": %s, "pin": "%s", "keyring": "keys/%s.gpg", "trusted": %s, "marker": %s}' \
    "$1" "$1" "$2" "$( [[ -n $3 ]] && printf '"%s"' "$3" || echo null)" "$4" "$5" "$trusted" "$marker")")
}

new_repo() { # name [git init args...]
  local r=$OUT/$1; shift
  git init -q -b main "$@" "$r"
  printf '%s' "$GOOD_TOML" > "$r/.agent-actions.toml"
  echo "$r"
}

commit() { # repo signing-fpr|"" message [gpg-program]
  local r=$1 key=$2 msg=$3 prog=${4:-gpg}
  git -C "$r" add -A
  if [[ -n $key ]]; then
    git -C "$r" -c commit.gpgsign=true -c user.signingkey="$key" -c gpg.program="$prog" \
      commit -q --allow-empty -m "$msg"
  else
    git -C "$r" -c commit.gpgsign=false commit -q --allow-empty -m "$msg"
  fi
  git -C "$r" rev-parse HEAD
}

write_loose() { # repo oid type file: overwrite loose object, header + zlib like git
  python3 -I - "$@" <<'PY'
import os, sys, zlib
repo, oid, typ, src = sys.argv[1:5]
data = open(src, 'rb').read()
path = os.path.join(repo, '.git', 'objects', oid[:2], oid[2:])
os.makedirs(os.path.dirname(path), exist_ok=True)
if os.path.exists(path):
    os.chmod(path, 0o644)
with open(path, 'wb') as f:
    f.write(zlib.compress(typ.encode() + b' %d\0' % len(data) + data))
PY
}

fake_program() { # path marker [stdout-lines...]: touches marker, prints lines, exits 0
  local path=$1 marker=$2; shift 2
  { echo '#!/bin/sh'
    echo "touch '$OUT/markers/$marker'"
    for l in "$@"; do printf "echo '%s'\n" "$l"; done
    echo 'exit 0'; } > "$path"
  chmod +x "$path"
}

# A (v1) <- B (v2) <- D (v3), all owner-signed; echoes "A B D"
linear_history() {
  local r=$1 a b d
  a=$(commit "$r" "$OWNER" A)
  printf '%s' "$GOOD_TOML_V2" > "$r/.agent-actions.toml"; b=$(commit "$r" "$OWNER" B)
  printf '%s# v3\n' "$GOOD_TOML_V2" > "$r/.agent-actions.toml"; d=$(commit "$r" "$OWNER" D)
  echo "$a $b $d"
}

for k in owner:$OWNER foreign:$FOREIGN subowner:$SUBOWNER expired:$EXPIRED revoked:$REVOKED; do
  gpg --export "${k#*:}" > "$OUT/keys/${k%%:*}.gpg"
done

# --- passing cases -------------------------------------------------------------

r=$(new_repo signed-clean); commit "$r" "$OWNER" init >/dev/null
add_case signed-clean pass "" "$OWNER" owner "" ""

r=$(new_repo subkey-signed); commit "$r" "$SUBOWNER" init >/dev/null
add_case subkey-signed pass "" "$SUBOWNER" subowner "" ""

# unsigned HEAD not touching the definitions file: C = the signed commit below it
r=$(new_repo unsigned-head-other-file); commit "$r" "$OWNER" init >/dev/null
echo x > "$r/other.txt"; commit "$r" "" "unsigned, other file" >/dev/null
add_case unsigned-head-other-file pass "" "$OWNER" owner "" ""

r=$(new_repo accepted-descendant); read -r _ b _ <<<"$(linear_history "$r")"
add_case accepted-descendant pass "" "$OWNER" owner "$b" ""

# repo gpg program that would reject everything; must not run
r=$(new_repo gpg-program-owner); commit "$r" "$OWNER" init >/dev/null
printf '#!/bin/sh\ntouch %q\nexit 1\n' "$OUT/markers/gpg-program-owner" > "$OUT/bin/fail-gpg"; chmod +x "$OUT/bin/fail-gpg"
git -C "$r" config gpg.program "$OUT/bin/fail-gpg"
git -C "$r" config gpg.openpgp.program "$OUT/bin/fail-gpg"
add_case gpg-program-owner pass "" "$OWNER" owner "" gpg-program-owner

# --- failing cases -------------------------------------------------------------

r=$(new_repo uncommitted-edit); commit "$r" "$OWNER" init >/dev/null
printf '%s' "$EVIL_TOML" > "$r/.agent-actions.toml"
add_case uncommitted-edit fail "uncommitted edits" "$OWNER" owner "" ""

r=$(new_repo unsigned-commit); commit "$r" "" init >/dev/null
add_case unsigned-commit fail "signature status" "$OWNER" owner "" ""

r=$(new_repo foreign-key); commit "$r" "$FOREIGN" init >/dev/null
add_case foreign-key fail "foreign key" "$OWNER" owner "" ""

# gpgv exits 0 with VALIDSIG here; only EXPKEYSIG/REVKEYSIG instead of GOODSIG tells
r=$(new_repo expired-key); commit "$r" "$EXPIRED" init "$OUT/bin/gpg-past" >/dev/null
add_case expired-key fail "signature status" "$EXPIRED" expired "" ""

r=$(new_repo revoked-key); commit "$r" "$REVOKED" init >/dev/null
sed 's/^:-----/-----/' "$GNUPGHOME/openpgp-revocs.d/$REVOKED.rev" | gpg --batch --import 2>/dev/null
gpg --export "$REVOKED" > "$OUT/keys/revoked.gpg"
add_case revoked-key fail "signature status" "$REVOKED" revoked "" ""

# replace ref => porcelain shows forged blob; broker ignores it and sees a working-file diff
r=$(new_repo replace-ref); commit "$r" "$OWNER" init >/dev/null
printf '%s' "$EVIL_TOML" > "$r/.agent-actions.toml"
git -C "$r" replace "$(git -C "$r" rev-parse HEAD:.agent-actions.toml)" "$(git -C "$r" hash-object -w .agent-actions.toml)"
add_case replace-ref fail "uncommitted edits" "$OWNER" owner "" ""

r=$(new_repo grafts); commit "$r" "$OWNER" init >/dev/null
echo x > "$r/other.txt"; head=$(commit "$r" "$OWNER" second)
echo "$head" > "$r/.git/info/grafts"
add_case grafts fail "grafts present" "$OWNER" owner "" ""

r=$(new_repo sha256-repo --object-format=sha256); commit "$r" "$OWNER" init >/dev/null
add_case sha256-repo fail "unsupported object format" "$OWNER" owner "" ""

r=$(new_repo rollback); read -r _ b d <<<"$(linear_history "$r")"
git -C "$r" checkout -q --detach "$b"
add_case rollback fail "rollback" "$OWNER" owner "$d" ""

r=$(new_repo forged-blob); commit "$r" "$OWNER" init >/dev/null
printf '%s' "$EVIL_TOML" > "$r/.agent-actions.toml"
write_loose "$r" "$(git -C "$r" rev-parse HEAD:.agent-actions.toml)" blob "$r/.agent-actions.toml"
add_case forged-blob fail "hash mismatch" "$OWNER" owner "" ""

r=$(new_repo forged-tree); commit "$r" "$OWNER" init >/dev/null
printf '%s' "$EVIL_TOML" > "$r/.agent-actions.toml"
evil_blob=$(git -C "$r" hash-object -w .agent-actions.toml)
python3 -I -c 'import sys; sys.stdout.buffer.write(b"100644 .agent-actions.toml\0" + bytes.fromhex(sys.argv[1]))' \
  "$evil_blob" > "$OUT/forged-tree.body"
write_loose "$r" "$(git -C "$r" rev-parse 'HEAD^{tree}')" tree "$OUT/forged-tree.body"
rm "$OUT/forged-tree.body"
add_case forged-tree fail "hash mismatch" "$OWNER" owner "" ""

# rollback to B, root A forged to claim TRUSTED D as parent so a naive walk finds D
r=$(new_repo forged-intermediate-commit); read -r a b d <<<"$(linear_history "$r")"
git -C "$r" checkout -q --detach "$b"
git -C "$r" cat-file commit "$a" | sed "1a parent $d" > "$OUT/forged-commit.body"
write_loose "$r" "$a" commit "$OUT/forged-commit.body"
rm "$OUT/forged-commit.body"
add_case forged-intermediate-commit fail "hash mismatch" "$OWNER" owner "$d" ""

# git -c gpg.program=gpg overrides this, but gpgv must not depend on that
r=$(new_repo gpg-program-foreign); commit "$r" "$FOREIGN" init >/dev/null
fake_program "$OUT/bin/fake-gpg" gpg-program-foreign \
  '[GNUPG:] NEWSIG' \
  "[GNUPG:] GOODSIG ${OWNER: -16} Owner <owner@test>" \
  "[GNUPG:] VALIDSIG $OWNER 2026-01-01 1767225600 0 4 0 22 8 00 $OWNER" \
  '[GNUPG:] TRUST_ULTIMATE 0 pgp'
git -C "$r" config gpg.openpgp.program "$OUT/bin/fake-gpg"
add_case gpg-program-foreign fail "foreign key" "$OWNER" owner "" gpg-program-foreign

# SSH-armored gpgsig => git runs repo gpg.ssh.program even with -c gpg.program=gpg
r=$(new_repo ssh-program); commit "$r" "" base >/dev/null
tree=$(git -C "$r" rev-parse 'HEAD^{tree}')
forged=$(printf 'tree %s\nauthor Fixture <fixture@test> 1767225600 +0000\ncommitter Fixture <fixture@test> 1767225600 +0000\ngpgsig -----BEGIN SSH SIGNATURE-----\n U1NIU0lH\n -----END SSH SIGNATURE-----\n\nssh-signed\n' "$tree" \
  | git -C "$r" hash-object -t commit -w --stdin)
git -C "$r" update-ref refs/heads/main "$forged"
fake_program "$OUT/bin/fake-ssh" ssh-program 'Good "git" signature for owner@test with ED25519 key SHA256:fixture'
echo 'owner@test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixture' > "$r/.git/allowed_signers"
git -C "$r" config gpg.ssh.program "$OUT/bin/fake-ssh"
git -C "$r" config gpg.ssh.allowedSignersFile "$r/.git/allowed_signers"
add_case ssh-program fail "signature status" "$OWNER" owner "" ssh-program

# textconv shows only line 1 => masks the edit for git diff --quiet; must not run
r=$(new_repo textconv); commit "$r" "$OWNER" init >/dev/null
echo '.agent-actions.toml diff=evil' > "$r/.gitattributes"
commit "$r" "$OWNER" gitattributes >/dev/null
git -C "$r" config diff.evil.textconv "sh -c 'touch $OUT/markers/textconv; head -n 1 \"\$0\"'"
printf '%s' "$EVIL_TOML" > "$r/.agent-actions.toml"
add_case textconv fail "uncommitted edits" "$OWNER" owner "" textconv

r=$(new_repo symlink-committed)
mv "$r/.agent-actions.toml" "$r/defs.toml"; ln -s defs.toml "$r/.agent-actions.toml"
commit "$r" "$OWNER" init >/dev/null
add_case symlink-committed fail "symlink" "$OWNER" owner "" ""

r=$(new_repo symlink-worktree); commit "$r" "$OWNER" init >/dev/null
mv "$r/.agent-actions.toml" "$r/defs.toml"; ln -s defs.toml "$r/.agent-actions.toml"
add_case symlink-worktree fail "symlink" "$OWNER" owner "" ""

r=$(new_repo mode-100755); chmod +x "$r/.agent-actions.toml"
git -C "$r" -c core.fileMode=true add .agent-actions.toml
commit "$r" "$OWNER" init >/dev/null
add_case mode-100755 fail "file mode" "$OWNER" owner "" ""

# --- manifest ----------------------------------------------------------------

{
  echo '{'
  echo "  \"owner\": \"$OWNER\","
  echo '  "cases": ['
  for i in "${!CASES[@]}"; do
    sep=,; (( i == ${#CASES[@]} - 1 )) && sep=
    echo "  ${CASES[$i]}$sep"
  done
  echo '  ]'
  echo '}'
} > "$OUT/cases.json"

echo "wrote ${#CASES[@]} cases to $OUT"
