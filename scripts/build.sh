#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/verso-plugin-jodu5174x .
cp manifest.json dist/manifest.json
cp rootfs/etc/init.d/verso-plugin-jodu5174x dist/verso-plugin-jodu5174x.init
printf 'Built: %s\n' "$(file dist/verso-plugin-jodu5174x)"
