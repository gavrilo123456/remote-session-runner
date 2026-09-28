#!/bin/sh
set -eu
umask 077

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != tomasz.walczuk ]; then
	printf '%s\n' 'P131 host gate must run on the selected Mac account tomasz.walczuk' >&2
	exit 2
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
go_bin=${GO:-"$service_root/toolchains/go1.27.1/bin/go"}
launch_agents="$HOME/Library/LaunchAgents"
uid=$(id -u)
local_plist="$launch_agents/com.remote-session-runner.local.plist"
locald_plist="$launch_agents/com.remote-session-runner.locald.plist"
api_socket="$service_root/run/local-api.sock"
locald_socket="$service_root/run/locald.sock"
success=0
fixture_created=0

if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain is not executable: %s\n' "$go_bin" >&2
	exit 1
fi
for name in dispatcher_ed25519 direct-client.key direct-client.pem poc-ca.pem ssh_known_hosts; do
	path="$service_root/secrets/$name"
	if [ ! -f "$path" ] || [ -L "$path" ] || [ "$(stat -f '%u' "$path")" != "$uid" ] || [ "$(stat -f '%Lp' "$path")" != 600 ]; then
		printf 'selected owner-only secret file is unavailable: %s\n' "$path" >&2
		exit 1
	fi
done

for path in \
	"$service_root/bin" "$service_root/config" "$service_root/logs" \
	"$service_root/run" "$service_root/state" "$service_root/mailbox" \
	"$service_root/workspaces" "$service_root/tmp" "$service_root/backups" \
	"$local_plist" "$locald_plist"; do
	if [ -e "$path" ] || [ -L "$path" ]; then
		printf 'P131 host gate requires an unused selected service path: %s\n' "$path" >&2
		exit 1
	fi
done
for label in com.remote-session-runner.local com.remote-session-runner.locald; do
	if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
		printf 'P131 host gate requires an unloaded LaunchAgent: %s\n' "$label" >&2
		exit 1
	fi
done

wait_for_stopped() {
	path=$1
	i=0
	while [ "$i" -lt 75 ]; do
		if [ ! -e "$path" ] && [ ! -L "$path" ]; then
			return 0
		fi
		sleep 0.2
		i=$((i + 1))
	done
	printf 'socket remained after launchd stop: %s\n' "$path" >&2
	return 1
}

cleanup_fixture() {
	launchctl bootout "gui/$uid" "$local_plist" >/dev/null 2>&1 || true
	launchctl bootout "gui/$uid" "$locald_plist" >/dev/null 2>&1 || true
	if ! wait_for_stopped "$api_socket" || ! wait_for_stopped "$locald_socket"; then
		printf '%s\n' 'P131 cleanup preserved service files because a process still owns a socket' >&2
		return 1
	fi
	rm -f "$local_plist" "$locald_plist"
	for name in bin config logs run state mailbox workspaces tmp backups; do
		rm -rf "$service_root/$name"
	done
	for path in \
		"$local_plist" "$locald_plist" "$service_root/bin" "$service_root/config" \
		"$service_root/logs" "$service_root/run" "$service_root/state" \
		"$service_root/mailbox" "$service_root/workspaces" "$service_root/tmp" \
		"$service_root/backups"; do
		if [ -e "$path" ] || [ -L "$path" ]; then
			printf 'P131 cleanup left a fixture path behind: %s\n' "$path" >&2
			return 1
		fi
	done
	fixture_created=0
}

cleanup_on_exit() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$fixture_created" -eq 1 ]; then
		if ! cleanup_fixture; then
			status=1
		fi
	fi
	exit "$status"
}
trap cleanup_on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -m 700 "$service_root/config"
fixture_created=1
install -m 600 "$repo_root/deploy/macos/mac.yaml.example" "$service_root/config/mac.yaml"
export GOCACHE=${GOCACHE:-/private/tmp/remote-session-runner-gocache}
export GOMODCACHE=${GOMODCACHE:-/private/tmp/remote-session-runner-gomodcache}
export GOTOOLCHAIN=local

"$repo_root/deploy/macos/install-launchagents.sh"
RSR_P131_HOST_GATE=1 RUNNER_P131_MAC_CONFIG="$service_root/config/mac.yaml" \
	"$go_bin" test -tags=p131host ./src/internal/runnerlocal \
	-run '^TestP131MacGracefulShutdownProcessHost$' -count=1 -v

cleanup_fixture
success=1
printf 'P131 PASS: launchd signal shutdown drained accepted Mac work, flushed event/audit state, closed resumable streams, and restarted both services as %s\n' "$(id -un)"
