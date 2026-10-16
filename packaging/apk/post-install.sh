#!/bin/sh
# APK post-install and post-upgrade hook. Never print config values.
set -eu

config=/etc/config/jodu
template=/usr/share/verso/plugins/jodu/default-config
legacy_config=/etc/config/jodu5174x

# A package rename does not uninstall a differently named APK package. Stop
# its service before any later validation can fail, then remove only its exact
# plugin manifest and obsolete ACL.
if [ -x /etc/init.d/verso-plugin-jodu5174x ]; then
  /etc/init.d/verso-plugin-jodu5174x disable 2>/dev/null || true
  /etc/init.d/verso-plugin-jodu5174x stop 2>/dev/null || true
  killall -q verso-plugin-jodu5174x 2>/dev/null || true
fi
rm -f /usr/share/verso/plugins/jodu5174x/manifest.json
rm -f /usr/share/rpcd/acl.d/verso-jodu5174x.json

if [ -e "$config" ] || [ -L "$config" ]; then
  [ ! -L "$config" ] || { echo 'JODU config path must not be a symlink.' >&2; exit 1; }
  [ -f "$config" ] || { echo 'JODU config path is not a regular file.' >&2; exit 1; }
else
  umask 077
  tmp="$(mktemp /etc/config/.jodu.XXXXXX)" || exit 1
  if [ -f "$legacy_config" ] && [ ! -L "$legacy_config" ]; then
    work="$(mktemp -d /tmp/jodu-migration.XXXXXX)" || {
      rm -f "$tmp"
      echo 'Could not prepare JODU config migration.' >&2
      exit 1
    }
    if ! uci -q -c "$work" -P "$work" set jodu.main=jodu ||
       ! uci -q -c "$work" -P "$work" set jodu.main.model=jodu5174x; then
      rm -rf "$work" "$tmp"
      echo 'Could not prepare JODU config migration.' >&2
      exit 1
    fi
    for option in host username password enabled; do
      if value="$(uci -q -c /etc/config -P "$work" get "jodu5174x.main.$option" 2>/dev/null)"; then
        if ! uci -q -c "$work" -P "$work" set "jodu.main.$option=$value"; then
          rm -rf "$work" "$tmp"
          echo 'Could not migrate JODU config.' >&2
          exit 1
        fi
      fi
    done
    if ! uci -q -c "$work" -P "$work" commit jodu || ! cp "$work/jodu" "$tmp"; then
      rm -rf "$work" "$tmp"
      echo 'Could not migrate JODU config.' >&2
      exit 1
    fi
    rm -rf "$work"
  else
    [ -r "$template" ] || {
      rm -f "$tmp"
      echo 'JODU default config template is missing.' >&2
      exit 1
    }
    if ! cp "$template" "$tmp"; then
      rm -f "$tmp"
      echo 'Could not prepare the JODU config.' >&2
      exit 1
    fi
  fi
  if ! chmod 0600 "$tmp" || ! ln "$tmp" "$config" 2>/dev/null; then
    if [ ! -f "$config" ] || [ -L "$config" ]; then
      rm -f "$tmp"
      echo 'Could not install the JODU config.' >&2
      exit 1
    fi
  fi
  rm -f "$tmp"
fi
[ -f "$config" ] || { echo 'JODU config was not created.' >&2; exit 1; }
chmod 0600 "$config" || { echo 'Could not protect the JODU config.' >&2; exit 1; }

[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload 2>/dev/null || true
if [ -x /etc/init.d/verso-plugin-jodu ]; then
  /etc/init.d/verso-plugin-jodu enable
  /etc/init.d/verso-plugin-jodu restart
fi
if [ -x /etc/init.d/verso ]; then
  killall -HUP verso 2>/dev/null || /etc/init.d/verso restart 2>/dev/null || true
fi
exit 0
