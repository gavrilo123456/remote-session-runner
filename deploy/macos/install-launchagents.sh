#!/bin/sh
set -eu
umask 077

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != tomasz.walczuk ]; then
	printf '%s\n' 'install-launchagents.sh must run on the selected Mac account tomasz.walczuk' >&2
	exit 2
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
config_file="$service_root/config/mac.yaml"
launch_agents="$HOME/Library/LaunchAgents"
go_bin="$service_root/toolchains/go1.27.1/bin/go"
uid=$(id -u)
config_source=''
source_revision=''
source_origin_revision=''
build_ldflags=''
controlled_restart_mode=0
mac_recover_stalled_mode=0
mac_recovery_pairs=''
mac_recovery_sessions=''

usage() {
	printf '%s\n' "usage: $0 [--config /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml] [--b011-controlled-restart] [--recover-stalled [--lost-pair SESSION_ID:COMMAND_ID ...] [--lost-session SESSION_ID ...]]" >&2
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--config)
			if [ "$#" -lt 2 ]; then
				usage
				exit 2
			fi
			config_source=$2
			shift 2
			;;
		--b011-controlled-restart)
			controlled_restart_mode=1
			shift
			;;
		--recover-stalled)
			mac_recover_stalled_mode=1
			shift
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
			if [ -n "$mac_recovery_pairs" ]; then
				mac_recovery_pairs="$mac_recovery_pairs
$2"
			else
				mac_recovery_pairs=$2
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
			if [ -n "$mac_recovery_sessions" ]; then
				mac_recovery_sessions="$mac_recovery_sessions
$2"
			else
				mac_recovery_sessions=$2
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

if [ "$controlled_restart_mode" -ne 0 ] && [ "$mac_recover_stalled_mode" -ne 0 ]; then
	printf '%s\n' 'Controlled restart and stalled recovery cannot be combined.' >&2
	exit 2
fi
if [ "$mac_recover_stalled_mode" -eq 0 ] && { [ -n "$mac_recovery_pairs" ] || [ -n "$mac_recovery_sessions" ]; }; then
	printf '%s\n' '--lost-pair and --lost-session require --recover-stalled.' >&2
	exit 2
fi
if [ "$mac_recover_stalled_mode" -ne 0 ] && [ -z "$mac_recovery_pairs" ] && [ -z "$mac_recovery_sessions" ]; then
	printf '%s\n' '--recover-stalled requires at least one --lost-pair or --lost-session.' >&2
	exit 2
fi

if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain missing: %s\n' "$go_bin" >&2
	exit 1
fi

require_source_checkout() {
	if [ ! -d "$repo_root/.git" ] || ! git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		printf 'expected checked-out repository is unavailable: %s\n' "$repo_root" >&2
		exit 1
	fi
	if [ "$(git -C "$repo_root" branch --show-current)" != dev ]; then
		printf '%s\n' 'repository must be on dev before installing LaunchAgents' >&2
		exit 1
	fi
	if [ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]; then
		printf '%s\n' 'repository must be clean before installing LaunchAgents' >&2
		exit 1
	fi
	source_revision=$(git -C "$repo_root" rev-parse HEAD) || {
		printf '%s\n' 'could not resolve checked-out source revision' >&2
		exit 1
	}
	source_origin_revision=$(git -C "$repo_root" rev-parse origin/dev 2>/dev/null) || {
		printf '%s\n' 'repository does not have origin/dev for LaunchAgent installation' >&2
		exit 1
	}
	if ! printf '%s' "$source_revision" | /usr/bin/grep -Eq '^[0-9a-f]{40}$'; then
		printf '%s\n' 'source revision has an unexpected format' >&2
		exit 1
	fi
	if [ "$source_revision" != "$source_origin_revision" ]; then
		printf '%s\n' 'repository HEAD does not match origin/dev for LaunchAgent installation' >&2
		exit 1
	fi
	build_ldflags="-X remote-session-runner/src/internal/buildinfo.SourceRevision=$source_revision"
}

health_reports_build_revision() {
	socket=$1
	health=$(/usr/bin/curl --silent --show-error --fail --unix-socket "$socket" http://runner/health/ready 2>/dev/null) || return 1
	printf '%s' "$health" |
		/usr/bin/python3 -c 'import json,sys; report=json.load(sys.stdin); sys.exit(0 if report.get("build_revision") == sys.argv[1] else 1)' "$source_revision" >/dev/null 2>&1
}

wait_for_build_revision() {
	socket=$1
	label=$2
	attempt=0
	while [ "$attempt" -lt 75 ]; do
		if health_reports_build_revision "$socket"; then
			return 0
		fi
		sleep 0.2
		attempt=$((attempt + 1))
	done
	printf 'LaunchAgent did not report expected build revision after start: %s\n' "$label" >&2
	return 1
}

ensure_private_directory() {
	private_directory=$1
	if [ -L "$private_directory" ]; then
		printf 'refusing symlinked service directory: %s\n' "$private_directory" >&2
		exit 1
	fi
	if [ ! -e "$private_directory" ]; then
		mkdir "$private_directory"
	fi
	if [ -L "$private_directory" ] || [ ! -d "$private_directory" ] || [ "$(stat -f '%u' "$private_directory")" != "$uid" ]; then
		printf 'service directory must be a real directory owned by uid %s: %s\n' "$uid" "$private_directory" >&2
		exit 1
	fi
	chmod 700 "$private_directory"
}

ensure_private_service_directory() {
	directory=$1
	case "$directory" in
		"$service_root"|"$service_root"/*) ;;
		*)
			printf 'service directory is outside the selected root: %s\n' "$directory" >&2
			exit 1
			;;
	esac
	ensure_private_directory "$service_root"
	if [ "$directory" = "$service_root" ]; then
		return
	fi
	relative=${directory#"$service_root"/}
	current=$service_root
	while [ -n "$relative" ]; do
		part=${relative%%/*}
		current="$current/$part"
		ensure_private_directory "$current"
		if [ "$relative" = "$part" ]; then
			relative=''
		else
			relative=${relative#*/}
		fi
	done
}

