#!/bin/sh
[ -x /etc/init.d/verso-plugin-jodu5174x ] && {
  /etc/init.d/verso-plugin-jodu5174x stop
  /etc/init.d/verso-plugin-jodu5174x disable
}
exit 0
