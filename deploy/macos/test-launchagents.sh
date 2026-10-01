#!/bin/sh
set -eu

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != tomasz.walczuk ]; then
	printf '%s\n' 'P125 host gate must run on the selected Mac account tomasz.walczuk' >&2
	exit 2
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
launch_agents="$HOME/Library/LaunchAgents"
uid=$(id -u)
local_plist="$launch_agents/com.remote-session-runner.local.plist"
locald_plist="$launch_agents/com.remote-session-runner.locald.plist"
api_socket="$service_root/run/local-api.sock"
locald_socket="$service_root/run/locald.sock"
success=0

wait_for_stopped() {
	path=$1
	i=0
	while [ "$i" -lt 50 ]; do
		if [ ! -e "$path" ] && [ ! -L "$path" ]; then
			return 0
		fi
		sleep 0.2
		i=$((i + 1))
	done
	printf 'socket remained after launchd stop: %s\n' "$path" >&2
	return 1
}

for path in "$service_root/bin" "$service_root/config" "$service_root/logs" "$service_root/run" "$service_root/state" "$service_root/mailbox" "$service_root/workspaces" "$service_root/tmp" "$service_root/backups" "$local_plist" "$locald_plist"; do
	if [ -e "$path" ] || [ -L "$path" ]; then
		printf 'P125 host gate requires an unused selected service path: %s\n' "$path" >&2
		exit 1
	fi
done

mkdir -m 700 "$service_root/config"
install -m 600 "$repo_root/deploy/macos/mac.yaml.example" "$service_root/config/mac.yaml"

cleanup_fixture() {
	launchctl bootout "gui/$uid" "$local_plist" >/dev/null 2>&1 || true
	launchctl bootout "gui/$uid" "$locald_plist" >/dev/null 2>&1 || true
	wait_for_stopped "$api_socket"
	wait_for_stopped "$locald_socket"
	rm -f "$local_plist" "$locald_plist"
	for path in bin config logs run state mailbox workspaces tmp backups; do
		rm -rf "$service_root/$path"
	done
	for path in "$local_plist" "$locald_plist" "$service_root/bin" "$service_root/config" "$service_root/logs" "$service_root/run" "$service_root/state" "$service_root/mailbox" "$service_root/workspaces" "$service_root/tmp" "$service_root/backups"; do
		if [ -e "$path" ] || [ -L "$path" ]; then
			printf 'P125 cleanup left a test path behind: %s\n' "$path" >&2
			return 1
		fi
	done
}

cleanup_on_failure() {
	status=$?
	trap - EXIT
	if [ "$success" -ne 1 ]; then
		cleanup_fixture || printf '%s\n' 'P125 failure cleanup could not remove every test path' >&2
	fi
	exit "$status"
}
trap cleanup_on_failure EXIT

"$repo_root/deploy/macos/install-launchagents.sh"

check_mode() {
	path=$1
	want=$2
	actual=$(stat -f '%Lp' "$path")
	if [ "$actual" != "$want" ]; then
		printf 'mode for %s is %s; expected %s\n' "$path" "$actual" "$want" >&2
		exit 1
	fi
}

wait_for_socket() {
	path=$1
	i=0
	while [ "$i" -lt 50 ]; do
		if [ -S "$path" ]; then
			return 0
		fi
		sleep 0.2
		i=$((i + 1))
	done
	printf 'timed out waiting for service socket: %s\n' "$path" >&2
	return 1
}

wait_for_socket "$api_socket"
wait_for_socket "$locald_socket"
check_mode "$service_root" 700
check_mode "$service_root/config" 700
check_mode "$service_root/config/mac.yaml" 600
check_mode "$service_root/run" 700
check_mode "$api_socket" 600
check_mode "$locald_socket" 600
for path in "$service_root/mailbox" "$service_root/mailbox/inbox" "$service_root/mailbox/outbox" "$service_root/mailbox/events" "$service_root/mailbox/acks" "$service_root/mailbox/diagnostics"; do
	check_mode "$path" 700
done

api_result=$(/usr/bin/curl --silent --show-error --unix-socket "$api_socket" http://localhost/v1/sessions/sess-p125-missing 2>&1 || true)
case "$api_result" in
	*session_not_found*) ;;
	*) printf 'Unix API smoke returned an unexpected response: %s\n' "$api_result" >&2; exit 1 ;;
esac

inbox="$service_root/mailbox/inbox"
suffix="$(date +%s)-$$"
outbox_root="$service_root/mailbox/outbox"
write_request() {
	request_id=$1
	payload=$2
	printf '%s\n' "$payload" > "$inbox/$request_id.json"
	chmod 600 "$inbox/$request_id.json"
	: > "$inbox/$request_id.ready"
	chmod 600 "$inbox/$request_id.ready"
}
await_terminal_response() {
	request_id=$1
	response="$outbox_root/$request_id.json"
	i=0
	while [ "$i" -lt 250 ]; do
		if [ -f "$response" ]; then
			state=$(/usr/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("request_state", ""))' "$response")
			case "$state" in
				complete|rejected|indeterminate) return 0 ;;
			esac
		fi
		sleep 0.2
		i=$((i + 1))
	done
	printf 'mailbox response did not become terminal: %s\n' "$request_id" >&2
	return 1
}
response_field() {
	/usr/bin/python3 -c 'import json,sys; value=json.load(open(sys.argv[1])).get(sys.argv[2], ""); print(value if value is not None else "")' "$1" "$2"
}