ensure_launch_agents_directory() {
	if [ -L "$launch_agents" ]; then
		printf 'refusing symlinked LaunchAgents directory: %s\n' "$launch_agents" >&2
		exit 1
	fi
	if [ ! -e "$launch_agents" ]; then
		mkdir "$launch_agents"
	fi
	if [ -L "$launch_agents" ] || [ ! -d "$launch_agents" ] || [ "$(stat -f '%u' "$launch_agents")" != "$uid" ]; then
		printf 'LaunchAgents directory must be a real directory owned by uid %s: %s\n' "$uid" "$launch_agents" >&2
		exit 1
	fi
	chmod 755 "$launch_agents"
}

ensure_private_regular_file() {
	path=$1
	label=$2
	if [ -L "$path" ] || [ ! -f "$path" ] || [ "$(stat -f '%u' "$path")" != "$uid" ] || [ "$(stat -f '%Lp' "$path")" != 600 ]; then
		printf '%s must be a regular file owned by this account with mode 0600: %s\n' "$label" "$path" >&2
		exit 1
	fi
}

validate_launchagent_files() {
	for name in com.remote-session-runner.locald com.remote-session-runner.local; do
		plist="$repo_root/deploy/macos/launchagents/$name.plist"
		installed="$launch_agents/$name.plist"
		if [ -L "$plist" ] || [ ! -f "$plist" ]; then
			printf 'LaunchAgent source must be a regular non-symlink file: %s\n' "$plist" >&2
			exit 1
		fi
		if [ -L "$installed" ]; then
			printf 'refusing symlinked installed LaunchAgent: %s\n' "$installed" >&2
			exit 1
		fi
		if [ -e "$installed" ] && { [ ! -f "$installed" ] || [ "$(stat -f '%u' "$installed")" != "$uid" ] || [ "$(stat -f '%Lp' "$installed")" != 600 ]; }; then
			printf 'installed LaunchAgent must be a regular file owned by uid %s with mode 0600: %s\n' "$uid" "$installed" >&2
			exit 1
		fi
		/usr/bin/plutil -lint "$plist" >/dev/null
	done
}

service_loaded() {
	launchctl print "gui/$uid/$1" >/dev/null 2>&1
}

# Capture the current launchd child before bootout. A label becoming unloaded
# and its socket disappearing do not prove that its old process is gone. This
# recovery route must not repair the authority while an older Router or locald
# could still be serving or writing it.
capture_loaded_agent_pid() {
	label=$1
	if ! service_loaded "$label"; then
		printf 'Stalled recovery requires a loaded LaunchAgent before its process boundary can be captured: %s\n' "$label" >&2
		return 1
	fi
	attempt=0
	while [ "$attempt" -lt 10 ]; do
		if ! snapshot=$(launchctl print "gui/$uid/$label"); then
			printf 'Could not inspect the loaded LaunchAgent process boundary: %s\n' "$label" >&2
			return 1
		fi
		if pid=$(printf '%s\n' "$snapshot" | /usr/bin/awk '
			/^[[:space:]]*pid = [0-9][0-9]*[[:space:]]*$/ { count++; value=$3 }
			END { if (count != 1 || value !~ /^[1-9][0-9]*$/) exit 1; print value }
		'); then
			printf '%s\n' "$pid"
			return 0
		fi
		sleep 0.2
		attempt=$((attempt + 1))
	done
	printf 'Could not capture one unambiguous loaded LaunchAgent process boundary: %s\n' "$label" >&2
	return 1
}

# A zombie has no running code, descriptors, or SQLite lock. Treat it as an
# inert old service process while keeping the retained command's own zombie
# proof conservative in runner-locald. A live or uninspectable process never
# passes this installer boundary.
agent_pid_presence() {
	/usr/bin/python3 -c '
import os
import sys

pid = int(sys.argv[1])
try:
    os.kill(pid, 0)
except ProcessLookupError:
    print("absent")
except PermissionError:
    print("inaccessible")
except OSError:
    print("inspection_error")
else:
    print("present")
' "$1"
}

wait_for_inert_or_absent_agent_pid() {
	pid=$1
	label=$2
	case "$pid" in
		''|0|*[!0-9]*)
			printf 'Invalid captured LaunchAgent process boundary: %s\n' "$label" >&2
			return 1
			;;
	esac
	attempt=0
	# The installed LaunchAgents have a 15-second ExitTimeOut. Leave a small
	# scheduling margin rather than declaring the just-booted-out old process
	# live at the exact deadline.
	while [ "$attempt" -lt 100 ]; do
		if ! presence=$(agent_pid_presence "$pid"); then
			printf 'Could not inspect captured LaunchAgent process presence: %s\n' "$label" >&2
			return 1
		fi
		case "$presence" in
			absent)
				return 0
				;;
			present)
				if ! state=$(/bin/ps -o stat= -p "$pid" 2>/dev/null); then
					# The process can exit between the presence probe and ps. Accept
					# only a fresh, explicit absence result; any other failure stays
					# fail-closed so an old writer is never mistaken for gone.
					if ! after_presence=$(agent_pid_presence "$pid"); then
						printf 'Could not reinspect a captured LaunchAgent process after state lookup failed: %s\n' "$label" >&2
						return 1
					fi
					if [ "$after_presence" = absent ]; then
						return 0
					fi
					printf 'Could not inspect a still-present LaunchAgent process state: %s\n' "$label" >&2
					return 1
				fi
				state=$(printf '%s' "$state" | /usr/bin/tr -d '[:space:]')
				case "$state" in
					Z*)
						return 0
						;;
					'')
						if ! after_presence=$(agent_pid_presence "$pid"); then
							printf 'Could not reinspect a captured LaunchAgent process after an empty state: %s\n' "$label" >&2
							return 1
						fi
						if [ "$after_presence" = absent ]; then
							return 0
						fi
						printf 'Captured LaunchAgent process state was empty; refusing stalled recovery: %s\n' "$label" >&2
						return 1
						;;
				esac
				;;
			inaccessible|inspection_error|*)
				printf 'Captured LaunchAgent process presence was not provable; refusing stalled recovery: %s\n' "$label" >&2
				return 1
				;;
		esac
		sleep 0.2
		attempt=$((attempt + 1))
	done
	printf 'LaunchAgent process remained live after quiescence; refusing stalled recovery: %s\n' "$label" >&2
	return 1
}

