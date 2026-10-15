#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
./scripts/build.sh
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
install -d "$stage/usr/bin" "$stage/usr/share/verso/plugins/jodu5174x" "$stage/etc/init.d"
install -m 0755 dist/verso-plugin-jodu5174x "$stage/usr/bin/verso-plugin-jodu5174x"
install -m 0644 manifest.json "$stage/usr/share/verso/plugins/jodu5174x/manifest.json"
install -m 0755 rootfs/etc/init.d/verso-plugin-jodu5174x "$stage/etc/init.d/verso-plugin-jodu5174x"
tar -czf dist/verso-plugin-jodu5174x-linux-arm64.tar.gz -C "$stage" etc usr
