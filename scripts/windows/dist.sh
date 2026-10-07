#!/bin/sh
# Build the Windows distribution: tailmixd, tailmix and tailmix-tray for
# amd64 and arm64, the matching wintun.dll, and the install scripts, zipped
# per architecture under dist/.
#
# Usage: scripts/windows/dist.sh [arch...]   (default: amd64 arm64)

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"

wintun_version=0.14.1
wintun_sha256=07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
wintun_url="https://www.wintun.net/builds/wintun-$wintun_version.zip"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/tailmix-windows.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

curl -fsSL -o "$tmp/wintun.zip" "$wintun_url"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/wintun.zip" | cut -d' ' -f1)
else
	actual=$(shasum -a 256 "$tmp/wintun.zip" | cut -d' ' -f1)
fi
if [ "$actual" != "$wintun_sha256" ]; then
	echo "wintun-$wintun_version.zip checksum $actual, want $wintun_sha256" >&2
	exit 1
fi
(cd "$tmp" && unzip -q wintun.zip)

version_ldflags=$(go run ./cmd/mkversion)
version=$(printf '%s' "$version_ldflags" | sed -n 's/.*shortStamp=\([^ ]*\).*/\1/p')
mkdir -p dist

for arch in ${*:-amd64 arm64}; do
	stage="$tmp/tailmix-windows-$arch"
	mkdir -p "$stage"
	GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$version_ldflags" \
		-o "$stage/" ./cmd/tailmixd ./cmd/tailmix
	GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$version_ldflags -H windowsgui" \
		-o "$stage/tailmix-tray.exe" ./cmd/tailmix-tray
	cp "$tmp/wintun/bin/$arch/wintun.dll" "$stage/"
	cp "$tmp/wintun/LICENSE.txt" "$stage/wintun-LICENSE.txt"
	cp scripts/windows/install.ps1 scripts/windows/uninstall.ps1 "$stage/"
	cp docs/windows.md "$stage/README.md"
	cp LICENSE "$stage/LICENSE.txt"
	cp licenses/tailmix.md "$stage/THIRD-PARTY-LICENSES.md"
	archive="$root/dist/tailmix-windows-$arch.zip"
	rm -f "$archive"
	(cd "$tmp" && zip -qr "$archive" "tailmix-windows-$arch")
	echo "built $archive ($version)"
done
