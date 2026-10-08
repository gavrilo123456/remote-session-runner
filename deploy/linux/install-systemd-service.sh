#!/bin/sh
set -eu
umask 077

service_root='/home/ubuntu/.local/share/remote-session-runner'
unit_source=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)/runnerd.service
entrypoint_source=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)/runnerd-entrypoint.sh
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
go_bin="$service_root/toolchains/go1.27.1/bin/go"
uid=$(id -u)
go_arch=''
# Reuse the selected ubuntu account's normal Go caches. An installer-owned
# module cache forces a full download on every normal service update, produces
# avoidable temporary disk pressure, and can outlive a disconnected installer.
# These shared account caches are never removed by this script.
go_build_cache='/home/ubuntu/.cache/go-build'
go_mod_cache='/home/ubuntu/go/pkg/mod'
temporary=''
source_revision=''
source_origin_revision=''
build_ldflags=''
candidate_startup_attempted=0
candidate_service_quiesced=0
linux_recover_stalled_mode=0
linux_recovery_jobs=''
linux_recovery_pairs=''
linux_recovery_sessions=''

usage() {
	printf '%s\n' "usage: $0 [--recover-stalled [--job-id JOB_ID ...] [--lost-pair SESSION_ID:COMMAND_ID ...] [--lost-session SESSION_ID ...]]" >&2
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--recover-stalled)
			linux_recover_stalled_mode=1
			shift
			;;
		--job-id)
			if [ "$#" -lt 2 ]; then
				usage
				exit 2
			fi
			case "$2" in
				*[!a-z0-9-]*|'')
					printf '%s\n' 'invalid --job-id' >&2
					exit 2
					;;
			esac
			if [ -n "$linux_recovery_jobs" ]; then
				linux_recovery_jobs="$linux_recovery_jobs
$2"
			else
				linux_recovery_jobs=$2
			fi
			shift 2
			;;
		--lost-pair)
			if [ "$#" -lt 2 ]; then
				usage
				exit 2
			fi
			case "$2" in
				*[!a-z0-9:-]*|'')
					printf '%s\n' 'invalid --lost-pair' >&2
					exit 2
					;;
			esac
			if [ -n "$linux_recovery_pairs" ]; then
				linux_recovery_pairs="$linux_recovery_pairs
$2"
			else
				linux_recovery_pairs=$2
			fi
			shift 2
			;;
		--lost-session)
			if [ "$#" -lt 2 ]; then
				usage
				exit 2
			fi
			case "$2" in
				*[!a-z0-9-]*|'')
					printf '%s\n' 'invalid --lost-session' >&2
					exit 2
					;;
			esac
			if [ -n "$linux_recovery_sessions" ]; then
				linux_recovery_sessions="$linux_recovery_sessions
$2"
			else
				linux_recovery_sessions=$2
			fi
			shift 2
			;;
		--help)
			usage
			exit 0
			;;
		*)
			usage
			exit 2
			;;
	esac
done

if [ "$linux_recover_stalled_mode" -eq 0 ] && { [ -n "$linux_recovery_jobs" ] || [ -n "$linux_recovery_pairs" ] || [ -n "$linux_recovery_sessions" ]; }; then
	printf '%s\n' '--job-id, --lost-pair, and --lost-session require --recover-stalled.' >&2
	exit 2
fi
if [ "$linux_recover_stalled_mode" -ne 0 ] && [ -z "$linux_recovery_jobs" ] && [ -z "$linux_recovery_pairs" ] && [ -z "$linux_recovery_sessions" ]; then
	printf '%s\n' '--recover-stalled requires at least one --job-id, --lost-pair, or --lost-session.' >&2
	exit 2
fi

if [ "$(uname -s)" != Linux ] || [ "$(id -un)" != ubuntu ] || [ "$uid" != 1001 ]; then
	printf '%s\n' 'install-systemd-service.sh must run on the selected Linux host as ubuntu (uid 1001)' >&2
	exit 2
fi
case "$(uname -m)" in
	x86_64|amd64) go_arch=amd64 ;;
	aarch64|arm64) go_arch=arm64 ;;
	*) printf 'unsupported Linux architecture for Go 1.27.1: %s\n' "$(uname -m)" >&2; exit 1 ;;
esac
if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain missing: %s\n' "$go_bin" >&2
	exit 1
fi
go_version=$("$go_bin" version)
expected_go_version="go version go1.27.1 linux/$go_arch"
if [ "$go_version" != "$expected_go_version" ]; then
	printf 'expected %s, got: %s\n' "$expected_go_version" "$go_version" >&2
	exit 1
