#!/bin/sh
# Fetch the pinned repositories of repos.txt into a directory (default ./repos):
#   scripts/bench/fetch.sh [dir] [name...]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
dest=${1:-repos}
[ $# -gt 0 ] && shift
mkdir -p "$dest"
grep -v '^#' "$here/repos.txt" | while read -r name url commit sparse; do
	[ -z "$name" ] && continue
	if [ $# -gt 0 ] && ! printf '%s\n' "$@" | grep -qx "$name"; then continue; fi
	[ -d "$dest/$name/.git" ] && { echo "$name: already fetched"; continue; }
	echo "$name: fetching $commit"
	mkdir -p "$dest/$name"
	(
		cd "$dest/$name"
		git init -q
		git remote add origin "$url"
		if [ -n "$sparse" ]; then
			git sparse-checkout init --cone >/dev/null 2>&1 || true
			# shellcheck disable=SC2086
			git sparse-checkout set $sparse
			git fetch -q --depth 1 --filter=blob:none origin "$commit"
		else
			git fetch -q --depth 1 origin "$commit"
		fi
		git checkout -q FETCH_HEAD
	)
done
