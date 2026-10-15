# Verso JODU5174x adapter

Standalone native Verso plugin for JODU51740/JODU51741 5G outdoor units on OpenWrt. It talks to the ODU's HTTPS CGI API directly; it does not call or package the separate LuCI add-on, its helper, curl, or proprietary assets.

## Adapter features

The native adapter provides the Verso surfaces below:

| Surface | Feature |
| --- | --- |
| Status | Connection/radio state, primary and secondary carrier metrics, RSRP, RSRQ, SINR, BLER, ODU traffic counters, Ethernet link and last successful check |
| Settings | ODU host, username and password, staged through Verso's Apply flow |
| Monitoring | Server-side status refresh every 15 seconds; its switch is staged through Apply and defaults off until enabled with a saved password |
| Reboot | Single-use confirmation form starts at most one immediate background request; page reload shows pending, confirmed or unconfirmed outcome |
| Overview card | Read-only summary when the companion Verso `feat/plugin-overview-cards` core change is installed |

These are the adapter's implemented surfaces. Exact parity with the separately licensed LuCI package has not been established. The homepage card requires both that companion Verso core change (based on v0.6.0) and this adapter; stock Verso v0.6.0 does not provide its contract. The v0.2.0 release introduces the native backend; v0.2.1 adds the dedicated service account required by Verso v0.7.0.

## Requirements

- Verso with manifest v1 plugin support and OpenWrt with APK v3, `procd`, `rpcd` and UCI.
- ARM64 package architecture (`aarch64_generic`) for router installs.
- ODU reachable from the router at its HTTPS management address.

Monitoring is off by default. The package seeds `/etc/config/jodu5174x` only when that file is absent, with host `192.168.225.1`, username `Admin`, empty password and `enabled '0'`. Set the password and enable monitoring in Verso → **System** → **ODU configuration**; view readings under **Network** → **ODU stats**. The config is mode `0600`; upgrades preserve its values.

The UI and ODU HTTPS client run as `verso-plugin-jodu5174x`, matching Verso v0.7.0 socket identity checks. A separate root process reads only the committed JODU UCI configuration and publishes an atomic private runtime snapshot every five seconds. Snapshots expire after 45 seconds; pending Apply edits never drive monitoring. The saved `/etc/config/jodu5174x` remains root-owned and mode `0600`.

Stock Verso core upgrades preserve the sidebar pages but replace a manually patched homepage-card binary. The card needs the companion core patch in every installed core version until upstream adopts that API; reinstalling this plugin alone cannot restore it.

## Build

With Go 1.22+ on macOS or Linux:

```sh
./scripts/package.sh
```

This creates `dist/verso-plugin-jodu5174x-linux-arm64.tar.gz` with the static ARM64 binary, manifest, service, scoped rpcd ACL, default-config template and install hook. No external Go modules are used.

To build the signed APK, run `scripts/build-apk.sh VERSION SIGNING_KEY` inside Alpine 3.23+ after `scripts/build.sh`. The package depends on Verso only; the native ODU client uses Go's standard library.

## Install

The signed [v0.2.1 release](https://github.com/wpfyorg/verso-plugin-jodu5174x/releases/tag/v0.2.1) includes the native ODU backend and sidebar integration.

The existing feed key and index remain available:

- Public signing key: <https://wpfyorg.github.io/verso-plugin-jodu5174x/verso-plugin-jodu5174x.pem>
- APK repository: <https://wpfyorg.github.io/verso-plugin-jodu5174x/aarch64_generic/packages.adb>

Configure the feed and install or upgrade the package with:

```sh
uclient-fetch -O /tmp/jodu-feed.sh https://wpfyorg.github.io/verso-plugin-jodu5174x/setup-feed.sh
sh /tmp/jodu-feed.sh
apk update
apk add --upgrade verso-plugin-jodu5174x
```

Alternatively, build and install the local tarball:

```sh
cat dist/verso-plugin-jodu5174x-linux-arm64.tar.gz | ssh root@ROUTER 'cat > /tmp/verso-plugin-jodu5174x.tar.gz'
ssh root@ROUTER 'tar -xzf /tmp/verso-plugin-jodu5174x.tar.gz -C / && sh /usr/libexec/verso-plugin-jodu5174x/post-install.sh'
```

The hook seeds config only if absent, sets its mode to `0600`, reloads rpcd grants, enables/restarts the adapter and signals Verso to rescan plugins and refresh live grants. It does not print config values. Configure the password and monitoring switch in Verso after install. Existing config and credentials survive package upgrades and removal.

The signed APK uses the same config-preserving hook and existing public signing key. Package upgrades preserve the saved ODU settings.

## Remove

For feed installations, run `apk del verso-plugin-jodu5174x`. The adapter's UCI config is an unowned system file and remains in place. For a manual tar install:

```sh
ssh root@ROUTER '/etc/init.d/verso-plugin-jodu5174x stop; /etc/init.d/verso-plugin-jodu5174x disable; rm -f /etc/init.d/verso-plugin-jodu5174x /usr/bin/verso-plugin-jodu5174x /usr/share/verso/plugins/jodu5174x/manifest.json /usr/share/verso/plugins/jodu5174x/default-config /usr/share/rpcd/acl.d/verso-jodu5174x.json /usr/libexec/verso-plugin-jodu5174x/post-install.sh; /etc/init.d/rpcd reload; killall -HUP verso 2>/dev/null || /etc/init.d/verso restart'
```

Remove `/etc/config/jodu5174x` separately only if you also want to delete the saved ODU credentials.

## Development and verification

```sh
go test ./...
go vet ./...
./scripts/package.sh
tar -tzf dist/verso-plugin-jodu5174x-linux-arm64.tar.gz
./scripts/package.test.sh
```

The packaging check exercises config creation, mode, upgrade preservation, and seed-failure handling in a cached Docker image (`golang:1.27.1-alpine`) with networking disabled. It still does not build/sign an APK.

Reboot acts immediately after confirmation; it does not wait for staged settings to be applied. It returns while the request runs in the background, then a page reload checks the outcome. No physical ODU reboot test has been run.

The native client accepts the ODU's self-signed HTTPS certificate. It uses no proxy and does not follow redirects; exact request-behavior parity with the former curl helper is unverified. Authentication cookies and rotating request tokens stay in memory and are not logged. The adapter reads Ethernet routing/sysfs state locally. Live monitoring and configuration were verified on the target router with a JODU51741; other hardware and firmware remain unverified. Exact parity with proprietary LuCI behavior is unknown.

Project is MIT licensed. The companion Verso core and ODU retain their own licenses.
