#!/bin/sh
# ATM's delivery for the fork teseo/no-mistakes. Every run works in a fresh clone whose only remote is the fork,
# and gh resolves a fork to its parent by default, so the default repo is pinned to the fork first and checked:
# nothing may ever reach kunchenguid/no-mistakes.
gh repo set-default teseo/no-mistakes || exit 1
[ "$(gh repo set-default --view)" = "teseo/no-mistakes" ] || { echo "deliver: gh default repo is not teseo/no-mistakes" >&2; exit 1; }
if git remote | grep -qx upstream; then
  git remote set-url --push upstream no_push || exit 1
fi
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT" --wait 24h
