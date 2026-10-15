#!/bin/sh
# One-time setup on an OpenWrt router using APK v3.
set -eu
url='https://wpfyorg.github.io/verso-plugin-jodu5174x'
key='/etc/apk/keys/verso-plugin-jodu5174x.pem'
list='/etc/apk/repositories.d/verso-plugin-jodu5174x.list'

mkdir -p /etc/apk/keys /etc/apk/repositories.d
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
uclient-fetch -q -O "$tmp" "$url/verso-plugin-jodu5174x.pem"
test -s "$tmp"
cp "$tmp" "$key"
chmod 0644 "$key"
printf '%s\n' "$url/aarch64_generic/packages.adb" > "$list"
grep -qxF aarch64_generic /etc/apk/arch || printf '%s\n' aarch64_generic >> /etc/apk/arch

echo 'Feed configured. Run: apk update && apk add verso-plugin-jodu5174x'
