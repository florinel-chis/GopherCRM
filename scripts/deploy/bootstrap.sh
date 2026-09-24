#!/usr/bin/env bash
# One-time setup of automated production deploys. Run it once, from the
# repository root, on a machine that has root SSH access to the production
# server and a `gh` login with admin rights on the repository:
#
#   scripts/deploy/bootstrap.sh <user@host> <public URL> <web root on the server>
#
# <host> must be the server's direct address: a proxied public name (behind a
# CDN, say) does not accept SSH.
#
# It:
#   1. generates a dedicated deploy key (in a temporary directory that is
#      removed on exit; the private key is only ever stored as a GitHub secret);
#   2. installs scripts/deploy/receive-release.sh on the server, with a config
#      file holding the web root, and adds the key to authorized_keys pinned to
#      that script (`restrict,command=…`: no shell, no forwarding, no pty);
#   3. creates the GitHub `production` environment, open to protected branches
#      only, and stores the key, the server's host key, host and user there as
#      secrets (so they never show in the public Actions logs) and the URL as a
#      variable;
#   4. checks that the key works, then starts the first deploy.
#
# Re-running it replaces the key (the old one stops working) and reinstalls the
# script, so it doubles as key rotation.
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <user@host> <public URL> <web root on the server>" >&2
  echo "  <host> is the server's direct address, not a CDN-proxied name." >&2
  exit 2
fi
target=$1
url=${2%/}
web_root=$3
[[ $target == *@* ]] || { echo "bootstrap: the target must be user@host" >&2; exit 2; }
user=${target%@*}
host=${target#*@}

for tool in ssh scp ssh-keygen gh; do
  command -v "$tool" >/dev/null || { echo "bootstrap: '$tool' is required" >&2; exit 2; }
done
[ -f scripts/deploy/receive-release.sh ] || { echo "bootstrap: run this from the repository root" >&2; exit 2; }
repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
[ "$(gh api "repos/$repo/branches/main" --jq .protected)" = true ] ||
  { echo "bootstrap: main is not a protected branch; the environment policy relies on it" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ssh-keygen -q -t ed25519 -N '' -C gophercrm-deploy -f "$tmp/deploy_key"

echo "bootstrap: installing the deploy script and key on $host"
scp -q scripts/deploy/receive-release.sh "$target:/tmp/receive-release.new"
# ssh joins its arguments with spaces into one remote command line, so each
# argument is quoted for the remote shell here.
ssh "$target" "bash -s -- $(printf '%q ' "$(cat "$tmp/deploy_key.pub")" "$web_root")" <<'REMOTE'
set -euo pipefail
public_key=$1
web_root=$2
[[ $public_key =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+\ gophercrm-deploy$ ]] ||
  { echo "server: unexpected public key argument" >&2; exit 1; }
[[ $web_root == /* ]] || { echo "server: the web root must be an absolute path" >&2; exit 1; }
for tool in flock rsync mysqldump gzip curl tar systemctl; do
  command -v "$tool" >/dev/null || { echo "server: '$tool' is missing" >&2; exit 1; }
done
# The deploy script runs as root and trusts these directories, so root owns
# them and no one else may write there (the service only reads bin/ and env/).
# 755, not just go-w: the service user must still be able to reach bin/.
chown root:root /srv/gophercrm
chmod 755 /srv/gophercrm
for dir in bin deploy releases; do
  install -d -o root -g root -m 755 "/srv/gophercrm/$dir"
  chown root:root "/srv/gophercrm/$dir"
  chmod 755 "/srv/gophercrm/$dir"
done
install -d -o root -g root -m 700 /srv/gophercrm/backups
chown root:root /srv/gophercrm/backups
chmod 700 /srv/gophercrm/backups
install -o root -g root -m 755 /tmp/receive-release.new /srv/gophercrm/deploy/receive-release
rm -f /tmp/receive-release.new
# The web root is synced by root with --delete, so it gets the same rule.
install -d -o root -g root -m 755 "$web_root"
chown root:root "$web_root"
chmod 755 "$web_root"
printf 'GOPHERCRM_WEB_ROOT=%q\n' "$web_root" >/srv/gophercrm/deploy/receive-release.conf
chown root:root /srv/gophercrm/deploy/receive-release.conf
chmod 644 /srv/gophercrm/deploy/receive-release.conf

# Replace any earlier deploy key atomically: build the new file next to the
# old one, then rename it into place.
install -d -m 700 ~/.ssh
touch ~/.ssh/authorized_keys
keys=$(mktemp ~/.ssh/authorized_keys.XXXXXX)
unix=$(mktemp ~/.ssh/authorized_keys.XXXXXX)
tr -d '\r' <~/.ssh/authorized_keys >"$unix" ||
  { rm -f "$keys" "$unix"; echo "server: could not read authorized_keys" >&2; exit 1; }
rc=0
grep -v ' gophercrm-deploy$' "$unix" >"$keys" || rc=$?
rm -f "$unix"
[ "$rc" -le 1 ] || { rm -f "$keys"; echo "server: could not filter authorized_keys" >&2; exit 1; }
printf 'restrict,command="/srv/gophercrm/deploy/receive-release" %s\n' "$public_key" >>"$keys"
chmod 600 "$keys"
mv -f "$keys" ~/.ssh/authorized_keys
echo "server: deploy script and key installed"
REMOTE

# The host key is read over the session already authenticated above, not
# from an unauthenticated scan.
host_key=$(ssh "$target" cat /etc/ssh/ssh_host_ed25519_key.pub | awk '{ print $1, $2 }')
[ -n "$host_key" ] || { echo "bootstrap: could not read the host key of $host" >&2; exit 1; }
known_hosts="$host $host_key"
printf '%s\n' "$known_hosts" >"$tmp/known_hosts"

echo "bootstrap: checking that the deploy key reaches only the deploy script"
ssh -i "$tmp/deploy_key" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o UserKnownHostsFile="$tmp/known_hosts" -o StrictHostKeyChecking=yes \
  "$target" status

echo "bootstrap: configuring the GitHub 'production' environment of $repo"
# Only protected branches (main) may use the environment, so a workflow on any
# other branch cannot get the key. This PUT sets only the branch policy; if
# reviewers or a wait timer are added by hand later, re-check them after a key
# rotation.
printf '%s' '{"deployment_branch_policy":{"protected_branches":true,"custom_branch_policies":false}}' |
  gh api -X PUT "repos/$repo/environments/production" --input - >/dev/null
gh secret set PRODUCTION_SSH_KEY --env production --repo "$repo" <"$tmp/deploy_key"
gh secret set PRODUCTION_SSH_KNOWN_HOSTS --env production --repo "$repo" <"$tmp/known_hosts"
gh secret set PRODUCTION_HOST --env production --repo "$repo" --body "$host"
gh secret set PRODUCTION_SSH_USER --env production --repo "$repo" --body "$user"
gh variable set PRODUCTION_URL --env production --repo "$repo" --body "$url"
# Earlier versions of this script stored host and user as variables.
gh variable delete PRODUCTION_HOST --env production --repo "$repo" 2>/dev/null || true
gh variable delete PRODUCTION_SSH_USER --env production --repo "$repo" 2>/dev/null || true

echo "bootstrap: starting the first deploy"
gh workflow run Deploy --repo "$repo" -f action=deploy
echo "bootstrap: done. Every merge to main now deploys itself once CI passes."
