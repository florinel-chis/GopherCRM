#!/usr/bin/env bash
# Fails when a tracked file exceeds the size limit. Build outputs and other
# binaries belong in .gitignore, not in history: once pushed, a large blob
# stays in every clone even after the file is deleted.
set -euo pipefail

limit_bytes=${MAX_TRACKED_FILE_BYTES:-5242880} # 5 MiB
status=0

while IFS= read -r -d '' path; do
  [ -f "$path" ] || continue
  size=$(wc -c <"$path" | tr -d ' ')
  if [ "$size" -gt "$limit_bytes" ]; then
    echo "Tracked file larger than $limit_bytes bytes: $path ($size bytes)" >&2
    status=1
  fi
done < <(git ls-files -z)

if [ "$status" -eq 0 ]; then
  echo "No tracked file exceeds $limit_bytes bytes."
fi
exit "$status"