wait_for_absent_path() {
	path=$1
	attempt=0
	while [ "$attempt" -lt 75 ]; do
		if [ ! -e "$path" ] && [ ! -L "$path" ]; then
			return 0
		fi
		sleep 0.2
		attempt=$((attempt + 1))
	done
	printf 'socket remained after LaunchAgent quiescence: %s\n' "$path" >&2
	return 1
}

stop_agent_for_config_change() {
	label=$1
	plist=$2
	socket=$3
	if service_loaded "$label"; then
		if [ -L "$plist" ] || [ ! -f "$plist" ]; then
			printf 'loaded LaunchAgent has no safe installed plist: %s\n' "$label" >&2
			exit 1
		fi
		launchctl bootout "gui/$uid" "$plist"
	fi
	if service_loaded "$label"; then
		printf 'LaunchAgent remained loaded after quiescence: %s\n' "$label" >&2
		exit 1
	fi
	wait_for_absent_path "$socket"
}

# run_mac_recover_stalled passes the explicit complete lost-pair and/or
# commandless lost-session set to the staged candidate only after both Mac
# LaunchAgents have been quiesced. The candidate command proves every selected
# process boundary, releases only matching durable capacity, and never reads or
# replays a stored script. Keep values as distinct argv entries; runner-locald
# rejects malformed, duplicate, omitted, or extra recovery inputs before it
# changes authority state.
run_mac_recover_stalled() {
	set -- recover-stalled --config "$config_file" --apply
	previous_ifs=$IFS
	IFS='
'
	for pair in $mac_recovery_pairs; do
		set -- "$@" --lost-pair "$pair"
	done
	for session in $mac_recovery_sessions; do
		set -- "$@" --lost-session "$session"
	done
	IFS=$previous_ifs
	"$staging_directory/runner-locald" "$@"
}

# install_staged_recovery_candidate_binaries changes only the stopped LaunchAgent
# executable paths. The recovery route calls it after it has proved both old
# processes inert and before it can open or migrate local.db. A recovery
# refusal therefore cannot revive an older binary against a newer schema.
install_staged_recovery_candidate_binaries() {
	for name in runner runner-local runner-locald; do
		staged="$staging_directory/$name"
		installed="$service_root/bin/$name"
		if [ -L "$staged" ] || [ ! -f "$staged" ] || [ ! -x "$staged" ]; then
			printf 'staged candidate binary is unavailable: %s\n' "$staged" >&2
			return 1
		fi
		candidate_copy="$service_root/bin/.${name}.candidate.$$"
		if [ -e "$candidate_copy" ] || [ -L "$candidate_copy" ]; then
			printf 'candidate binary staging path already exists: %s\n' "$candidate_copy" >&2
			return 1
		fi
		if ! cp "$staged" "$candidate_copy" || ! chmod 700 "$candidate_copy" || ! mv -f "$candidate_copy" "$installed"; then
			rm -f "$candidate_copy" || true
			printf 'could not install stopped candidate binary: %s\n' "$installed" >&2
			return 1
		fi
	done
	candidate_binaries_installed=1
}

# The controlled-restart route is deliberately separate from normal service
# replacement. It freezes only the old local executor before a durable plan is
# written, so its queue worker cannot claim the preserved command between plan
# preparation and hard-stop. KeepAlive is disabled first. A suspended process
# that still owns a SQLite write transaction makes plan preparation fail
# closed; it is restored before the installer exits without a plan.
#
# On the selected Mac, a disposable KeepAlive LaunchAgent proved that disable
# alone does not suppress an already loaded job after SIGKILL. The safe
# handoff therefore keeps the old executor SIGSTOPed, bootouts the label while
# it is frozen, proves it unloaded, and only then re-enables that label for a
# candidate bootstrap. The frozen old process cannot run graceful cleanup in
# that interval. A booted-out label alone is not proof that its frozen process
# exited, so the socket-boundary gate below must pass before a candidate can
# be enabled.
controlled_restart_suspend_locald() {
	label=com.remote-session-runner.locald
	if [ "$locald_was_loaded" -ne 1 ] || ! service_loaded "$label"; then
		printf '%s\n' 'Controlled restart requires the active runner-locald LaunchAgent to be loaded.' >&2
		return 1
	fi
	if [ -L "$launch_agents/$label.plist" ] || [ ! -f "$launch_agents/$label.plist" ]; then
		printf 'controlled restart has no safe installed LaunchAgent plist: %s\n' "$label" >&2
		return 1
	fi
	# Record rollback intent before every signal-affecting launchctl call. A
	# HUP/INT/TERM is delivered after the shell regains control, so the EXIT trap
	# can re-enable or kickstart even if launchctl succeeded immediately before
	# the signal. Keep the intent on reported failure too: launchd may have
	# changed state before returning a failure.
	controlled_locald_disabled=1
	if ! launchctl disable "gui/$uid/$label"; then
		printf 'could not disable runner-locald KeepAlive before controlled restart: %s\n' "$label" >&2
		return 1
	fi
	controlled_locald_suspended=1
	if ! launchctl kill SIGSTOP "gui/$uid/$label"; then
		printf 'could not suspend runner-locald before controlled restart: %s\n' "$label" >&2
		return 1
	fi
	if ! service_loaded "$label"; then
		printf 'runner-locald disappeared while being suspended for controlled restart: %s\n' "$label" >&2
		return 1
	fi
}