umask 077
draft_id="req-p125-draft-$suffix"
printf '{"request_id":"%s","operation":"get_session","session_id":"sess-p125-missing"}\n' "$draft_id" > "$inbox/$draft_id.json"
chmod 600 "$inbox/$draft_id.json"
sleep 1
if [ -e "$outbox_root/$draft_id.json" ]; then
	printf '%s\n' 'unmarked mailbox draft was imported' >&2
	exit 1
fi
rm -f "$inbox/$draft_id.json"

create_id="req-p125-create-$suffix"
create_payload=$(/usr/bin/python3 -c 'import json,sys; print(json.dumps({"request_id":sys.argv[1],"idempotency_key":"p125-create-"+sys.argv[2],"operation":"create_session","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}))' "$create_id" "$suffix")
write_request "$create_id" "$create_payload"
await_terminal_response "$create_id"
create_response="$outbox_root/$create_id.json"
check_mode "$create_response" 600
if [ "$(response_field "$create_response" request_state)" != complete ] || [ "$(response_field "$create_response" session_state)" != ready ]; then
	printf 'mailbox create did not reach ready: %s\n' "$(/usr/bin/python3 -m json.tool "$create_response")" >&2
	exit 1
fi
session_id=$(response_field "$create_response" session_id)
if [ -z "$session_id" ]; then
	printf '%s\n' 'mailbox create response omitted session_id' >&2
	exit 1
fi

submit_id="req-p125-submit-$suffix"
submit_payload=$(/usr/bin/python3 -c 'import json,sys; print(json.dumps({"request_id":sys.argv[1],"idempotency_key":"p125-submit-"+sys.argv[2],"operation":"submit_command","session_id":sys.argv[3],"script":"printf '\''P125_MAILBOX_OK\\n'\''"}))' "$submit_id" "$suffix" "$session_id")
write_request "$submit_id" "$submit_payload"
await_terminal_response "$submit_id"
submit_response="$outbox_root/$submit_id.json"
check_mode "$submit_response" 600
if [ "$(response_field "$submit_response" request_state)" != complete ] || [ "$(response_field "$submit_response" command_state)" != succeeded ] || [ "$(response_field "$submit_response" output_complete)" != True ]; then
	printf 'mailbox command did not return complete success: %s\n' "$(/usr/bin/python3 -m json.tool "$submit_response")" >&2
	exit 1
fi
events_file=$(response_field "$submit_response" events_file)
if [ -z "$events_file" ] || [ ! -f "$service_root/mailbox/$events_file" ]; then
	printf '%s\n' 'mailbox command response omitted its event file' >&2
	exit 1
fi
check_mode "$service_root/mailbox/$events_file" 600
if ! /usr/bin/grep -Fq P125_MAILBOX_OK "$service_root/mailbox/$events_file"; then
	printf '%s\n' 'mailbox event file omitted the command output marker' >&2
	exit 1
fi

close_id="req-p125-close-$suffix"
close_payload=$(/usr/bin/python3 -c 'import json,sys; print(json.dumps({"request_id":sys.argv[1],"idempotency_key":"p125-close-"+sys.argv[2],"operation":"close_session","session_id":sys.argv[3]}))' "$close_id" "$suffix" "$session_id")
write_request "$close_id" "$close_payload"
await_terminal_response "$close_id"
close_response="$outbox_root/$close_id.json"
if [ "$(response_field "$close_response" request_state)" != complete ] || [ "$(response_field "$close_response" session_state)" != closed ]; then
	printf 'mailbox close did not confirm teardown: %s\n' "$(/usr/bin/python3 -m json.tool "$close_response")" >&2
	exit 1
fi

launchctl bootout "gui/$uid" "$local_plist"
launchctl bootout "gui/$uid" "$locald_plist"
wait_for_stopped "$api_socket"
wait_for_stopped "$locald_socket"
launchctl bootstrap "gui/$uid" "$locald_plist"
launchctl bootstrap "gui/$uid" "$local_plist"
wait_for_socket "$locald_socket"
wait_for_socket "$api_socket"
check_mode "$api_socket" 600
check_mode "$locald_socket" 600
api_result=$(/usr/bin/curl --silent --show-error --unix-socket "$api_socket" http://localhost/v1/sessions/sess-p125-missing 2>&1 || true)
case "$api_result" in
	*session_not_found*) ;;
	*) printf 'Unix API did not recover after launchd restart: %s\n' "$api_result" >&2; exit 1 ;;
esac

cleanup_fixture
success=1
printf 'P125 PASS: launchd start/stop/restart, local API socket, local execution as %s, marker-last mailbox create/exec/events/close, and owner-only modes\n' "$(id -un)"
