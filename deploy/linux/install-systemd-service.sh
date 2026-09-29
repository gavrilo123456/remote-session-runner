#!/bin/sh
set -eu

service_root='/home/ubuntu/.local/share/remote-session-runner'
unit_source=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)/runnerd.service
entrypoint_source=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)/runnerd-entrypoint.sh
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
go_bin="$service_root/toolchains/go1.27.1/bin/go"
uid=$(id -u)

if [ "$(uname -s)" != Linux ] || [ "$(id -un)" != ubuntu ] || [ "$uid" != 1001 ]; then
	printf '%s\n' 'install-systemd-service.sh must run on the selected Linux host as ubuntu (uid 1001)' >&2
	exit 2
fi
if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain missing: %s\n' "$go_bin" >&2
	exit 1
fi
go_version=$("$go_bin" version)
case "$go_version" in
	'go version go1.27.1 linux/amd64') ;;
	*) printf 'expected Go 1.27.1 linux/amd64, got: %s\n' "$go_version" >&2; exit 1 ;;
esac

ensure_private_directory() {
	directory=$1
	if [ -L "$directory" ]; then
		printf 'refusing symlinked service directory: %s\n' "$directory" >&2
		exit 1
	fi
	if [ ! -e "$directory" ]; then
		mkdir -p "$directory"
	fi
	if [ ! -d "$directory" ] || [ "$(stat -c '%u' "$directory")" != "$uid" ]; then
		printf 'service directory must be a real directory owned by uid %s: %s\n' "$uid" "$directory" >&2
		exit 1
	fi
	chmod 700 "$directory"
}

for directory in \
	"$service_root" "$service_root/bin" "$service_root/config" \
	"$service_root/run" "$service_root/state" "$service_root/tmp" \
	"$service_root/tmp/scripts" "$service_root/workspaces" \
	"$service_root/backups" "$service_root/secrets"; do
	ensure_private_directory "$directory"
done

require_private_file() {
	path=$1
	if [ -L "$path" ] || [ ! -f "$path" ] || [ "$(stat -c '%u:%a' "$path")" != "$uid:600" ]; then
		printf 'required owner-only file is missing or not mode 0600: %s\n' "$path" >&2
		exit 1
	fi
}

for file in \
	"$service_root/config/linux.yaml" \
	"$service_root/config/client-principals.yaml" \
	"$service_root/secrets/server.pem" \
	"$service_root/secrets/server.key" \
	"$service_root/secrets/client-ca.pem"; do
	require_private_file "$file"
done

queued_bridge_manifest="$service_root/config/queued-ssh-bridge.manifest"
if [ -e "$queued_bridge_manifest" ] || [ -L "$queued_bridge_manifest" ]; then
	"$repo_root/deploy/ssh/install-queued-bridge.sh" preflight
fi

require_no_active_work() {
	GO="$go_bin" make -C "$repo_root" test-p128-host-status
}

# Replacing a binary does not replace an already-running service process.
# Use the checked-in read-only status gate before beginning an active-service
# update, then repeat it immediately before the controlled restart.
was_active=0
if sudo -n systemctl is-active --quiet runnerd.service; then
	was_active=1
	require_no_active_work
fi

temporary="$service_root/bin/.runnerd.$$"
trap 'rm -f "$temporary"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
(cd "$repo_root" && GOTOOLCHAIN=local "$go_bin" build -o "$temporary" ./src/cmd/runnerd)
chmod 700 "$temporary"
mv -f "$temporary" "$service_root/bin/runnerd"
trap - EXIT HUP INT TERM
install -m 700 "$entrypoint_source" "$service_root/bin/runnerd-entrypoint.sh"

sudo -n install -o root -g root -m 644 "$unit_source" /etc/systemd/system/runnerd.service
sudo -n systemctl daemon-reload
sudo -n systemd-analyze verify /etc/systemd/system/runnerd.service
sudo -n systemctl enable runnerd.service
if [ "$was_active" -eq 1 ]; then
	require_no_active_work
	sudo -n systemctl restart runnerd.service
else
	sudo -n systemctl start runnerd.service
fi
if ! sudo -n systemctl is-active --quiet runnerd.service; then
	sudo -n systemctl status --no-pager runnerd.service >&2 || true
	printf '%s\n' 'runnerd.service did not become active' >&2
	exit 1
fi

socket_ready=0
for attempt in $(seq 1 40); do
	if [ ! -L "$service_root/run/runnerd.sock" ] \
		&& [ -S "$service_root/run/runnerd.sock" ] \
		&& [ "$(stat -c '%u:%a' "$service_root/run/runnerd.sock" 2>/dev/null || true)" = "$uid:600" ]; then
		socket_ready=1
		break
	fi
	sleep 0.25
done
if [ "$socket_ready" -ne 1 ]; then
	sudo -n systemctl status --no-pager runnerd.service >&2 || true
	printf '%s\n' 'runnerd.service did not create an ubuntu-owned mode-0600 private socket' >&2
	exit 1
fi

if [ -e "$queued_bridge_manifest" ] || [ -L "$queued_bridge_manifest" ]; then
	"$repo_root/deploy/ssh/install-queued-bridge.sh" refresh
fi

printf 'Installed and started runnerd.service as %s with %s\n' "$(id -un)" "$go_version"
