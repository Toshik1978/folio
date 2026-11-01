#!/bin/sh
# Tests the beads git tooling against a fake `br` on PATH, so it runs where br is not
# installed — CI included. Exits 1 on any failure; run by `task test:beads`.
set -u
here=$(cd "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fails=0
fail() { echo "FAIL: $*"; fails=1; }

# fake_br VERSION DIRTY — a br that reports VERSION, DIRTY dirty issues, and logs every call.
fake_br() {
  mkdir -p "$work/bin"
  cat > "$work/bin/br" <<EOF
#!/bin/sh
echo "\$*" >> "$work/br.log"
case "\$1" in
  --version) echo "br $1" ;;
  sync) case "\$*" in *--status*) echo "  Dirty issues: $2" ;; esac ;;
esac
EOF
  chmod +x "$work/bin/br"
  : > "$work/br.log"
}

repo="$work/repo"
git init -q "$repo"

# check-setup: old br fails, missing driver fails, both present passes.
fake_br 1.8.2 0
( cd "$repo" && git config merge.beads.driver "br merge-driver %O %A %B" \
  && PATH="$work/bin:$PATH" sh "$here/check-setup.sh" >/dev/null 2>&1 ) && fail "br 1.8.2 accepted"
fake_br 1.9.0 0
( cd "$repo" && git config --unset merge.beads.driver \
  && PATH="$work/bin:$PATH" sh "$here/check-setup.sh" >/dev/null 2>&1 ) && fail "missing driver accepted"
( cd "$repo" && git config merge.beads.driver "br merge-driver %O %A %B" \
  && PATH="$work/bin:$PATH" sh "$here/check-setup.sh" >/dev/null 2>&1 ) || fail "complete setup refused"
fake_br 1.10.0 0
( cd "$repo" && PATH="$work/bin:$PATH" sh "$here/check-setup.sh" >/dev/null 2>&1 ) || fail "br 1.10.0 refused (version compared as text?)"

# follow-branch: a clean DB is rebuilt from the branch's JSONL; a dirty one is left alone, loudly;
# a file checkout (PRE_COMMIT_CHECKOUT_TYPE=0) changes no branch and does nothing.
mkdir -p "$repo/.beads"
fake_br 1.9.0 0
( cd "$repo" && PRE_COMMIT_CHECKOUT_TYPE=1 PATH="$work/bin:$PATH" sh "$here/follow-branch.sh" >/dev/null 2>&1 )
grep -q -- "sync --import-only --rebuild" "$work/br.log" || fail "clean DB not rebuilt on a branch switch"

fake_br 1.9.0 3
err=$( cd "$repo" && PRE_COMMIT_CHECKOUT_TYPE=1 PATH="$work/bin:$PATH" sh "$here/follow-branch.sh" 2>&1 >/dev/null )
grep -q -- "--rebuild" "$work/br.log" && fail "dirty DB rebuilt — its 3 unflushed issues would be lost"
echo "$err" | grep -q "3" || fail "dirty DB skipped without saying how many issues are at stake"

fake_br 1.9.0 0
( cd "$repo" && PRE_COMMIT_CHECKOUT_TYPE=0 PATH="$work/bin:$PATH" sh "$here/follow-branch.sh" >/dev/null 2>&1 )
[ -s "$work/br.log" ] && fail "file checkout touched br"

# After a merge or a rewrite pre-commit sets no checkout type: rebuild.
fake_br 1.9.0 0
( cd "$repo" && PATH="$work/bin:$PATH" sh "$here/follow-branch.sh" >/dev/null 2>&1 )
grep -q -- "--rebuild" "$work/br.log" || fail "not rebuilt after a merge/rewrite"

exit "$fails"
