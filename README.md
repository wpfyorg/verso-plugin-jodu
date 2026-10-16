# Verso plugin for JODU

Native Verso plugin for JODU51740/JODU51741 5G outdoor units on OpenWrt. It talks to the ODU's HTTPS CGI API directly; it does not call or package the separate LuCI add-on, its helper, curl, or proprietary assets.

## Features

| Surface | Feature |
| --- | --- |
| Status | Connection/radio state, primary and secondary carrier metrics, RSRP, RSRQ, SINR, BLER, ODU traffic counters, Ethernet link and last successful check |
| Settings | ODU host, username and password, staged through Verso's Apply flow |
| Monitoring | Server-side status refresh every 15 seconds; its switch is staged through Apply and defaults off until enabled with a saved password |
| Reboot | Single-use confirmation form starts at most one immediate background request; page reload shows pending, confirmed or unconfirmed outcome |
| Overview card | Read-only summary when the companion Verso `feat/plugin-overview-cards` core change is installed |

Exact parity with the separately licensed LuCI package has not been established. The homepage card requires the companion Verso core change (based on v0.6.0); stock Verso v0.6.0 does not provide its contract. The v0.2.0 release introduced the native backend; v0.2.1 added the dedicated service account required by Verso v0.7.0.

## Requirements

- Verso with manifest v1 plugin support and OpenWrt with APK v3, `procd`, `rpcd` and UCI.
- ARM64 package architecture (`aarch64_generic`) for router installs.
- ODU reachable from the router at its HTTPS management address.

The package is `verso-plugin-jodu`. Monitoring is off by default. On a new install, it creates `/etc/config/jodu` with model `jodu5174x`, host `192.168.225.1`, username `Admin`, empty password and `enabled '0'`. Set the password and enable monitoring in Verso → **System** → **ODU configuration**; view readings under **Network** → **ODU stats**. The config is mode `0600`; upgrades preserve its values.

When upgrading from `verso-plugin-jodu5174x`, replace the old package with the new one:

```sh
apk add verso-plugin-jodu
apk del verso-plugin-jodu5174x
```

The old package is stopped and disabled during removal. Its `/etc/config/jodu5174x` file remains. If `/etc/config/jodu` does not exist, the new package migrates only `host`, `username`, `password` and `enabled`, and sets `model 'jodu5174x'`. The old config stays in place. An existing `/etc/config/jodu` always wins and keeps its values. The migration hook never prints config values.

The UI and ODU HTTPS client run as `verso-plugin-jodu`, matching Verso v0.7.0 socket identity checks. A separate root process reads only the committed JODU UCI config and publishes an atomic private runtime snapshot every five seconds. Snapshots expire after 45 seconds; pending Apply edits never drive monitoring. The saved `/etc/config/jodu` remains root-owned and mode `0600`.

Stock Verso core upgrades preserve the sidebar pages but replace a manually patched homepage-card binary. The card needs the companion core patch in every installed core version until upstream adopts that API; reinstalling this plugin alone cannot restore it.

## Build

With Go 1.22+ on macOS or Linux:

```sh
./scripts/package.sh
```

This creates `dist/verso-plugin-jodu-linux-arm64.tar.gz` with the static ARM64 binary, manifest, service, scoped rpcd ACL, default-config template and install hook. No external Go modules are used.

To build the signed APK, run `scripts/build-apk.sh VERSION SIGNING_KEY` inside Alpine 3.23+ after `scripts/build.sh`. The package depends on Verso only; the native ODU client uses Go's standard library.

## Install

The signed [v0.2.1 release](https://github.com/wpfyorg/verso-plugin-jodu5174x/releases/tag/v0.2.1) contains the native ODU backend and sidebar integration. The existing feed key and index remain hosted under the repository's existing GitHub Pages address:

- Public signing key: <https://wpfyorg.github.io/verso-plugin-jodu5174x/verso-plugin-jodu5174x.pem>
- APK repository: <https://wpfyorg.github.io/verso-plugin-jodu5174x/aarch64_generic/packages.adb>

Configure the feed and install or upgrade the package with:

```sh
uclient-fetch -O /tmp/jodu-feed.sh https://wpfyorg.github.io/verso-plugin-jodu5174x/setup-feed.sh
sh /tmp/jodu-feed.sh
apk update
```

For an existing install, run the package replacement commands shown above. For a new install, run:

```sh
apk add verso-plugin-jodu
```

Alternatively, build and install the local tarball:

```sh
cat dist/verso-plugin-jodu-linux-arm64.tar.gz | ssh root@ROUTER 'cat > /tmp/verso-plugin-jodu.tar.gz'
ssh root@ROUTER 'tar -xzf /tmp/verso-plugin-jodu.tar.gz -C / && sh /usr/libexec/verso-plugin-jodu/post-install.sh'
```

The hook creates or migrates config only when `/etc/config/jodu` is absent, protects it with mode `0600`, reloads rpcd grants, disables the legacy service, removes its exact stale manifest and ACL, enables/restarts the new service, and signals Verso to rescan plugins. Config values are never printed.

## Remove

For feed installations, run `apk del verso-plugin-jodu`. The adapter's UCI config is an unowned system file and remains in place. For a manual tar install:

```sh
ssh root@ROUTER '/etc/init.d/verso-plugin-jodu stop; /etc/init.d/verso-plugin-jodu disable; rm -f /etc/init.d/verso-plugin-jodu /usr/bin/verso-plugin-jodu /usr/share/verso/plugins/jodu/manifest.json /usr/share/verso/plugins/jodu/default-config /usr/share/rpcd/acl.d/verso-jodu.json /usr/libexec/verso-plugin-jodu/post-install.sh; /etc/init.d/rpcd reload; killall -HUP verso 2>/dev/null || /etc/init.d/verso restart'
```

Remove `/etc/config/jodu` separately only if you also want to delete the saved ODU credentials. The legacy `/etc/config/jodu5174x` is retained after migration and can be removed separately when no longer needed.

## Development and verification

```sh
go test ./...
go vet ./...
./scripts/package.sh
tar -tzf dist/verso-plugin-jodu-linux-arm64.tar.gz
./scripts/package.test.sh
```

The packaging check validates archive paths and, when the cached Docker image is available, exercises config creation, migration, config preservation and seed-failure handling with networking disabled. It does not build or sign an APK.

Reboot acts immediately after confirmation; it does not wait for staged settings to be applied. It returns while the request runs in the background, then a page reload checks the outcome. No physical ODU reboot test has been run.

The native client accepts the ODU's self-signed HTTPS certificate. It uses no proxy and does not follow redirects; exact request-behavior parity with the former curl helper is unverified. Authentication cookies and rotating request tokens stay in memory and are not logged. The adapter reads Ethernet routing/sysfs state locally. Live monitoring and configuration were verified on the target router with a JODU51741; other hardware and firmware remain unverified. Exact parity with proprietary LuCI behavior is unknown.

Project is MIT licensed. The companion Verso core and ODU retain their own licenses.
