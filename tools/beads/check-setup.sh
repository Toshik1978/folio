#!/bin/sh
# Pre-push: this clone can merge .beads/issues.jsonl across branches. Git silently falls back to a
# line merge when the `beads` driver .gitattributes names is not configured, and the line merge is
# what corrupts the tracker — so a clone missing it is stopped here, not mid-rebase.
# Local only: CI runners have no br, and the driver config is per clone, never --global.
need=1.9.0
command -v br >/dev/null 2>&1 || { echo "beads: br is not installed (need >= $need)"; exit 1; }
have=$(br --version 2>/dev/null | awk '{print $2}')
lowest=$(printf '%s\n%s\n' "$need" "$have" | sort -V | head -n 1)
[ "$lowest" = "$need" ] || { echo "beads: br $have is older than $need, which ships merge-driver"; exit 1; }
[ "$(git config --get merge.beads.driver)" = "br merge-driver %O %A %B" ] || {
  echo 'beads: merge driver not configured for this clone. Run:'
  echo '  git config merge.beads.driver "br merge-driver %O %A %B"'
  exit 1; }
exit 0
