#!/bin/sh
# Verso discovers plugins on start. Enable this service and refresh discovery.
config=/etc/config/jodu5174x
template=/usr/share/verso/plugins/jodu5174x/default-config
if [ -e "$config" ] || [ -L "$config" ]; then
  [ -f "$config" ] || { echo 'JODU config path is not a regular file.' >&2; exit 1; }
else
  [ -r "$template" ] || { echo 'JODU default config template is missing.' >&2; exit 1; }
  # Prepare a private same-filesystem inode, then link it into place atomically.
  # ln fails without replacing a config created concurrently by the operator.
  umask 077
  tmp="$(mktemp /etc/config/.jodu5174x.XXXXXX)" || exit 1
  if ! cp "$template" "$tmp" || ! chmod 0600 "$tmp"; then
    rm -f "$tmp"
    echo 'Could not prepare the JODU config.' >&2
    exit 1
  fi
  if ! ln "$tmp" "$config" 2>/dev/null; then
    if [ ! -f "$config" ]; then
      rm -f "$tmp"
      echo 'Could not seed the JODU config.' >&2
      exit 1
    fi
  fi
  rm -f "$tmp"
fi
[ -f "$config" ] || { echo 'JODU config was not created.' >&2; exit 1; }
chmod 0600 "$config" || { echo 'Could not protect the JODU config.' >&2; exit 1; }
mode="$(stat -c '%a' "$config")" || { echo 'Could not verify JODU config mode.' >&2; exit 1; }
[ "$mode" = 600 ] || { echo 'JODU config mode is not 0600.' >&2; exit 1; }
[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload 2>/dev/null
[ -x /etc/init.d/verso-plugin-jodu5174x ] && {
  /etc/init.d/verso-plugin-jodu5174x enable
  /etc/init.d/verso-plugin-jodu5174x restart
}
if [ -x /etc/init.d/verso ]; then
  killall -HUP verso 2>/dev/null || /etc/init.d/verso restart 2>/dev/null
fi
exit 0