fi

require_source_checkout() {
	if [ ! -d "$repo_root/.git" ] || ! git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		printf 'expected checked-out repository is unavailable: %s\n' "$repo_root" >&2
		exit 1
	fi
	if [ "$(git -C "$repo_root" branch --show-current)" != dev ]; then
		printf '%s\n' 'repository must be on dev before installing runnerd.service' >&2
		exit 1
	fi
	if [ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]; then
		printf '%s\n' 'repository must be clean before installing runnerd.service' >&2
		exit 1
	fi
	source_revision=$(git -C "$repo_root" rev-parse HEAD) || {
		printf '%s\n' 'could not resolve checked-out source revision' >&2
		exit 1
	}
	source_origin_revision=$(git -C "$repo_root" rev-parse origin/dev 2>/dev/null) || {
		printf '%s\n' 'repository does not have origin/dev for runnerd.service installation' >&2
		exit 1
	}
	if ! printf '%s' "$source_revision" | grep -Eq '^[0-9a-f]{40}$'; then
		printf '%s\n' 'source revision has an unexpected format' >&2
		exit 1
	fi
	if [ "$source_revision" != "$source_origin_revision" ]; then
		printf '%s\n' 'repository HEAD does not match origin/dev for runnerd.service installation' >&2
		exit 1
	fi
	build_ldflags="-X remote-session-runner/src/internal/buildinfo.SourceRevision=$source_revision"
}

health_reports_build_revision() {
	socket=$1
	health=$(curl --silent --show-error --fail --unix-socket "$socket" http://runner/health/ready 2>/dev/null) || return 1
	printf '%s' "$health" |
		/usr/bin/python3 -c 'import json,sys; report=json.load(sys.stdin); sys.exit(0 if report.get("build_revision") == sys.argv[1] else 1)' "$source_revision" >/dev/null 2>&1
}

wait_for_build_revision() {
	socket=$1
	label=$2
	for attempt in $(seq 1 40); do
		if health_reports_build_revision "$socket"; then
			return 0
		fi
		sleep 0.25
	done
	printf 'service did not report expected build revision after start: %s\n' "$label" >&2
	return 1
}

quiesce_candidate_service_after_start_failure() {
	if ! sudo -n systemctl stop runnerd.service; then
		printf '%s\n' 'could not stop runnerd.service after candidate startup failed' >&2
		return 1
	fi
	for attempt in $(seq 1 40); do
		if ! sudo -n systemctl is-active --quiet runnerd.service \
			&& [ ! -e "$service_root/run/runnerd.sock" ] \
			&& [ ! -L "$service_root/run/runnerd.sock" ]; then
			return 0
		fi
		sleep 0.25
	done
	printf '%s\n' 'runnerd.service or its private socket remained active after candidate startup failed' >&2
	return 1
}

# stop_runnerd_for_offline_recovery stops only the existing service and waits
# for the listener boundary to disappear. The staged candidate's
# recover-stalled command separately proves systemd's empty-cgroup state while
# holding its lifecycle lock, so a concurrent service start cannot race the
# ownership proof or capacity release.
stop_runnerd_for_offline_recovery() {
	if ! sudo -n systemctl stop runnerd.service; then
		printf '%s\n' 'could not stop runnerd.service for offline recovery' >&2
		return 1
	fi
	for attempt in $(seq 1 40); do
		if ! sudo -n systemctl is-active --quiet runnerd.service \
			&& [ ! -e "$service_root/run/runnerd.sock" ] \
			&& [ ! -L "$service_root/run/runnerd.sock" ]; then
			return 0
		fi
		sleep 0.25
	done
	printf '%s\n' 'runnerd.service or its private socket remained active before offline recovery' >&2
	return 1
}

require_source_checkout

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
	GO="$go_bin" GOOS=linux GOARCH="$go_arch" GOCACHE="$go_build_cache" GOMODCACHE="$go_mod_cache" \
		make -C "$repo_root" test-p128-host-status
}

run_linux_recover_stalled() {
	set -- recover-stalled --config "$service_root/config/linux.yaml" --apply
	previous_ifs=$IFS
	IFS='
'
	for job in $linux_recovery_jobs; do
		set -- "$@" --job-id "$job"
	done
	for pair in $linux_recovery_pairs; do
		set -- "$@" --lost-pair "$pair"
	done
	for session in $linux_recovery_sessions; do
		set -- "$@" --lost-session "$session"
	done
	IFS=$previous_ifs
	"$service_root/bin/runnerd" "$@"
}

