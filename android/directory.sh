#!/usr/bin/env bash
# Copies the feed directory the app searches offline, awesome-rss-feeds under
# CC0, into android/assets/directory at one pinned commit. Run it by hand to
# move the pin; the build never reaches the network for it.
set -euo pipefail

REPO=plenaryapp/awesome-rss-feeds
SHA=${1:-9ab2482cae9031a5a39635c887fcfc8ece7e130a}
cd "$(dirname "$0")"
OUT=assets/directory
rm -rf "$OUT"
mkdir -p "$OUT/topics" "$OUT/countries"

fetch() { curl -fsSL "https://raw.githubusercontent.com/$REPO/$SHA/$1" -o "$2"; }
list() {
  curl -fsSL "https://api.github.com/repos/$REPO/git/trees/$SHA?recursive=1" |
    grep -o "\"path\": *\"$1/[^\"]*\.opml\"" | sed 's/.*"\(.*\)"$/\1/'
}

for kind in topics:recommended/with_category countries:countries/with_category; do
  dir=${kind%%:*} src=${kind#*:}
  while IFS= read -r path; do
    fetch "${path// /%20}" "$OUT/$dir/$(basename "$path")"
  done < <(list "$src")
done
fetch LICENSE "$OUT/LICENSE"
echo "$REPO at $SHA" > "$OUT/SOURCE"
du -sh "$OUT"
