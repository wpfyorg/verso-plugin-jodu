# Verso JODU5174x status plugin

A standalone [Verso](https://github.com/we-are-mono/verso) status dashboard for JODU51740/JODU51741 5G outdoor units on OpenWrt. It is intended for ARM64 routers, including JioRouter AX6000-class hardware.

The plugin displays connection state, primary and secondary 5G carrier details, RSRP, RSRQ, SINR, BLER, ODU traffic counters and Ethernet link status. It also provides a Verso settings form for the ODU address, username, password and monitoring switch. Settings are staged for Verso's normal review and Apply flow. The status reader runs every 15 seconds and hides stale results when an ODU read fails.

## Requirements

- Verso with manifest v1 plugin support (developed against Verso 0.5.x).
- OpenWrt with `procd`, `curl`, `uci`, and ARM64 architecture (`aarch64`).
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

First install and configure the original JODU LuCI package from its release page above. Its `/etc/config/jodu5174x` must contain a `main` section with the correct ODU address and credentials. The adapter itself does not modify that package.

Copy the built tarball to the router (replace `root@ROUTER` with your SSH destination):

```sh
cat dist/verso-plugin-jodu5174x-linux-arm64.tar.gz | ssh root@ROUTER 'cat > /tmp/verso-plugin-jodu5174x.tar.gz'
ssh root@ROUTER 'tar -xzf /tmp/verso-plugin-jodu5174x.tar.gz -C / && /etc/init.d/verso-plugin-jodu5174x enable && /etc/init.d/verso-plugin-jodu5174x restart && /etc/init.d/verso restart'
```

Open Verso → **5G ODU** (in the Status section). Use **ODU Settings** to stage changes and apply them using Verso's staged-changes bar. If status is unavailable, confirm `/usr/libexec/jodu5174x-data.sh` works on the router and that the logged-in account can read the JODU configuration.

## Remove

```sh
ssh root@ROUTER '/etc/init.d/verso-plugin-jodu5174x stop; /etc/init.d/verso-plugin-jodu5174x disable; rm -f /etc/init.d/verso-plugin-jodu5174x /usr/bin/verso-plugin-jodu5174x /usr/share/verso/plugins/jodu5174x/manifest.json; /etc/init.d/verso restart'
```

The underlying JODU package and `/etc/config/jodu5174x` remain installed.

## Design and limitations

- Independent native JSON widget plugin over a private Unix socket; no LuCI iframe, JavaScript, CSS or upstream proprietary source is bundled.
- Read permission for JODU UCI configuration is required before rendering live status. Passwords are never rendered. Settings write access is handled by Verso's rpcd ACL checks.
- Rebooting the ODU is not implemented. Current Verso plugin commands do not expose an authorized ODU reboot action.
- Dashboard refresh uses normal page reloads. Live polling happens server-side every 15 seconds; there is no push UI.
- Tested with synthetic responses and a Linux ARM64 cross-build. **Not yet validated on live JODU hardware or installed on a router.** Package installation, access control and Verso UI appearance still require live verification.

## Development

```sh
go test ./...
go vet ./...
./scripts/package.sh
```

Project is MIT licensed; the upstream JODU package retains its separate license.
