#!/usr/bin/env bash
# One-time setup of automated production deploys. Run it once, from the
# repository root, on a machine that has root SSH access to the production
# server and a `gh` login with admin rights on the repository:
#
#   scripts/deploy/bootstrap.sh <user@host> <public URL> <web root on the server>
#
# It:
#   1. generates a dedicated deploy key (in a temporary directory that is
#      removed on exit; the private key is only ever stored as a GitHub secret);
#   2. installs scripts/deploy/receive-release.sh on the server, with a config
#      file holding the web root, and adds the key to authorized_keys pinned to
#      that script (`restrict,command=…`: no shell, no forwarding, no pty);
#   3. creates the GitHub `production` environment and stores the key, the
#      server's host key and the host/user/URL there;
#   4. checks that the key works, then starts the first deploy.
#
# Re-running it replaces the key (the old one stops working) and reinstalls the
# script, so it doubles as key rotation.
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <user@host> <public URL> <web root on the server>" >&2
  exit 2
fi
target=$1
url=${2%/}
web_root=$3
user=${target%@*}
host=${target#*@}

for tool in ssh scp ssh-keygen ssh-keyscan gh; do
  command -v "$tool" >/dev/null || { echo "bootstrap: '$tool' is required" >&2; exit 2; }
done
[ -f scripts/deploy/receive-release.sh ] || { echo "bootstrap: run this from the repository root" >&2; exit 2; }
repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ssh-keygen -q -t ed25519 -N '' -C gophercrm-deploy -f "$tmp/deploy_key"

echo "bootstrap: installing the deploy script and key on $host"
scp -q scripts/deploy/receive-release.sh "$target:/tmp/receive-release.new"
ssh "$target" bash -s -- "$(cat "$tmp/deploy_key.pub")" "$web_root" <<'REMOTE'
set -euo pipefail
public_key=$1
web_root=$2
for tool in flock rsync mysqldump gzip curl tar systemctl; do
  command -v "$tool" >/dev/null || { echo "server: '$tool' is missing" >&2; exit 1; }
done
install -d -m 755 /srv/gophercrm/deploy /srv/gophercrm/releases
install -d -m 700 /srv/gophercrm/backups
install -m 755 /tmp/receive-release.new /srv/gophercrm/deploy/receive-release
rm -f /tmp/receive-release.new
printf 'GOPHERCRM_WEB_ROOT=%q\n' "$web_root" >/srv/gophercrm/deploy/receive-release.conf
chmod 644 /srv/gophercrm/deploy/receive-release.conf

install -d -m 700 ~/.ssh
touch ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
keys=$(mktemp)
grep -v ' gophercrm-deploy$' ~/.ssh/authorized_keys >"$keys" || true
printf 'restrict,command="/srv/gophercrm/deploy/receive-release" %s\n' "$public_key" >>"$keys"
cat "$keys" >~/.ssh/authorized_keys
rm -f "$keys"
echo "server: deploy script and key installed"
REMOTE

known_hosts=$(ssh-keyscan -t ed25519 "$host" 2>/dev/null)
[ -n "$known_hosts" ] || { echo "bootstrap: could not read the host key of $host" >&2; exit 1; }

echo "bootstrap: checking that the deploy key reaches only the deploy script"
ssh -i "$tmp/deploy_key" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o UserKnownHostsFile=<(printf '%s\n' "$known_hosts") -o StrictHostKeyChecking=yes \
  "$target" status

echo "bootstrap: configuring the GitHub 'production' environment of $repo"
gh api -X PUT "repos/$repo/environments/production" >/dev/null
gh secret set PRODUCTION_SSH_KEY --env production --repo "$repo" <"$tmp/deploy_key"
printf '%s\n' "$known_hosts" | gh secret set PRODUCTION_SSH_KNOWN_HOSTS --env production --repo "$repo"
gh variable set PRODUCTION_HOST --env production --repo "$repo" --body "$host"
gh variable set PRODUCTION_SSH_USER --env production --repo "$repo" --body "$user"
gh variable set PRODUCTION_URL --env production --repo "$repo" --body "$url"

echo "bootstrap: starting the first deploy"
gh workflow run Deploy --repo "$repo" -f action=deploy
echo "bootstrap: done. Every merge to main now deploys itself once CI passes."
