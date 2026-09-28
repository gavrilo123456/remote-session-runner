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
sudo -n systemctl enable --now runnerd.service
if ! sudo -n systemctl is-active --quiet runnerd.service; then
	sudo -n systemctl status --no-pager runnerd.service >&2 || true
	printf '%s\n' 'runnerd.service did not become active' >&2
	exit 1
fi

printf 'Installed and started runnerd.service as %s with %s\n' "$(id -un)" "$go_version"
