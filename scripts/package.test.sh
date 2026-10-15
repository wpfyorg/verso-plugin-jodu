#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

./scripts/package.sh
if tar -tzf dist/verso-plugin-jodu5174x-linux-arm64.tar.gz | grep -E '(^|/)\._' >/dev/null; then
  echo 'Router archive contains macOS metadata files.' >&2
  exit 1
fi
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
tar -xzf dist/verso-plugin-jodu5174x-linux-arm64.tar.gz -C "$stage"
test -f "$stage/usr/share/verso/plugins/jodu5174x/default-config"
test -f "$stage/usr/share/rpcd/acl.d/verso-jodu5174x.json"
test -f "$stage/usr/libexec/verso-plugin-jodu5174x/post-install.sh"
test ! -e "$stage/etc/config/jodu5174x"

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
for path in usr etc usr/share/rpcd/acl.d/verso-jodu5174x.json usr/bin/verso-plugin-jodu5174x; do
  test "$(stat -c '%u:%g' "/tmp/package-owner/$path")" = 0:0
done
mkdir -p /etc/config /etc/init.d /tmp/stubs
for service in rpcd verso-plugin-jodu5174x verso; do
  cat >"/etc/init.d/$service" <<'STUB'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >>/tmp/hook-calls
STUB
  chmod 0755 "/etc/init.d/$service"
done
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
PATH="/tmp/no-stat:$PATH" sh /hook
test -f /etc/config/jodu5174x
test "$(stat -c '%a' /etc/config/jodu5174x)" = 600
cmp /etc/config/jodu5174x /usr/share/verso/plugins/jodu5174x/default-config
grep -qx 'rpcd reload' /tmp/hook-calls
grep -qx 'verso-plugin-jodu5174x enable' /tmp/hook-calls
grep -qx 'verso-plugin-jodu5174x restart' /tmp/hook-calls
grep -qx 'killall -HUP verso' /tmp/hook-calls

printf '%s\n' 'preserved-operator-config' >/etc/config/jodu5174x
sh /hook
grep -qx 'preserved-operator-config' /etc/config/jodu5174x
test "$(stat -c '%a' /etc/config/jodu5174x)" = 600

rm /etc/config/jodu5174x
printf '%s\n' 'preserve-symlink-target' >/tmp/operator-config
chmod 0644 /tmp/operator-config
ln -s /tmp/operator-config /etc/config/jodu5174x
if sh /hook; then
  echo 'Symlink config unexpectedly accepted.' >&2
  exit 1
fi
test -L /etc/config/jodu5174x
grep -qx 'preserve-symlink-target' /tmp/operator-config
test "$(stat -c '%a' /tmp/operator-config)" = 644

check_seed_failure() {
  command="$1"
  rm -f /etc/config/jodu5174x
  mkdir -p "/tmp/fail-$command"
  cat >"/tmp/fail-$command/$command" <<'FAIL'
#!/bin/sh
exit 23
FAIL
  chmod 0755 "/tmp/fail-$command/$command"
  if PATH="/tmp/fail-$command:$PATH" sh /hook; then
    echo "seed unexpectedly succeeded when $command failed" >&2
    exit 1
  fi
  test ! -e /etc/config/jodu5174x
  rm -rf "/tmp/fail-$command"
}
check_seed_failure cp
check_seed_failure chmod
check_seed_failure ln

# The installed service must create an isolated non-root socket owner for
# Verso 0.7 peer-credential checks; the config publisher alone remains root.
grep -q '^verso:' /etc/group || echo 'verso:x:6000:' >>/etc/group
grep -q '^verso:' /etc/passwd || echo 'verso:x:6000:6000:Verso:/tmp:/bin/false' >>/etc/passwd
printf '#!/bin/sh\nexit 0\n' >/usr/bin/verso-plugin-jodu5174x
chmod 0755 /usr/bin/verso-plugin-jodu5174x
procd_open_instance() { printf 'instance %s\n' "$*" >>/tmp/procd-calls; }
procd_set_param() { printf 'param %s\n' "$*" >>/tmp/procd-calls; }
procd_close_instance() { :; }
. /service
start_service
start_service
test "$(id -u verso-plugin-jodu5174x)" != 0
test "$(grep -c '^verso-plugin-jodu5174x:' /etc/passwd)" = 1
test "$(stat -c '%u:%a' /var/run/verso-plugin-jodu5174x)" = 0:750
test "$(stat -c '%u:%a' /var/run/verso-plugin-jodu5174x/ui)" = "$(id -u verso-plugin-jodu5174x):750"
grep -qx 'param user verso-plugin-jodu5174x' /tmp/procd-calls
grep -qx 'param group verso' /tmp/procd-calls
grep -qx 'param command /usr/bin/verso-plugin-jodu5174x --export-config /var/run/verso-plugin-jodu5174x/config.json' /tmp/procd-calls
CONTAINER_TEST

docker run --rm --pull=never --network none \
  -v "$PWD/dist/verso-plugin-jodu5174x-linux-arm64.tar.gz:/package:ro" \
  -v "$stage/usr/share/verso/plugins/jodu5174x/default-config:/usr/share/verso/plugins/jodu5174x/default-config:ro" \
  -v "$stage/usr/libexec/verso-plugin-jodu5174x/post-install.sh:/hook:ro" \
  -v "$stage/test-hook.sh:/test-hook.sh:ro" \
  -v "$PWD/rootfs/etc/init.d/verso-plugin-jodu5174x:/service:ro" \
  "$image" /bin/sh /test-hook.sh

if docker run --rm --pull=never --network none \
  -v "$stage/usr/libexec/verso-plugin-jodu5174x/post-install.sh:/hook:ro" \
  "$image" /bin/sh -c 'mkdir -p /etc/config; if /bin/sh /hook; then exit 1; fi; test ! -e /etc/config/jodu5174x'; then
  echo 'Missing-template failure check: ok'
else
  echo 'Missing-template failure check failed.' >&2
  exit 1
fi
echo 'Package path and config preservation checks: ok'
