#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/verso-plugin-jodu .
cp manifest.json dist/manifest.json
cp rootfs/etc/init.d/verso-plugin-jodu dist/verso-plugin-jodu.init
cp rootfs/etc/config/jodu dist/jodu.default-config
cp rootfs/usr/share/rpcd/acl.d/verso-jodu.json dist/verso-jodu.acl.json
printf 'Built: %s\n' "$(file dist/verso-plugin-jodu)"
