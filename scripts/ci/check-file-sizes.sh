#!/usr/bin/env bash
# Fails when a tracked file exceeds the size limit. Build outputs and other
# binaries belong in .gitignore, not in history: once pushed, a large blob
# stays in every clone even after the file is deleted.
#
# Sizes are read from the blobs in the index, not from the working tree, so a
# symlink counts as the link itself and an unstaged edit does not change the
# verdict.
set -euo pipefail

limit_bytes=${MAX_TRACKED_FILE_BYTES:-5242880} # 5 MiB
if ! [[ $limit_bytes =~ ^[0-9]+$ ]]; then
  echo "MAX_TRACKED_FILE_BYTES must be a whole number of bytes, got: $limit_bytes" >&2
  exit 2
fi

# Capture the listing first: a failure inside a process substitution would
# otherwise leave the loop empty and report success.
listing=$(mktemp)
trap 'rm -f "$listing"' EXIT
if ! git ls-files -s -z >"$listing"; then
  echo "git ls-files failed; cannot check tracked file sizes" >&2
  exit 2
fi

status=0
# Each record: "<mode> <object> <stage>\t<path>", NUL-terminated.
while IFS= read -r -d '' record; do
  meta=${record%%$'\t'*}
  path=${record#*$'\t'}
  read -r mode object _stage <<<"$meta"
  [ "$mode" = 160000 ] && continue # submodule gitlink, no blob
  size=$(git cat-file -s "$object")
  if [ "$size" -gt "$limit_bytes" ]; then
    echo "Tracked file larger than $limit_bytes bytes: $path ($size bytes)" >&2
    status=1
  fi
done <"$listing"

if [ "$status" -eq 0 ]; then
  echo "No tracked file exceeds $limit_bytes bytes."
fi
exit "$status"
