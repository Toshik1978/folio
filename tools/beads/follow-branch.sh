#!/bin/sh
# post-checkout / post-merge / post-rewrite: point the beads DB at the checked-out branch.
# The DB is one per clone while issues.jsonl is per branch, so after a switch the next br write
# would flush the previous branch's issues into this one. --rebuild makes the JSONL authoritative.
#
# A git hook here cannot abort a checkout, and --rebuild drops unflushed DB changes, so a dirty DB
# is left alone with a warning naming the fix. Never fails the git command it follows.
[ "${PRE_COMMIT_CHECKOUT_TYPE:-1}" = 1 ] || exit 0   # 0 = a file checkout; no branch changed
[ -d .beads ] || exit 0
command -v br >/dev/null 2>&1 || { echo "beads: br not installed — the beads DB still describes the previous branch" >&2; exit 0; }
dirty=$(br sync --status 2>/dev/null | awk -F': *' '/Dirty issues/ { print $2; exit }')
if [ "${dirty:-0}" != 0 ]; then
  echo "beads: $dirty unflushed issue change(s) in the DB — NOT following the branch. On the branch" >&2
  echo "  they belong to: br sync --flush-only && commit; then here: br sync --import-only --rebuild" >&2
  exit 0
fi
br sync --import-only --rebuild >/dev/null 2>&1 \
  || echo "beads: rebuild failed — run: br sync --import-only --rebuild" >&2
exit 0