controlled_restart_restore_suspended_locald() {
	label=com.remote-session-runner.locald
	controlled_restart_refresh_failure_boundary
	if [ "$controlled_restart_plan_prepared" -ne 0 ] || [ "$controlled_restart_old_agents_restore_allowed" -ne 1 ]; then
		return 0
	fi
	if [ "$controlled_locald_disabled" -ne 0 ]; then
		if ! launchctl enable "gui/$uid/$label"; then
			printf 'could not re-enable runner-locald after controlled-restart preparation failure: %s\n' "$label" >&2
			return 1
		fi
		controlled_locald_disabled=0
	fi
	if [ "$controlled_locald_suspended" -ne 0 ]; then
		if ! launchctl kickstart -k "gui/$uid/$label"; then
			printf 'could not restart runner-locald after controlled-restart preparation failure: %s\n' "$label" >&2
			return 1
		fi
		controlled_locald_suspended=0
	fi
}

controlled_restart_hard_stop_locald() {
	label=com.remote-session-runner.locald
	plist="$launch_agents/$label.plist"
	if [ "$controlled_restart_plan_prepared" -ne 1 ] || [ "$controlled_locald_suspended" -ne 1 ]; then
		printf '%s\n' 'Controlled restart hard-stop requires a prepared plan and suspended old local executor.' >&2
		return 1
	fi
	if ! launchctl bootout "gui/$uid" "$plist"; then
		printf 'could not unload frozen runner-locald after controlled-restart plan preparation: %s\n' "$label" >&2
		return 1
	fi
	if service_loaded "$label"; then
		printf 'runner-locald remained loaded after controlled frozen bootout: %s\n' "$label" >&2
		return 1
	fi
	controlled_locald_suspended=0
}

# launchd can report a label as unloaded while a frozen old process still owns
# its Unix listener. The staged helper opens no database; it validates the
# active owner-only config/root and removes only a stale socket. A live,
# foreign, non-socket, or changing path fails closed before any candidate is
# enabled.
controlled_restart_prove_old_locald_socket_boundary() {
	if ! "$staging_directory/runner-locald" controlled-restart-socket-boundary --config "$config_file"; then
		printf '%s\n' 'Could not prove that the old runner-locald process boundary is clear before candidate bootstrap.' >&2
		return 1
	fi
}

# The label was disabled while the old executor was frozen. Hold for the
# exact ThrottleInterval declared by the source plist after clearing that
# override, before its first candidate bootstrap.
controlled_restart_wait_for_launchd_throttle() {
	label=com.remote-session-runner.locald
	plist="$repo_root/deploy/macos/launchagents/$label.plist"
	throttle=$(/usr/libexec/PlistBuddy -c 'Print :ThrottleInterval' "$plist" 2>/dev/null) || {
		printf 'could not read runner-locald ThrottleInterval for controlled restart: %s\n' "$label" >&2
		return 1
	}
	case "$throttle" in
		''|*[!0-9]*)
			printf 'runner-locald ThrottleInterval is invalid for controlled restart: %s\n' "$label" >&2
			return 1
			;;
	esac
	if [ "$throttle" -le 0 ]; then
		printf 'runner-locald ThrottleInterval is invalid for controlled restart: %s\n' "$label" >&2
		return 1
	fi
	sleep "$throttle"
}

# A prepared or active durable plan may be resumed after a shell crash between
# frozen bootout and candidate bootstrap. Always clear only this Runner label's
# disabled override before installing the candidate, including that resume.
controlled_restart_enable_candidate_locald() {
	label=com.remote-session-runner.locald
	if ! launchctl enable "gui/$uid/$label"; then
		printf 'could not re-enable runner-locald for candidate bootstrap: %s\n' "$label" >&2
		return 1
	fi
	controlled_locald_disabled=0
	controlled_restart_wait_for_launchd_throttle
}

# This command opens the existing authority read-only and reports only its
# durable restart boundary: legacy, prepared, active, or migrated-without-plan.
# A read failure is unsafe for rollback because prepare may already have
# migrated the database before this shell observed its result.
controlled_restart_read_state() {
	"$staging_directory/runner-locald" controlled-restart-status --config "$config_file"
}

# Once prepare-controlled-restart has been invoked, a signal can arrive after
# its migration commits but before this installer stores a shell variable. The
# decision to revive old agents must therefore come from the durable database,
# not from whether this script reached its next line.
controlled_restart_refresh_failure_boundary() {
	if [ "$controlled_restart_prepare_invoked" -ne 1 ]; then
		return 0
	fi
	controlled_restart_old_agents_restore_allowed=0
	if state=$(controlled_restart_read_state 2>/dev/null); then
		controlled_restart_state=$state
		if [ "$state" = legacy ]; then
			controlled_restart_old_agents_restore_allowed=1
			return 0
		fi
	fi
	candidate_activation_started=1
	return 0
}

