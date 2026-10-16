#!/bin/sh
[ -x /etc/init.d/verso-plugin-jodu ] && {
  /etc/init.d/verso-plugin-jodu stop
  /etc/init.d/verso-plugin-jodu disable
}
exit 0
