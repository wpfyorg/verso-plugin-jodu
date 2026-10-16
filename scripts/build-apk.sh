#!/bin/sh
# Run inside Alpine 3.23+ after scripts/build.sh has cross-compiled the binary.
set -eu
cd "$(dirname "$0")/.."

if [ "$#" -ne 2 ]; then
  echo 'usage: scripts/build-apk.sh VERSION SIGNING_KEY' >&2
  exit 2
fi
version="$1"
key="$2"
case "$version" in
  *[!0-9A-Za-z._+-]*|'') echo 'invalid package version' >&2; exit 2 ;;
esac
test -s "$key"
test -x dist/verso-plugin-jodu

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
install -d "$stage/usr/bin" "$stage/usr/share/verso/plugins/jodu" "$stage/usr/share/rpcd/acl.d" "$stage/usr/libexec/verso-plugin-jodu" "$stage/etc/init.d"
install -m 0755 dist/verso-plugin-jodu "$stage/usr/bin/verso-plugin-jodu"
install -m 0644 manifest.json "$stage/usr/share/verso/plugins/jodu/manifest.json"
install -m 0644 dist/jodu.default-config "$stage/usr/share/verso/plugins/jodu/default-config"
install -m 0644 dist/verso-jodu.acl.json "$stage/usr/share/rpcd/acl.d/verso-jodu.json"
install -m 0755 rootfs/etc/init.d/verso-plugin-jodu "$stage/etc/init.d/verso-plugin-jodu"
install -m 0755 packaging/apk/post-install.sh "$stage/usr/libexec/verso-plugin-jodu/post-install.sh"

repo=site/aarch64_generic
mkdir -p "$repo"
apk mkpkg \
  --info name:verso-plugin-jodu \
  --info "version:$version" \
  --info arch:aarch64_generic \
  --info 'description:Verso plugin for JODU 5G outdoor units' \
  --info license:MIT \
  --info url:https://github.com/wpfyorg/verso-plugin-jodu \
  --info origin:verso-plugin-jodu \
  --info replaces:verso-plugin-jodu5174x \
  --info 'depends:verso' \
  --files "$stage" \
  --script post-install:packaging/apk/post-install.sh \
  --script post-upgrade:packaging/apk/post-install.sh \
  --script pre-deinstall:packaging/apk/pre-deinstall.sh \
  --script post-deinstall:packaging/apk/post-deinstall.sh \
  --sign-key "$key" \
  --output "$repo/verso-plugin-jodu-$version.apk"

(cd "$repo" && apk mkndx --sign-key "$key" --output packages.adb ./*.apk)

