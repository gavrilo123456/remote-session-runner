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

usage() {
	printf '%s\n' "usage: $0 [--config /Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml]" >&2
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
	directory=$1
	if [ -L "$directory" ]; then
		printf 'refusing symlinked service directory: %s\n' "$directory" >&2
		exit 1
	fi
	if [ ! -e "$directory" ]; then
		mkdir "$directory"
	fi
	if [ -L "$directory" ] || [ ! -d "$directory" ] || [ "$(stat -f '%u' "$directory")" != "$uid" ]; then
		printf 'service directory must be a real directory owned by uid %s: %s\n' "$uid" "$directory" >&2
		exit 1
	fi
	chmod 700 "$directory"
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
	if [ "$locald_was_loaded" -eq 1 ] && ! service_loaded com.remote-session-runner.locald; then
		if ! launchctl bootstrap "gui/$uid" "$launch_agents/com.remote-session-runner.locald.plist" || ! launchctl kickstart -k "gui/$uid/com.remote-session-runner.locald"; then
			restore_failed=1
		fi
	fi
	if [ "$local_was_loaded" -eq 1 ] && ! service_loaded com.remote-session-runner.local; then
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
		if ! rm -rf "$staging_directory"; then
			printf 'could not remove installer staging directory: %s\n' "$staging_directory" >&2
		fi
	fi
}
on_exit() {
	status=$?
	if [ "$status" -ne 0 ] && [ "$restore_prior_agents_on_failure" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ]; then
		printf '%s\n' 'Install stopped before candidate activation; restoring prior LaunchAgents and active mac.yaml.' >&2
		if ! restore_prior_agents; then
			printf '%s\n' 'Restore failed before candidate activation.' >&2
		fi
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
# Until the candidate begins replacing active artifacts, any failure restores
# exactly the pre-install LaunchAgent state. Set this before stopping either
# job so a failure while quiescing the second job also restarts the first.
restore_prior_agents_on_failure=1
stop_agent_for_config_change com.remote-session-runner.local "$launch_agents/com.remote-session-runner.local.plist" "$service_root/run/local-api.sock"
stop_agent_for_config_change com.remote-session-runner.locald "$launch_agents/com.remote-session-runner.locald.plist" "$service_root/run/locald.sock"
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
for name in runner runner-local runner-locald; do
	mv -f "$staging_directory/$name" "$service_root/bin/$name"
done

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