# install_candidate replaces only the on-disk candidate. It does not signal or
# restart the running service. In offline recovery mode this happens before
# stopping the old service, so a schema migration followed by a refused
# recovery never leaves the unit pointing at an old binary that cannot reopen
# the migrated authority.
install_candidate() {
	mv -f "$temporary" "$service_root/bin/runnerd"
	temporary=''
	install -m 700 "$entrypoint_source" "$service_root/bin/runnerd-entrypoint.sh"
	sudo -n install -o root -g root -m 644 "$unit_source" /etc/systemd/system/runnerd.service
	sudo -n systemctl daemon-reload
	sudo -n systemd-analyze verify /etc/systemd/system/runnerd.service
	sudo -n systemctl enable runnerd.service
}

# Replacing a binary does not replace an already-running service process.
# Use the checked-in read-only status gate before beginning an active-service
# update, then repeat it immediately before the controlled restart.
cleanup() {
	cleanup_status=$?
	trap - EXIT
	# Do not let a second ordinary termination signal interrupt safe cleanup of
	# the installer-owned temporary tree.
	trap '' HUP INT TERM
	if [ "$cleanup_status" -ne 0 ] && [ "$candidate_startup_attempted" -eq 1 ] && [ "$candidate_service_quiesced" -eq 0 ]; then
		printf '%s\n' 'Candidate startup was attempted; stopping candidate runnerd.service without attempting a rollback.' >&2
		if quiesce_candidate_service_after_start_failure; then
			candidate_service_quiesced=1
		else
			printf '%s\n' 'Could not verify candidate runnerd.service quiescence after candidate startup failed.' >&2
			cleanup_status=1
		fi
	fi
	if [ -n "$temporary" ] && { [ -e "$temporary" ] || [ -L "$temporary" ]; }; then
		if ! rm -f -- "$temporary"; then
			cleanup_status=1
		fi
	fi
	exit "$cleanup_status"
}

temporary="$service_root/bin/.runnerd.$$"
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

was_active=0
if sudo -n systemctl is-active --quiet runnerd.service; then
	was_active=1
	if [ "$linux_recover_stalled_mode" -eq 0 ]; then
		require_no_active_work
	fi
fi

(cd "$repo_root" && GOTOOLCHAIN=local GOOS=linux GOARCH="$go_arch" GOCACHE="$go_build_cache" GOMODCACHE="$go_mod_cache" \
	"$go_bin" build -ldflags "$build_ldflags" -o "$temporary" ./src/cmd/runnerd)
chmod 700 "$temporary"
if [ "$linux_recover_stalled_mode" -eq 1 ]; then
	# Put the schema-compatible candidate in the service path before recovery.
	# The old running process keeps its open executable until the explicit stop.
	install_candidate
	if ! stop_runnerd_for_offline_recovery; then
		exit 1
	fi
	# The common start path must use start, not restart, after this explicit stop.
	was_active=0
	if ! run_linux_recover_stalled; then
		printf '%s\n' 'Stalled Linux recovery did not complete; leaving runnerd.service stopped.' >&2
		exit 1
	fi
	# The candidate has now migrated any supported existing authority and proved
	# all retained capacity is released. This is the same zero-work gate used by
	# an ordinary active-service update, but it runs before a candidate service
	# start.
	require_no_active_work
else
	install_candidate
fi
if [ "$was_active" -eq 1 ]; then
	require_no_active_work
	candidate_startup_attempted=1
	candidate_service_quiesced=0
	sudo -n systemctl restart runnerd.service
else
	candidate_startup_attempted=1
	candidate_service_quiesced=0
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

if ! wait_for_build_revision "$service_root/run/runnerd.sock" 'runnerd.service'; then
	printf '%s\n' 'Build-revision verification failed; stopping candidate runnerd.service without attempting a rollback.' >&2
	if quiesce_candidate_service_after_start_failure; then
		candidate_service_quiesced=1
	else
		printf '%s\n' 'Could not verify candidate runnerd.service quiescence after build-revision verification failed.' >&2
	fi
	exit 1
fi

if [ -e "$queued_bridge_manifest" ] || [ -L "$queued_bridge_manifest" ]; then
	"$repo_root/deploy/ssh/install-queued-bridge.sh" refresh
fi

trap - EXIT HUP INT TERM
printf 'Installed and started runnerd.service as %s with %s at source revision %s\n' "$(id -un)" "$go_version" "$source_revision"
