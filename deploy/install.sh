#!/bin/sh
# Install or upgrade gentoo-zh-verify-bot from a GitHub release.
#
#   sh install.sh              # latest release
#   sh install.sh v4.3.0       # a specific tag
#
# Downloads the release binary for this machine's architecture, checks it against the
# published SHA256SUMS, installs the systemd unit, and leaves the service enabled. An
# existing bot.env is never overwritten, so upgrading is the same command.
set -eu

repo=Zakkaus/gentoo-zh-verify-bot
prefix=/usr/local/bin
confdir=/etc/gentoo-zh-verify-bot
unit=/etc/systemd/system/gentoo-zh-verify-bot.service
version=${1:-}

case $(uname -m) in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "install.sh: no release binary for $(uname -m)" >&2; exit 1 ;;
esac

if [ -z "$version" ]; then
	version=$(curl --fail --silent --show-error --location \
		"https://api.github.com/repos/${repo}/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || { echo "install.sh: could not resolve the latest release" >&2; exit 1; }
fi
echo "installing ${version} (${arch})"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
base="https://github.com/${repo}/releases/download/${version}"

curl --fail --silent --show-error --location --output "${work}/gentoo-zh-verify-bot-linux-${arch}" \
	"${base}/gentoo-zh-verify-bot-linux-${arch}"
curl --fail --silent --show-error --location --output "${work}/SHA256SUMS" "${base}/SHA256SUMS"
( cd "$work" && sha256sum --ignore-missing --strict --check SHA256SUMS )
curl --fail --silent --show-error --location --output "${work}/unit" \
	"https://raw.githubusercontent.com/${repo}/${version}/deploy/gentoo-zh-verify-bot.service"

sudo install -Dm755 "${work}/gentoo-zh-verify-bot-linux-${arch}" "${prefix}/gentoo-zh-verify-bot"
sudo install -Dm644 "${work}/unit" "$unit"
if [ ! -e "${confdir}/bot.env" ]; then
	sudo install -Dm600 /dev/null "${confdir}/bot.env"
	echo "created ${confdir}/bot.env — add your token before starting:"
	echo "    sudoedit ${confdir}/bot.env"
	echo "    BOT_TOKEN=<token from @BotFather>"
fi
sudo systemctl daemon-reload
sudo systemctl enable gentoo-zh-verify-bot

if grep -q '^BOT_TOKEN=.' "${confdir}/bot.env" 2>/dev/null; then
	sudo systemctl restart gentoo-zh-verify-bot
	echo "gentoo-zh-verify-bot ${version} is running."
	echo "First start writes a one-use owner link to the journal:"
	echo "    sudo journalctl -u gentoo-zh-verify-bot"
else
	echo "add BOT_TOKEN to ${confdir}/bot.env, then: sudo systemctl start gentoo-zh-verify-bot"
fi
