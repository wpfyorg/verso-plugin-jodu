#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

./scripts/package.sh
archive=dist/verso-plugin-jodu-linux-arm64.tar.gz
if tar -tzf "$archive" | grep -E '(^|/)\._' >/dev/null; then
  echo 'Router archive contains macOS metadata files.' >&2
  exit 1
fi
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
tar -xzf "$archive" -C "$stage"
test -f "$stage/usr/share/verso/plugins/jodu/default-config"
test -f "$stage/usr/share/rpcd/acl.d/verso-jodu.json"
test -f "$stage/usr/libexec/verso-plugin-jodu/post-install.sh"
test ! -e "$stage/etc/config/jodu"
grep -q '"id":"jodu"' "$stage/usr/share/verso/plugins/jodu/manifest.json"
grep -q '"socket":"/var/run/verso-plugin-jodu/ui/jodu.sock"' "$stage/usr/share/verso/plugins/jodu/manifest.json"

image=golang:1.27.1-alpine
if ! command -v docker >/dev/null 2>&1 || ! docker image inspect "$image" >/dev/null 2>&1; then
  echo 'Package paths checked; hook runtime check skipped (cached Docker image unavailable).'
  exit 0
fi

cat >"$stage/test-hook.sh" <<'CONTAINER_TEST'
#!/bin/sh
set -eu
mkdir -p /tmp/package-owner
 tar -xzf /package -C /tmp/package-owner
for path in usr etc usr/share/rpcd/acl.d/verso-jodu.json usr/bin/verso-plugin-jodu; do
  test "$(stat -c '%u:%g' "/tmp/package-owner/$path")" = 0:0
done
mkdir -p /etc/config /etc/init.d /tmp/stubs
for service in rpcd verso-plugin-jodu verso-plugin-jodu5174x verso; do
  cat >"/etc/init.d/$service" <<'STUB'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >>/tmp/hook-calls
STUB
  chmod 0755 "/etc/init.d/$service"
done
cat >/tmp/stubs/uci <<'STUB'
#!/bin/sh
set -eu
configdir=/etc/config
while [ "$#" -gt 0 ]; do
  case "$1" in
    -q) shift ;;
    -c) configdir="$2"; shift 2 ;;
    -P) shift 2 ;;
    get)
      option="${2##*.}"
      case "$option" in
        host) printf '%s\n' '192.0.2.1' ;;
        username) printf '%s\n' 'operator' ;;
        password) printf '%s\n' 'migration-secret-value' ;;
        enabled) printf '%s\n' '1' ;;
        *) exit 1 ;;
      esac
      exit 0
      ;;
    set)
      if [ ! -f "$configdir/jodu" ]; then
        echo 'UCI package missing before set.' >&2
        exit 1
      fi
      printf '%s\n' "$2" >>/tmp/uci-sets
      exit 0
      ;;
    commit)
      cat >"$configdir/jodu" <<'CONFIG'
config jodu 'main'
	option model 'jodu5174x'
	option host '192.0.2.1'
	option username 'operator'
	option password 'migration-secret-value'
	option enabled '1'
CONFIG
      exit 0
      ;;
    *) exit 2 ;;
  esac
done
STUB
chmod 0755 /tmp/stubs/uci
cat >/tmp/stubs/killall <<'STUB'
#!/bin/sh
printf 'killall %s\n' "$*" >>/tmp/hook-calls
STUB
chmod 0755 /tmp/stubs/killall
PATH="/tmp/stubs:$PATH"
export PATH

mkdir -p /tmp/no-stat
printf '#!/bin/sh\nexit 127\n' >/tmp/no-stat/stat
chmod 0755 /tmp/no-stat/stat
PATH="/tmp/no-stat:$PATH" sh /hook >/tmp/hook-output 2>&1
test -f /etc/config/jodu
test "$(stat -c '%a' /etc/config/jodu)" = 600
cmp /etc/config/jodu /usr/share/verso/plugins/jodu/default-config
grep -qx 'rpcd reload' /tmp/hook-calls
grep -qx 'verso-plugin-jodu enable' /tmp/hook-calls
grep -qx 'verso-plugin-jodu restart' /tmp/hook-calls
grep -qx 'killall -HUP verso' /tmp/hook-calls

# Migration copies only the supported settings and never writes the old file.
rm /etc/config/jodu
cat >/etc/config/jodu5174x <<'LEGACY'
config jodu5174x 'main'
	option host '192.0.2.1'
	option username 'operator'
	option password 'migration-secret-value'
	option enabled '1'
	option unsupported 'do-not-copy'
