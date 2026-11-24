#!/bin/sh
# Installs the Observe server agent as a systemd service.
#   curl -fsSL __OBSERVE_URL__/agent/install.sh | sudo sh -s -- --token YOUR_TOKEN
# Downloads the binary for this machine, checks it against the release's checksums.txt, writes the token to
# /etc/observe-agent.env (readable by root only) and starts observe-agent.service. Run again to upgrade.
set -eu

OBSERVE_URL="__OBSERVE_URL__"
DOWNLOAD_URL="__DOWNLOAD_URL__"
TOKEN=""

while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="${2:-}"; shift 2 ;;
    --token=*) TOKEN="${1#--token=}"; shift ;;
    --url) OBSERVE_URL="${2:-}"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

fail() { echo "observe-agent: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "run as root (pipe to sudo sh)"
[ "$(uname -s)" = "Linux" ] || fail "the agent runs on Linux"
command -v systemctl >/dev/null 2>&1 || fail "systemd is required"
if [ -z "$TOKEN" ] && [ -f /etc/observe-agent.env ]; then
  TOKEN=$(sed -n 's/^OBSERVE_TOKEN=//p' /etc/observe-agent.env)
fi
[ -n "$TOKEN" ] || fail "pass --token (copy the install command from the server's page in Observe)"

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) fail "unsupported architecture $(uname -m) (amd64 and arm64 are built)" ;;
esac
BINARY="observe-agent_linux_${ARCH}"

if command -v sha256sum >/dev/null 2>&1; then SHA="sha256sum"; else SHA="shasum -a 256"; fi
if command -v curl >/dev/null 2>&1; then GET="curl -fsSL -o"; else GET="wget -qO"; fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
echo "Downloading $BINARY…"
$GET "$TMP/$BINARY" "$DOWNLOAD_URL/$BINARY" || fail "download failed: $DOWNLOAD_URL/$BINARY"
$GET "$TMP/checksums.txt" "$DOWNLOAD_URL/checksums.txt" || fail "download failed: $DOWNLOAD_URL/checksums.txt"
(cd "$TMP" && grep " $BINARY\$" checksums.txt | $SHA -c - >/dev/null) || fail "checksum mismatch for $BINARY; not installing"

install -m 0755 "$TMP/$BINARY" /usr/local/bin/observe-agent
umask 077
printf 'OBSERVE_URL=%s\nOBSERVE_TOKEN=%s\n' "$OBSERVE_URL" "$TOKEN" > /etc/observe-agent.env
chmod 600 /etc/observe-agent.env

cat > /etc/systemd/system/observe-agent.service <<'UNIT'
__SYSTEMD_UNIT__
UNIT

systemctl daemon-reload
systemctl enable observe-agent >/dev/null 2>&1
systemctl restart observe-agent
echo "observe-agent $(/usr/local/bin/observe-agent -version) is running. Charts appear on the server's page within two minutes."