# A prior local executor may own local processes or durable work even when its
# LaunchAgent is down. After ingress is quiesced, always check the active
# authority before stopping or replacing runner-locald. The staged binary opens
# it in SQLite read-only mode; it never performs health writes, recovery, or
# cleanup. A missing authority is permitted only for a true first install with
# no prior executor artifact; all other uncertainty fails closed.
is_true_first_locald_install() {
	if [ "$locald_was_loaded" -ne 0 ]; then
		return 1
	fi
	for path in \
		"$service_root/bin/runner-locald" \
		"$launch_agents/com.remote-session-runner.locald.plist" \
		"$service_root/run/locald.sock" \
		"$service_root/state/local.db" \
		"$service_root/state/local.db-wal" \
		"$service_root/state/local.db-shm" \
		"$service_root/state/local.db-journal"; do
		if [ -e "$path" ] || [ -L "$path" ]; then
			return 1
		fi
	done
	return 0
}

preflight_active_locald_restart() {
	if "$staging_directory/runner-locald" preflight-restart --config "$config_file"; then
		return 0
	else
		preflight_status=$?
	fi
	if [ "$preflight_status" -eq 3 ] && is_true_first_locald_install; then
		printf '%s\n' 'No existing local authority was found; continuing the true first local executor install.' >&2
		return 0
	fi
	printf '%s\n' 'Refusing runner-locald refresh because local execution is not provably quiescent.' >&2
	return 1
}

stop_candidate_agent_after_start_failure() {
	label=$1
	plist=$2
	socket=$3
	failed=0
	if service_loaded "$label" && ! launchctl bootout "gui/$uid" "$plist"; then
		printf 'could not stop candidate LaunchAgent after candidate startup failed: %s\n' "$label" >&2
		failed=1
	fi
	if service_loaded "$label"; then
		printf 'candidate LaunchAgent remained loaded after candidate startup failed: %s\n' "$label" >&2
		failed=1
	fi
	if ! wait_for_absent_path "$socket"; then
		failed=1
	fi
	return "$failed"
}

quiesce_candidate_agents_after_start_failure() {
	failed=0
	if ! stop_candidate_agent_after_start_failure com.remote-session-runner.local "$launch_agents/com.remote-session-runner.local.plist" "$service_root/run/local-api.sock"; then
		failed=1
	fi
	if ! stop_candidate_agent_after_start_failure com.remote-session-runner.locald "$launch_agents/com.remote-session-runner.locald.plist" "$service_root/run/locald.sock"; then
		failed=1
	fi
	return "$failed"
}

restore_prior_agents() {
	restore_failed=0
	if ! controlled_restart_restore_suspended_locald; then
		restore_failed=1
	fi
	if [ "$controlled_restart_old_agents_restore_allowed" -eq 1 ] && [ "$locald_was_loaded" -eq 1 ] && ! service_loaded com.remote-session-runner.locald; then
		if ! launchctl bootstrap "gui/$uid" "$launch_agents/com.remote-session-runner.locald.plist" || ! launchctl kickstart -k "gui/$uid/com.remote-session-runner.locald"; then
			restore_failed=1
		fi
	fi
	if [ "$controlled_restart_old_agents_restore_allowed" -eq 1 ] && [ "$local_was_loaded" -eq 1 ] && ! service_loaded com.remote-session-runner.local; then
		if ! launchctl bootstrap "gui/$uid" "$launch_agents/com.remote-session-runner.local.plist" || ! launchctl kickstart -k "gui/$uid/com.remote-session-runner.local"; then
			restore_failed=1
		fi
	fi
	if [ "$restore_failed" -ne 0 ]; then
		printf '%s\n' 'Could not restore one or more prior LaunchAgents; active mac.yaml was left unchanged.' >&2
		return 1
	fi
}

