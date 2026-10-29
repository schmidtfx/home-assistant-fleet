#!/bin/sh
# Builds a Proxmox LXC template: Proxmox's official Alpine template plus hafleet,
# an OpenRC service (enabled at boot) and helper scripts.
#
# usage: build-proxmox-template.sh <arch: amd64|arm64> <linux hafleet binary> <output dir> <version>
set -eu

ARCH=$1
BIN=$2
OUT_DIR=$3
VERSION=$4

ROOT=$(cd "$(dirname "$0")/.." && pwd)
FILES="$ROOT/deploy/proxmox"
CACHE="$ROOT/build/proxmox/cache" # downloads only; nothing in here is executed
INDEX_URL=http://download.proxmox.com/images/aplinfo-pve-9.dat
mkdir -p "$CACHE" "$OUT_DIR"

# Pick the newest Alpine "default" template for ARCH from Proxmox's index.
curl -fsSL -o "$CACHE/aplinfo.dat" "$INDEX_URL"
entry=$(awk -v arch="$ARCH" '
	/^Package: /      { pkg = $2; loc = ""; sum = ""; a = "" }
	/^Architecture: / { a = $2 }
	/^Location: /     { loc = $2 }
	/^sha512sum: /    { sum = $2 }
	/^$/ {
		if (pkg ~ /^alpine-[0-9.]+-default$/ && a == arch && loc != "" && sum != "") print loc, sum
		pkg = ""
	}
	END { if (pkg ~ /^alpine-[0-9.]+-default$/ && a == arch && loc != "" && sum != "") print loc, sum }
' "$CACHE/aplinfo.dat" | sort -V | tail -1)
[ -n "$entry" ] || { echo "no Alpine template for $ARCH in $INDEX_URL" >&2; exit 1; }
location=${entry% *}
sha512=${entry#* }
base=$(basename "$location")
case "$base" in alpine-*-default_*_"$ARCH".tar.xz) ;; *) echo "unexpected template name: $base" >&2; exit 1 ;; esac

if [ ! -f "$CACHE/$base" ]; then
	echo "Downloading $base"
	curl -fsSL -o "$CACHE/$base.part" "http://download.proxmox.com/images/$location"
	mv "$CACHE/$base.part" "$CACHE/$base"
fi
# The index is fetched over plain HTTP like Proxmox's own tooling; the checksum guards against corrupt downloads.
echo "$sha512  $CACHE/$base" | shasum -a 512 -c - >/dev/null || {
	rm -f "$CACHE/$base"
	echo "checksum mismatch for $base (deleted, try again)" >&2
	exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
xz -dc "$CACHE/$base" > "$work/rootfs.tar"

# Files to add (paths relative to the container root).
stage="$work/stage"
mkdir -p "$stage/usr/local/bin" "$stage/etc/init.d" "$stage/etc/hafleet" "$stage/etc/runlevels/default"
install -m 0755 "$BIN" "$stage/usr/local/bin/hafleet"
install -m 0755 "$FILES/hafleet-run" "$stage/usr/local/bin/hafleet-run"
install -m 0755 "$FILES/hafleet-admin" "$stage/usr/local/bin/hafleet-admin"
install -m 0755 "$FILES/hafleet.initd" "$stage/etc/init.d/hafleet"
install -m 0640 "$FILES/hafleet.env" "$stage/etc/hafleet/hafleet.env"
install -m 0644 "$FILES/motd" "$stage/etc/motd"
ln -s /etc/init.d/hafleet "$stage/etc/runlevels/default/hafleet"

# Append to the base archive so its own entries (owners, modes, links) stay untouched.
# Later entries win on extraction, which replaces /etc/motd. Only new directories get entries.
if tar --version 2>/dev/null | grep -q bsdtar; then
	owner="--uid 0 --gid 0 --uname root --gname root"
else
	owner="--owner=0 --group=0 --numeric-owner"
fi
# shellcheck disable=SC2086
(cd "$stage" && tar -rf "$work/rootfs.tar" $owner --no-recursion \
	./etc/hafleet \
	./usr/local/bin/hafleet ./usr/local/bin/hafleet-run ./usr/local/bin/hafleet-admin \
	./etc/init.d/hafleet ./etc/hafleet/hafleet.env ./etc/motd ./etc/runlevels/default/hafleet)

alpine=$(echo "$base" | sed -E 's/^alpine-([0-9.]+)-default.*/\1/')
out="$OUT_DIR/hafleet-${VERSION}-alpine-${alpine}_${ARCH}.tar.gz"
gzip -9 -c "$work/rootfs.tar" > "$out"
echo "$out"
