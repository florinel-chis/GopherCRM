#!/usr/bin/env bash
# Fails when a tracked file exceeds the size limit. Build outputs and other
# binaries belong in .gitignore, not in history: once pushed, a large blob
# stays in every clone even after the file is deleted.
#
# Sizes are read from the blobs in the index, not from the working tree, so a
# symlink counts as the link itself and an unstaged edit does not change the
# verdict. All sizes come from one `git cat-file --batch-check` process, which
# keeps the check fast enough for the pre-commit hook.
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

objects=()
paths=()
# Each record: "<mode> <object> <stage>\t<path>", NUL-terminated.
while IFS= read -r -d '' record; do
  meta=${record%%$'\t'*}
  read -r mode object _stage <<<"$meta"
  [ "$mode" = 160000 ] && continue # submodule gitlink, no blob
  objects+=("$object")
  paths+=("${record#*$'\t'}")
done <"$listing"

status=0
if [ "${#objects[@]}" -gt 0 ]; then
  # Object ids contain no newlines, so newline-separated input is safe; the
  # sizes come back in the same order as the paths array.
  sizes=()
  while IFS= read -r size; do
    sizes+=("$size")
  done < <(printf '%s\n' "${objects[@]}" | git cat-file --batch-check='%(objectsize)')
  if [ "${#sizes[@]}" -ne "${#objects[@]}" ]; then
    echo "git cat-file returned ${#sizes[@]} sizes for ${#objects[@]} objects" >&2
    exit 2
  fi
  for i in "${!sizes[@]}"; do
    if ! [[ ${sizes[$i]} =~ ^[0-9]+$ ]]; then
      echo "cannot read the size of ${paths[$i]}: ${sizes[$i]}" >&2
      exit 2
    fi
    if [ "${sizes[$i]}" -gt "$limit_bytes" ]; then
      echo "Tracked file larger than $limit_bytes bytes: ${paths[$i]} (${sizes[$i]} bytes)" >&2
      status=1
    fi
  done
fi

if [ "$status" -eq 0 ]; then
  echo "No tracked file exceeds $limit_bytes bytes."
fi
exit "$status"