staging_directory=''
config_stage=''
local_was_loaded=0
locald_was_loaded=0
mac_recovery_local_pid=''
mac_recovery_locald_pid=''
mac_recovery_resume_mode=0
mac_recovery_candidate_boundary=0
candidate_binaries_installed=0
# Set to zero before the recovery route boots out either old LaunchAgent. The
# EXIT trap may revive the old pair only after both captured old processes are
# proven inert or absent; otherwise it could create a second writer.
mac_recovery_old_process_boundary_confirmed=1
controlled_locald_disabled=0
controlled_locald_suspended=0
controlled_restart_plan_prepared=0
controlled_restart_prepare_invoked=0
controlled_restart_old_agents_restore_allowed=1
controlled_restart_state=''
restore_prior_agents_on_failure=0
candidate_activation_started=0
candidate_config_handed_off=0
candidate_startup_attempted=0
candidate_services_quiesced=0
cleanup_staging() {
	if [ -n "$config_stage" ] && [ -e "$config_stage" ]; then
		# Once the no-rollback boundary is crossed this staged, owner-only copy
		# is the recovery candidate if an atomic handoff has not completed.
		if [ "$candidate_activation_started" -eq 1 ] && [ "$candidate_config_handed_off" -eq 0 ]; then
			:
		elif ! rm -f "$config_stage"; then
			printf 'could not remove installer config staging file: %s\n' "$config_stage" >&2
		fi
	fi
	if [ -n "$staging_directory" ] && [ -d "$staging_directory" ] && [ ! -L "$staging_directory" ]; then
		if [ "$candidate_activation_started" -eq 1 ] && [ "$candidate_config_handed_off" -eq 0 ]; then
			:
		elif ! rm -rf "$staging_directory"; then
			printf 'could not remove installer staging directory: %s\n' "$staging_directory" >&2
		fi
	fi
}
on_exit() {
	status=$?
	if [ "$status" -ne 0 ] && [ "$mac_recover_stalled_mode" -eq 1 ] && [ "$mac_recovery_candidate_boundary" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ]; then
		if [ "$candidate_binaries_installed" -eq 1 ]; then
			printf '%s\n' 'Stalled Mac recovery left candidate binaries installed and both LaunchAgents stopped; correct the complete evidence set and rerun the same installer command.' >&2
		else
			printf '%s\n' 'Stalled Mac candidate-install/recovery boundary was entered; both LaunchAgents remain stopped. Rerun the same installer command after correcting the reported failure.' >&2
		fi
	elif [ "$status" -ne 0 ] && [ "$controlled_restart_mode" -eq 1 ] && [ "$controlled_restart_prepare_invoked" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ]; then
		controlled_restart_refresh_failure_boundary
	fi
	if [ "$status" -ne 0 ] && [ "$restore_prior_agents_on_failure" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ] && [ "$mac_recovery_candidate_boundary" -eq 0 ] && [ "$mac_recovery_old_process_boundary_confirmed" -eq 1 ]; then
		printf '%s\n' 'Install stopped before candidate activation; restoring prior LaunchAgents and active mac.yaml.' >&2
		if ! restore_prior_agents; then
			printf '%s\n' 'Restore failed before candidate activation.' >&2
		fi
	elif [ "$status" -ne 0 ] && [ "$restore_prior_agents_on_failure" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ] && [ "$mac_recovery_candidate_boundary" -eq 0 ] && [ "$mac_recovery_old_process_boundary_confirmed" -eq 0 ]; then
		printf '%s\n' 'Stalled recovery stopped before the old service-process boundary was proven; prior LaunchAgents were not restarted automatically.' >&2
	elif [ "$status" -ne 0 ] && [ "$candidate_activation_started" -eq 1 ]; then
		if [ "$candidate_config_handed_off" -eq 1 ]; then
			if [ "$candidate_startup_attempted" -eq 1 ] && [ "$candidate_services_quiesced" -eq 0 ]; then
				printf '%s\n' 'Candidate startup was attempted; quiescing candidate LaunchAgents without rolling back the active candidate configuration.' >&2
				if quiesce_candidate_agents_after_start_failure; then
					candidate_services_quiesced=1
				else
					printf '%s\n' 'Could not verify candidate LaunchAgent quiescence after candidate startup failed.' >&2
				fi
			fi
			if [ "$candidate_services_quiesced" -eq 1 ]; then
				printf '%s\n' 'Candidate configuration is active; candidate LaunchAgents are confirmed stopped for safe repair.' >&2
			else
				printf '%s\n' 'Candidate configuration is active; candidate LaunchAgent quiescence was not confirmed. Inspect launchd and both private sockets before repair.' >&2
			fi
		elif [ -n "$config_stage" ] && [ -f "$config_stage" ]; then
			printf 'Candidate activation stopped before config handoff; staged candidate retained at: %s\n' "$config_stage" >&2
			printf 'Repair by rerunning: %s --config %s\n' "$0" "$config_stage" >&2
		elif [ -n "$config_source" ]; then
			printf 'Candidate activation stopped before config handoff; original candidate remains at: %s\n' "$config_source" >&2
			printf 'Repair by rerunning: %s --config %s\n' "$0" "$config_source" >&2
		else
			printf '%s\n' 'Candidate activation stopped; active mac.yaml is the candidate. Rerun the installer after repair.' >&2
		fi
	fi
	cleanup_staging
	trap - EXIT
	exit "$status"
}
trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

for directory in \
	"$service_root" "$service_root/bin" "$service_root/config" "$service_root/logs" \
	"$service_root/run" "$service_root/state" "$service_root/mailbox" \
	"$service_root/mailbox/inbox" "$service_root/mailbox/outbox" \
	"$service_root/mailbox/events" "$service_root/mailbox/acks" "$service_root/mailbox/diagnostics" \
	"$service_root/workspaces" "$service_root/tmp" "$service_root/tmp/scripts" \
	"$service_root/backups" "$service_root/secrets"; do
	ensure_private_service_directory "$directory"
done
ensure_launch_agents_directory

if [ -L "$config_file" ]; then
	printf 'refusing symlinked Mac config: %s\n' "$config_file" >&2
	exit 1
fi
if [ -z "$config_source" ] && [ ! -e "$config_file" ]; then
	install -m 600 "$repo_root/deploy/macos/mac.yaml.example" "$config_file"
	printf 'Created selected Mac config for review: %s\nRerun this installer after reviewing it.\n' "$config_file"
	exit 0
fi

if [ -e "$config_file" ]; then
	ensure_private_regular_file "$config_file" 'Mac config'
fi
if [ -z "$config_source" ] && [ ! -f "$config_file" ]; then
	printf 'Mac config is not a regular file: %s\n' "$config_file" >&2
	exit 1
fi

if [ -n "$config_source" ]; then
	if [ "$controlled_restart_mode" -ne 0 ]; then
		printf '%s\n' 'Controlled restart must use the active mac.yaml; it cannot combine a staged configuration change with live queued-work preservation.' >&2
		exit 1
	fi
	if [ "$mac_recover_stalled_mode" -ne 0 ]; then
		printf '%s\n' 'Stalled recovery must use the active mac.yaml; it cannot combine a staged configuration change with retained-work repair.' >&2
		exit 1
	fi
	config_directory=$(CDPATH= cd -- "$service_root/config" && pwd -P)
	candidate_directory=$(CDPATH= cd -- "$(dirname -- "$config_source")" && pwd -P) || {
		printf 'staged Mac config parent is unavailable: %s\n' "$config_source" >&2
		exit 1
	}
	if [ "$candidate_directory" != "$config_directory" ]; then
		printf 'staged Mac config must be inside the selected config directory: %s\n' "$config_source" >&2
		exit 1
	fi
	config_source="$candidate_directory/$(basename -- "$config_source")"
	if [ "$config_source" = "$config_file" ]; then
		printf '%s\n' 'pass a separate staged config to --config; do not replace the active mac.yaml directly' >&2
		exit 1
	fi
	ensure_private_regular_file "$config_source" 'Staged Mac config'
fi

require_source_checkout

validate_launchagent_files

staging_directory="$service_root/tmp/installer.$$"
if [ -e "$staging_directory" ] || [ -L "$staging_directory" ]; then
	printf 'installer staging path already exists: %s\n' "$staging_directory" >&2
	exit 1
