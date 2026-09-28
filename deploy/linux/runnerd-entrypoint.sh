#!/bin/sh
set -eu

service_root='/home/ubuntu/.local/share/remote-session-runner'
expected_uid=1001

fail_path_check() {
	printf 'runnerd: service path is not ready: %s\n' "$1" >&2
	exit 78
}

if [ "$(id -un)" != ubuntu ] || [ "$(id -u)" != "$expected_uid" ]; then
	fail_path_check 'service account must be ubuntu (uid 1001)'
fi

check_directory() {
	path=$1
	if [ -L "$path" ] || [ ! -d "$path" ]; then
		fail_path_check "$path must be a real directory"
	fi
	if [ "$(stat -c '%u:%a' "$path" 2>/dev/null || true)" != "$expected_uid:700" ]; then
		fail_path_check "$path must be owned by ubuntu with mode 0700"
	fi
}

check_file() {
	path=$1
	mode=$2
	if [ -L "$path" ] || [ ! -f "$path" ]; then
		fail_path_check "$path must be a regular file"
	fi
	if [ "$(stat -c '%u:%a' "$path" 2>/dev/null || true)" != "$expected_uid:$mode" ]; then
		fail_path_check "$path must be owned by ubuntu with mode $mode"
	fi
}

for directory in \
	"$service_root" "$service_root/bin" "$service_root/config" \
	"$service_root/run" "$service_root/state" "$service_root/tmp" \
	"$service_root/tmp/scripts" "$service_root/workspaces" \
	"$service_root/backups" "$service_root/secrets"; do
	check_directory "$directory"
done

check_file "$service_root/bin/runnerd" 700
check_file "$service_root/bin/runnerd-entrypoint.sh" 700
check_file "$service_root/config/linux.yaml" 600
check_file "$service_root/config/client-principals.yaml" 600
check_file "$service_root/secrets/server.pem" 600
check_file "$service_root/secrets/server.key" 600
check_file "$service_root/secrets/client-ca.pem" 600

exec "$service_root/bin/runnerd" --config "$service_root/config/linux.yaml"