LEGACY
: >/tmp/hook-calls
sh /hook >/tmp/hook-output 2>&1
grep -qx "option model 'jodu5174x'" /etc/config/jodu
grep -qx "option host '192.0.2.1'" /etc/config/jodu
grep -qx "option username 'operator'" /etc/config/jodu
grep -qx "option password 'migration-secret-value'" /etc/config/jodu
grep -qx "option enabled '1'" /etc/config/jodu
! grep -q 'unsupported' /etc/config/jodu
grep -qx "option password 'migration-secret-value'" /etc/config/jodu5174x
test "$(stat -c '%a' /etc/config/jodu)" = 600
! grep -q 'migration-secret-value' /tmp/hook-output

# Existing new-format config wins, and is protected without being rewritten.
printf '%s\n' 'existing-new-config' >/etc/config/jodu
sh /hook >/tmp/hook-output 2>&1
grep -qx 'existing-new-config' /etc/config/jodu
test "$(stat -c '%a' /etc/config/jodu)" = 600

rm /etc/config/jodu
printf '%s\n' 'preserve-symlink-target' >/tmp/operator-config
chmod 0644 /tmp/operator-config
ln -s /tmp/operator-config /etc/config/jodu
if sh /hook >/tmp/hook-output 2>&1; then
  echo 'Symlink config unexpectedly accepted.' >&2
  exit 1
fi
test -L /etc/config/jodu
grep -qx 'preserve-symlink-target' /tmp/operator-config
test "$(stat -c '%a' /tmp/operator-config)" = 644

# The old service is stopped/disabled and its exact stale manifest is removed.
mkdir -p /usr/share/verso/plugins/jodu5174x /usr/share/rpcd/acl.d
: >/usr/share/verso/plugins/jodu5174x/manifest.json
: >/usr/share/rpcd/acl.d/verso-jodu5174x.json
rm /etc/config/jodu
sh /hook >/tmp/hook-output 2>&1
grep -qx 'verso-plugin-jodu5174x stop' /tmp/hook-calls
grep -qx 'verso-plugin-jodu5174x disable' /tmp/hook-calls
grep -qx 'killall -q verso-plugin-jodu5174x' /tmp/hook-calls
test ! -e /usr/share/verso/plugins/jodu5174x/manifest.json
test ! -e /usr/share/rpcd/acl.d/verso-jodu5174x.json

grep -q '^verso:' /etc/group || echo 'verso:x:6000:' >>/etc/group
grep -q '^verso:' /etc/passwd || echo 'verso:x:6000:6000:Verso:/tmp:/bin/false' >>/etc/passwd
printf '#!/bin/sh\nexit 0\n' >/usr/bin/verso-plugin-jodu
chmod 0755 /usr/bin/verso-plugin-jodu
procd_open_instance() { printf 'instance %s\n' "$*" >>/tmp/procd-calls; }
procd_set_param() { printf 'param %s\n' "$*" >>/tmp/procd-calls; }
procd_close_instance() { :; }
. /service
start_service
start_service
test "$(id -u verso-plugin-jodu)" != 0
test "$(grep -c '^verso-plugin-jodu:' /etc/passwd)" = 1
test "$(stat -c '%u:%a' /var/run/verso-plugin-jodu)" = 0:750
test "$(stat -c '%u:%a' /var/run/verso-plugin-jodu/ui)" = "$(id -u verso-plugin-jodu):750"
grep -qx 'param user verso-plugin-jodu' /tmp/procd-calls
grep -qx 'param group verso' /tmp/procd-calls
grep -qx 'param command /usr/bin/verso-plugin-jodu --export-config /var/run/verso-plugin-jodu/config.json' /tmp/procd-calls
CONTAINER_TEST

chmod 0755 "$stage/test-hook.sh"
mkdir -p "$stage/usr/share/verso/plugins/jodu" "$stage/usr/libexec/verso-plugin-jodu"
docker run --rm --pull=never --network none \
  -v "$PWD/$archive:/package:ro" \
  -v "$stage/usr/share/verso/plugins/jodu/default-config:/usr/share/verso/plugins/jodu/default-config:ro" \
  -v "$stage/usr/libexec/verso-plugin-jodu/post-install.sh:/hook:ro" \
  -v "$stage/test-hook.sh:/test-hook.sh:ro" \
  -v "$PWD/rootfs/etc/init.d/verso-plugin-jodu:/service:ro" \
  "$image" /bin/sh /test-hook.sh

echo 'Package path and migration checks: ok'
