# Verso JODU5174x status plugin

A standalone [Verso](https://github.com/we-are-mono/verso) status dashboard for JODU51740/JODU51741 5G outdoor units on OpenWrt. It is intended for ARM64 routers, including JioRouter AX6000-class hardware.

The plugin displays connection state, primary and secondary 5G carrier details, RSRP, RSRQ, SINR, BLER, ODU traffic counters and Ethernet link status. It also provides a Verso settings form for the ODU address, username, password and monitoring switch. Settings are staged for Verso's normal review and Apply flow. The status reader runs every 15 seconds and hides stale results when an ODU read fails.

With the companion Verso `feat/plugin-overview-cards` change, the homepage also shows a **5G ODU** card with connection, radio, band, RSRP, SINR, Ethernet and last-check readings. Its link opens the full dashboard. It uses the same scoped configuration read as the dashboard and shows an unavailable message when permission, configuration or fresh telemetry is missing. Reload the homepage to refresh the card; it does not stream updates.

The homepage card requires both the companion Verso core change (based on v0.6.0) and this adapter build. Stock Verso v0.6.0 has no plugin homepage-card contract; installing this adapter alone keeps the existing status/settings pages but does not add a homepage card. The published v0.1.1 feed release does not include this change.

## Requirements

- Verso with manifest v1 plugin support (developed against Verso 0.5.x).
- OpenWrt with APK v3, `procd`, `curl`, `uci`, and compatible ARM64 package architecture (`aarch64_generic`).
- The independently installed [luci-app-jodu5174x-status](https://github.com/anishthevictorious/luci-app-jodu5174x-status) 5.0-r1 package, including its `/usr/libexec/jodu5174x-data.sh` command and `/etc/config/jodu5174x` settings.
- ODU reachable from the router at its HTTPS management address.

The JODU LuCI package is **proprietary and not included here**. Install it separately under its own license. This repository contains only the independent Verso adapter.

## Build

With Go 1.22+ on macOS or Linux:

```sh
./scripts/package.sh
```

This produces `dist/verso-plugin-jodu5174x-linux-arm64.tar.gz`, containing the standalone static ARM64 binary, the plugin manifest, and an OpenWrt `procd` service. No external Go modules are used.

## Install on an ARM64 OpenWrt router

First install and configure the original JODU LuCI package from its project or release page. This independently licensed, proprietary package must provide `/usr/libexec/jodu5174x-data.sh` and `/etc/config/jodu5174x`; its `main` section must contain the ODU address and credentials. The adapter does not include or modify that package.

The signed [v0.1.1 release](https://github.com/wpfyorg/verso-plugin-jodu5174x/releases/tag/v0.1.1) is available from the APK feed. Set up the feed once. Its public key and index are:

- Public signing key: <https://wpfyorg.github.io/verso-plugin-jodu5174x/verso-plugin-jodu5174x.pem>
- APK repository: <https://wpfyorg.github.io/verso-plugin-jodu5174x/aarch64_generic/packages.adb>

On the router, download and run the one-time setup script, then install normally:

```sh
uclient-fetch -O /tmp/jodu-feed.sh https://wpfyorg.github.io/verso-plugin-jodu5174x/setup-feed.sh
sh /tmp/jodu-feed.sh
apk update
apk add verso-plugin-jodu5174x
# Later, after a new release:
apk update
apk upgrade verso-plugin-jodu5174x
```

The setup script adds the public key and `aarch64_generic` repository without changing other feeds. The package requires the independently licensed JODU LuCI package described above; install and configure it separately first. This feed does not bundle its code. The APK enables the plugin and refreshes Verso after install or upgrade. Keep ODU credentials and private backups out of this public repository.

For local development, the manual tarball installation below remains available:

Copy the built tarball to the router (replace `root@ROUTER` with your SSH destination):

```sh
cat dist/verso-plugin-jodu5174x-linux-arm64.tar.gz | ssh root@ROUTER 'cat > /tmp/verso-plugin-jodu5174x.tar.gz'
ssh root@ROUTER 'tar -xzf /tmp/verso-plugin-jodu5174x.tar.gz -C / && /etc/init.d/verso-plugin-jodu5174x enable && /etc/init.d/verso-plugin-jodu5174x restart && /etc/init.d/verso restart'
```

Open Verso → **5G ODU** (in the Status section). Use **ODU Settings** to stage changes and apply them using Verso's staged-changes bar. If status is unavailable, confirm `/usr/libexec/jodu5174x-data.sh` works on the router and that the logged-in account can read the JODU configuration.

## Remove

For feed installations, run `apk del verso-plugin-jodu5174x`. The package removal hook stops the plugin and refreshes Verso. For the legacy tarball installation only:

```sh
ssh root@ROUTER '/etc/init.d/verso-plugin-jodu5174x stop; /etc/init.d/verso-plugin-jodu5174x disable; rm -f /etc/init.d/verso-plugin-jodu5174x /usr/bin/verso-plugin-jodu5174x /usr/share/verso/plugins/jodu5174x/manifest.json; /etc/init.d/verso restart'
```

The underlying JODU package and `/etc/config/jodu5174x` remain installed.

## Design and limitations

- Independent native JSON widget plugin over a private Unix socket; no LuCI iframe, JavaScript, CSS or upstream proprietary source is bundled.
- Read permission for JODU UCI configuration is required before rendering live status. Passwords are never rendered. Settings write access is handled by Verso's rpcd ACL checks.
- Rebooting the ODU is not implemented. Current Verso plugin commands do not expose an authorized ODU reboot action.
- Dashboard refresh uses normal page reloads. Live polling happens server-side every 15 seconds; there is no push UI.
- The optional `overview` manifest contribution requests `GET /overview`. It returns only a read-only summary; the companion shell supplies the card frame, link and saved layout controls. No write or command is available through this endpoint.
- Tested with synthetic responses and a Linux ARM64 cross-build. The signed APK and trusted APK v3 index were verified in CI. **Installation on the target router, live JODU hardware, access control and Verso UI appearance remain unverified.**

## Development

```sh
go test ./...
go vet ./...
./scripts/package.sh
```

The feed uses an ECDSA P-256 signing key. Its private PEM stays outside the repository and is stored in the GitHub Actions secret `APK_SIGNING_KEY`; the corresponding public PEM is tracked as `verso-plugin-jodu5174x.pem`. The [v0.1.1 release](https://github.com/wpfyorg/verso-plugin-jodu5174x/releases/tag/v0.1.1) publishes package `0.1.1-r0`. Each higher version tag builds and signs a new package and deploys the feed through GitHub Pages. Existing tags and releases are never replaced.

Project is MIT licensed; the upstream JODU package retains its separate license.
