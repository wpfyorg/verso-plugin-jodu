#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
./scripts/build.sh
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
install -d "$stage/usr/bin" "$stage/usr/share/verso/plugins/jodu5174x" "$stage/usr/share/rpcd/acl.d" "$stage/usr/libexec/verso-plugin-jodu5174x" "$stage/etc/init.d"
install -m 0755 dist/verso-plugin-jodu5174x "$stage/usr/bin/verso-plugin-jodu5174x"
install -m 0644 manifest.json "$stage/usr/share/verso/plugins/jodu5174x/manifest.json"
install -m 0644 dist/jodu5174x.default-config "$stage/usr/share/verso/plugins/jodu5174x/default-config"
install -m 0644 dist/verso-jodu5174x.acl.json "$stage/usr/share/rpcd/acl.d/verso-jodu5174x.json"
install -m 0755 rootfs/etc/init.d/verso-plugin-jodu5174x "$stage/etc/init.d/verso-plugin-jodu5174x"
install -m 0755 packaging/apk/post-install.sh "$stage/usr/libexec/verso-plugin-jodu5174x/post-install.sh"
case "$(tar --version 2>/dev/null)" in
  *bsdtar*) tar --uid 0 --gid 0 --uname root --gname root -czf dist/verso-plugin-jodu5174x-linux-arm64.tar.gz -C "$stage" etc usr ;;
  *) tar --owner=0 --group=0 --numeric-owner -czf dist/verso-plugin-jodu5174x-linux-arm64.tar.gz -C "$stage" etc usr ;;
esac
