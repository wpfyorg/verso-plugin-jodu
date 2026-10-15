#!/bin/sh
# Verso discovers plugins on start. Enable this service and refresh discovery.
[ -x /etc/init.d/verso-plugin-jodu5174x ] && {
  /etc/init.d/verso-plugin-jodu5174x enable
  /etc/init.d/verso-plugin-jodu5174x restart
}
[ -x /etc/init.d/verso ] && /etc/init.d/verso restart
exit 0