fi
mkdir -m 700 "$staging_directory"

selected_config=$config_file
if [ -n "$config_source" ]; then
	config_stage="$service_root/config/.mac.yaml.install.$$"
	if [ -e "$config_stage" ] || [ -L "$config_stage" ]; then
		printf 'installer config staging path already exists: %s\n' "$config_stage" >&2
		exit 1
	fi
	install -m 600 "$config_source" "$config_stage"
	selected_config=$config_stage
fi

for name in runner runner-local runner-locald; do
	temporary="$staging_directory/$name"
	(cd "$repo_root" && GOTOOLCHAIN=local "$go_bin" build -ldflags "$build_ldflags" -o "$temporary" "./src/cmd/$name")
	chmod 700 "$temporary"
done

# A configuration candidate is staged while the current ingress remains live.
# Inspect external parent chains and any existing tree without creating a
# candidate mailbox. The retained-state check must happen only after ingress
# is quiesced: a request accepted during the build/static/preflight window is
# then visible to the final check and cannot be stranded by an inbox removal.
"$staging_directory/runner-local" validate-config --config "$selected_config"
"$staging_directory/runner-local" validate-config --check-mailbox-directories --config "$selected_config"

if service_loaded com.remote-session-runner.local; then
	local_was_loaded=1
fi
if service_loaded com.remote-session-runner.locald; then
	locald_was_loaded=1
fi
if [ "$controlled_restart_mode" -eq 1 ]; then
	if ! controlled_restart_state=$(controlled_restart_read_state); then
		printf '%s\n' 'Could not read the durable controlled-restart state without changing the active authority.' >&2
		exit 1
	fi
	case "$controlled_restart_state" in
	legacy)
		if [ "$locald_was_loaded" -ne 1 ]; then
			printf '%s\n' 'A new controlled restart requires the active runner-locald LaunchAgent; no durable plan exists to resume.' >&2
			exit 1
		fi
		;;
	prepared)
		# A prior installer may have created the plan and stopped the old
		# executor before its candidate bootstrap completed. Resume it below.
		;;
	active)
		if [ "$locald_was_loaded" -eq 1 ]; then
			printf '%s\n' 'An active controlled restart still has a loaded executor; refusing to interrupt its candidate runtime.' >&2
			exit 1
		fi
		;;
	migrated-without-plan)
		printf '%s\n' 'The authority has the controlled-restart schema but no durable plan; refusing to revive or replace the executor automatically.' >&2
		exit 1
		;;
	*)
		printf '%s\n' 'The durable controlled-restart state is invalid; refusing service replacement.' >&2
		exit 1
		;;
	esac
fi

if [ "$mac_recover_stalled_mode" -eq 1 ]; then
	# This explicit offline route is intentionally narrower than a normal
	# refresh: it needs the old process identities before bootout, so it can
	# prove no old Router/locald writer survives into authority repair.
	if [ "$local_was_loaded" -eq 1 ] && [ "$locald_was_loaded" -eq 1 ]; then
		mac_recovery_local_pid=$(capture_loaded_agent_pid com.remote-session-runner.local) || exit 1
		mac_recovery_locald_pid=$(capture_loaded_agent_pid com.remote-session-runner.locald) || exit 1
	elif [ "$local_was_loaded" -eq 0 ] && [ "$locald_was_loaded" -eq 0 ] \
		&& [ ! -e "$service_root/run/local-api.sock" ] && [ ! -L "$service_root/run/local-api.sock" ] \
		&& [ ! -e "$service_root/run/locald.sock" ] && [ ! -L "$service_root/run/locald.sock" ]; then
		# A prior candidate-first recovery refusal deliberately leaves this
		# stopped boundary. It is safe to rebuild and retry the same exact set.
		mac_recovery_resume_mode=1
	else
		printf '%s\n' 'Stalled recovery requires both old LaunchAgents loaded, or both unloaded with both private sockets absent for a safe retry.' >&2
		exit 1
	fi
fi
# Until the candidate begins replacing active artifacts, any failure restores
# exactly the pre-install LaunchAgent state. Set this before stopping either
# job so a failure while quiescing the second job also restarts the first.
restore_prior_agents_on_failure=1
if [ "$mac_recover_stalled_mode" -eq 1 ] && [ "$mac_recovery_resume_mode" -eq 1 ]; then
	# The retry path accepts only the stopped boundary left by a prior candidate
	# recovery attempt. It never tries to infer an old process boundary.
	wait_for_absent_path "$service_root/run/local-api.sock"
	wait_for_absent_path "$service_root/run/locald.sock"
else
	if [ "$mac_recover_stalled_mode" -eq 1 ]; then
		# Record this before the first bootout. A signal or shell failure immediately
		# afterward must not cause the EXIT trap to bootstrap a second old process.
		mac_recovery_old_process_boundary_confirmed=0
	fi
	stop_agent_for_config_change com.remote-session-runner.local "$launch_agents/com.remote-session-runner.local.plist" "$service_root/run/local-api.sock"
	if [ "$mac_recover_stalled_mode" -eq 1 ]; then
		if ! wait_for_inert_or_absent_agent_pid "$mac_recovery_local_pid" com.remote-session-runner.local; then
			exit 1
		fi
		stop_agent_for_config_change com.remote-session-runner.locald "$launch_agents/com.remote-session-runner.locald.plist" "$service_root/run/locald.sock"
		if ! wait_for_inert_or_absent_agent_pid "$mac_recovery_locald_pid" com.remote-session-runner.locald; then
			exit 1
		fi
		mac_recovery_old_process_boundary_confirmed=1
	fi
