#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
./scripts/build.sh
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
install -d "$stage/usr/bin" "$stage/usr/share/verso/plugins/jodu" "$stage/usr/share/rpcd/acl.d" "$stage/usr/libexec/verso-plugin-jodu" "$stage/etc/init.d"
install -m 0755 dist/verso-plugin-jodu "$stage/usr/bin/verso-plugin-jodu"
install -m 0644 manifest.json "$stage/usr/share/verso/plugins/jodu/manifest.json"
install -m 0644 dist/jodu.default-config "$stage/usr/share/verso/plugins/jodu/default-config"
install -m 0644 dist/verso-jodu.acl.json "$stage/usr/share/rpcd/acl.d/verso-jodu.json"
install -m 0755 rootfs/etc/init.d/verso-plugin-jodu "$stage/etc/init.d/verso-plugin-jodu"
install -m 0755 packaging/apk/post-install.sh "$stage/usr/libexec/verso-plugin-jodu/post-install.sh"
case "$(tar --version 2>/dev/null)" in
  *bsdtar*) COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --uname root --gname root -czf dist/verso-plugin-jodu-linux-arm64.tar.gz -C "$stage" etc usr ;;
  *) tar --owner=0 --group=0 --numeric-owner -czf dist/verso-plugin-jodu-linux-arm64.tar.gz -C "$stage" etc usr ;;
esac
