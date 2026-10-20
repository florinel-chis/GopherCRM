#!/usr/bin/env bash
set -euo pipefail
: "${REVISION:?exact tested revision required}"
[ "$(git rev-parse HEAD)" = "$REVISION" ]
[ "$(uname -s)" = Linux ]
mkdir -p release/bin
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0
for target in gophercrm create-admin migrate; do
  package="./cmd/$target"
  if [ "$target" = gophercrm ]; then package=./cmd; fi
  go build -trimpath -ldflags "-s -w -X main.revision=$REVISION" -o "release/bin/$target" "$package"
done
(cd gocrm-ui && npm ci && VITE_API_BASE_URL=/api/v1 npm run build)
cp -R gocrm-ui/dist release/www
cp -R migrations release/migrations
cp LICENSE CHANGELOG.md release/
cp release-artifacts/release-manifest.json release/
printf '%s\n' "$REVISION" > release/REVISION
SOURCE_DATE_EPOCH=$(git show -s --format=%ct "$REVISION")
export SOURCE_DATE_EPOCH
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner -cf - -C release . | gzip -n > release-artifacts/gophercrm-linux-amd64.tgz
python3 scripts/release/release.py seal
python3 scripts/release/smoke.py