fi
if [ "$controlled_restart_mode" -eq 1 ]; then
	case "$controlled_restart_state" in
	legacy)
		controlled_restart_suspend_locald
		# This flag comes before the candidate invocation so the EXIT trap uses
		# durable state even if it receives a signal between the migration commit
		# and the next shell assignment.
		controlled_restart_prepare_invoked=1
		if ! "$staging_directory/runner-locald" prepare-controlled-restart --config "$config_file"; then
			controlled_restart_refresh_failure_boundary
			if [ "$controlled_restart_old_agents_restore_allowed" -eq 1 ]; then
				printf '%s\n' 'Controlled restart preparation did not change the authority; restoring the suspended active executor.' >&2
			else
				printf '%s\n' 'Controlled restart preparation crossed a candidate boundary; retaining the candidate for safe repair or resume.' >&2
			fi
			exit 1
		fi
		controlled_restart_plan_prepared=1
		# The durable plan migration makes the prior binary unsafe to revive. The
		# hard-stop sequence was proved against a disposable KeepAlive LaunchAgent:
		# disabled -> SIGSTOP -> bootout frozen old label -> enable, then candidate
		# bootstrap below. Never SIGKILL a still loaded KeepAlive label.
		candidate_activation_started=1
		controlled_restart_hard_stop_locald
		;;
	prepared)
		controlled_restart_plan_prepared=1
		candidate_activation_started=1
		if [ "$locald_was_loaded" -eq 1 ]; then
			controlled_restart_suspend_locald
			controlled_restart_hard_stop_locald
		fi
		;;
	active)
		# The candidate died after activation but before its exact scheduler
		# claim. Restarting the current candidate rehydrates the same generation.
		candidate_activation_started=1
		;;
	esac
	if ! controlled_restart_prove_old_locald_socket_boundary; then
		exit 1
	fi
	if ! controlled_restart_enable_candidate_locald; then
		exit 1
	fi
else
	if [ "$mac_recover_stalled_mode" -eq 1 ]; then
		# This is an irreversible candidate boundary: recovery may migrate local.db.
		# Install the stopped candidate before it can open the authority and never
		# revive the older executable after this point.
		mac_recovery_candidate_boundary=1
		if ! install_staged_recovery_candidate_binaries; then
			printf '%s\n' 'Could not install the stopped Mac recovery candidate; leaving both LaunchAgents stopped.' >&2
			exit 1
		fi
		# The recovery command has its own exact offline inventory gate. It may run
		# only after ingress and the old local executor are inert, so no worker can
		# race the explicit lost-runtime proof/release.
		if ! run_mac_recover_stalled; then
			printf '%s\n' 'Stalled Mac recovery did not complete; leaving candidate binaries installed and both LaunchAgents stopped.' >&2
			exit 1
		fi
	fi
	preflight_active_locald_restart
	if [ "$mac_recover_stalled_mode" -eq 0 ]; then
		stop_agent_for_config_change com.remote-session-runner.locald "$launch_agents/com.remote-session-runner.locald.plist" "$service_root/run/locald.sock"
	fi
fi
candidate_services_quiesced=1

if ! "$staging_directory/runner-local" validate-config --check-retained-mailboxes --config "$selected_config"; then
	printf '%s\n' 'Retained mailbox validation failed after ingress quiescence.' >&2
	exit 1
fi

# This is the irreversible boundary. A same-user file producer can publish a
# marker-last request as soon as the candidate layout is visible, and the
# activation command may migrate a schema-24 database that the prior V1 binary
# cannot reopen. Do not revive the prior configuration after this point: the
# staged runner-local records the complete candidate roots before it creates a
# missing tree or hands off mac.yaml. A failed post-registration tree setup
# leaves the candidate available for safe repair rather than stranding a newly
# published marker or restarting V1 on a newer schema.
candidate_activation_started=1
if ! "$staging_directory/runner-local" validate-config --check-retained-mailboxes --activate-mailbox-set --config "$selected_config"; then
	printf '%s\n' 'Candidate mailbox activation could not be recorded; leaving the candidate state in place for safe repair.' >&2
	exit 1
fi
if [ -n "$config_stage" ]; then
	mv -f "$config_stage" "$config_file"
	config_stage=''
fi
candidate_config_handed_off=1
if [ "$candidate_binaries_installed" -eq 0 ]; then
	for name in runner runner-local runner-locald; do
		mv -f "$staging_directory/$name" "$service_root/bin/$name"
	done
fi

for name in com.remote-session-runner.locald com.remote-session-runner.local; do
	plist="$repo_root/deploy/macos/launchagents/$name.plist"
	installed="$launch_agents/$name.plist"
	if service_loaded "$name"; then
		printf 'LaunchAgent became loaded during configuration replacement: %s\n' "$name" >&2
		exit 1
	fi
	install -m 600 "$plist" "$installed"
	candidate_startup_attempted=1
	candidate_services_quiesced=0
	launchctl bootstrap "gui/$uid" "$installed"
	launchctl kickstart -k "gui/$uid/$name"
done

provenance_failed=0
if ! wait_for_build_revision "$service_root/run/locald.sock" 'runner-locald'; then
	provenance_failed=1
fi
if [ "$provenance_failed" -eq 0 ] && ! wait_for_build_revision "$service_root/run/local-api.sock" 'runner-local'; then
	provenance_failed=1
fi
if [ "$provenance_failed" -ne 0 ]; then
	printf '%s\n' 'Build-revision verification failed; quiescing candidate LaunchAgents without rolling back the active candidate configuration.' >&2
	if quiesce_candidate_agents_after_start_failure; then
		candidate_services_quiesced=1
	else
		printf '%s\n' 'Could not verify candidate LaunchAgent quiescence after build-revision verification failed.' >&2
	fi
	exit 1
fi
printf 'Installed and started Mac LaunchAgents at source revision %s\n' "$source_revision"
