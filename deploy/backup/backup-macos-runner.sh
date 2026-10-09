#!/bin/sh
set -eu
umask 077

# Create a stopped-service continuity archive for the selected Mac Runner
# installation. The archive contains private material; write it only to an
# encrypted destination outside the source checkout.

account='tomasz.walczuk'
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
config_file="$service_root/config/mac.yaml"
launch_agents="$HOME/Library/LaunchAgents"
uid=$(id -u)
router_label='com.remote-session-runner.local'
executor_label='com.remote-session-runner.locald'
router_socket="$service_root/run/local-api.sock"
executor_socket="$service_root/run/locald.sock"

destination=''
bundle_name=''
ssh_mode='selected'
work=''
preflight_mailboxes=''
success=0
router_stopped=0
executor_stopped=0
router_bootout=0
executor_bootout=0

usage() {
	cat >&2 <<'EOF'
usage: backup-macos-runner.sh --destination /absolute/encrypted/directory [--name backup-name] [--without-ssh | --include-ssh-directory]

Creates one .tar.gz continuity archive and an adjacent .sha256 file. The
script verifies that both Mac Runner services are quiescent, stops them in
Router-then-executor order, snapshots the durable state, and leaves them
stopped after success. It restores previously running services on a safe
failure boundary.
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--destination)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			destination=$2
			shift 2
			;;
		--name)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			bundle_name=$2
			shift 2
			;;
		--without-ssh)
			ssh_mode='none'
			shift
			;;
		--include-ssh-directory)
			ssh_mode='directory'
			shift
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			usage
			exit 2
			;;
	esac
done

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != "$account" ]; then
	printf 'backup-macos-runner.sh must run on the selected Mac account %s\n' "$account" >&2
	exit 2
fi
if [ -z "$destination" ]; then
	usage
	exit 2
fi
case "$destination" in
	/*) ;;
	*) printf '%s\n' 'backup destination must be an absolute directory' >&2; exit 2 ;;
esac
if [ -L "$destination" ] || [ ! -d "$destination" ] || [ "$(stat -f '%u' "$destination")" != "$uid" ]; then
	printf 'backup destination must be a real directory owned by the selected account: %s\n' "$destination" >&2
	exit 1
fi
destination=$(CDPATH= cd -- "$destination" && pwd -P) || {
	printf 'could not resolve backup destination: %s\n' "$destination" >&2
	exit 1
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
case "$destination" in
	"$repo_root"|"$repo_root"/*|"$service_root"|"$service_root"/*)
		printf '%s\n' 'backup destination must be outside the source checkout and Runner service root' >&2
		exit 1
		;;
esac

if [ -z "$bundle_name" ]; then
	bundle_name="remote-session-runner-macos-$(date -u '+%Y%m%dT%H%M%SZ')"
fi
case "$bundle_name" in
	''|*[!A-Za-z0-9._-]*)
		printf '%s\n' 'backup name may contain only letters, digits, dot, underscore, and hyphen' >&2
		exit 2
		;;
esac
archive="$destination/$bundle_name.tar.gz"
checksum="$archive.sha256"
if [ -e "$archive" ] || [ -L "$archive" ] || [ -e "$checksum" ] || [ -L "$checksum" ]; then
	printf 'refusing to overwrite an existing backup artifact: %s\n' "$archive" >&2
	exit 1
fi

fail() {
	printf '%s\n' "$*" >&2
	exit 1
}

require_real_directory() {
	directory=$1
	label=$2
	if [ -L "$directory" ] || [ ! -d "$directory" ] || [ "$(stat -f '%u' "$directory")" != "$uid" ]; then
		fail "$label must be a real directory owned by the selected account: $directory"
	fi
}

require_regular_file() {
	path=$1
	label=$2
	if [ -L "$path" ] || [ ! -f "$path" ] || [ "$(stat -f '%u' "$path")" != "$uid" ]; then
		fail "$label must be a regular file owned by the selected account: $path"
	fi
}

reject_unsafe_tree() {
	tree=$1
	label=$2
	if [ -n "$(find "$tree" -type l -print -quit)" ]; then
		fail "$label contains a symlink and cannot be archived safely: $tree"
	fi
	if [ -n "$(find "$tree" ! -type d ! -type f -print -quit)" ]; then
		fail "$label contains a non-regular entry and cannot be archived safely: $tree"
	fi
}

reject_unsafe_ssh_tree() {
	tree=$1
	label=$2
	if [ -n "$(find "$tree" -type l -print -quit)" ]; then
		fail "$label contains a symlink and cannot be archived safely: $tree"
	fi
	if [ -n "$(find "$tree" ! -type d ! -type f ! -type s -print -quit)" ]; then
		fail "$label contains a non-regular, non-socket entry and cannot be archived safely: $tree"
	fi
}

copy_tree() {
	source=$1
	target=$2
	label=$3
	require_real_directory "$source" "$label"
	reject_unsafe_tree "$source" "$label"
	if [ -e "$target" ] || [ -L "$target" ]; then
		fail "backup staging target already exists: $target"
	fi
	mkdir -p "$(dirname -- "$target")"
	/usr/bin/ditto "$source" "$target"
}

copy_file() {
	source=$1
	target=$2
	label=$3
	require_regular_file "$source" "$label"
	if [ -e "$target" ] || [ -L "$target" ]; then
		fail "backup staging target already exists: $target"
	fi
	mkdir -p "$(dirname -- "$target")"
	/usr/bin/ditto "$source" "$target"
}

copy_ssh_tree() {
	source=$1
	target=$2
	label=$3
	require_real_directory "$source" "$label"
	reject_unsafe_ssh_tree "$source" "$label"
	if [ -e "$target" ] || [ -L "$target" ]; then
		fail "backup staging target already exists: $target"
	fi
	mkdir -p "$(dirname -- "$target")"
	/usr/bin/python3 - "$source" "$target" <<'PY'
import os
import shutil
import stat
import sys

source, target = sys.argv[1:]
for current, directories, files in os.walk(source, topdown=True, followlinks=False):
    current_stat = os.lstat(current)
    if not stat.S_ISDIR(current_stat.st_mode):
        raise SystemExit("SSH source directory changed while copying")
    relative = os.path.relpath(current, source)
    destination = target if relative == "." else os.path.join(target, relative)
    os.makedirs(destination, mode=0o700, exist_ok=True)
    os.chmod(destination, stat.S_IMODE(current_stat.st_mode))
    for name in directories:
        item = os.path.join(current, name)
        item_stat = os.lstat(item)
        if not stat.S_ISDIR(item_stat.st_mode):
            raise SystemExit("SSH source contains an unsafe directory entry")
    for name in files:
        item = os.path.join(current, name)
        item_stat = os.lstat(item)
        if stat.S_ISSOCK(item_stat.st_mode):
            continue
        if not stat.S_ISREG(item_stat.st_mode):
            raise SystemExit("SSH source contains an unsafe file entry")
        shutil.copy2(item, os.path.join(destination, name), follow_symlinks=False)
PY
	reject_unsafe_tree "$target" "staged $label"
}

validate_ssh_include_scope() {
	ssh_root=$1
	/usr/bin/python3 - "$ssh_root" <<'PY'
import glob
import os
import shlex
import stat
import sys

ssh_root = os.path.realpath(sys.argv[1])
seen = set()

def walk_config(config_path):
    real_config = os.path.realpath(config_path)
    if real_config in seen:
        return
    seen.add(real_config)
    with open(config_path, encoding="utf-8") as source:
        for raw in source:
            stripped = raw.strip()
            if not stripped or stripped.startswith("#"):
                continue
            try:
                words = shlex.split(stripped, comments=True)
            except ValueError as error:
                raise SystemExit("could not parse SSH Include directive") from error
            if not words or words[0].lower() != "include":
                continue
            for pattern in words[1:]:
                expanded = os.path.expanduser(pattern)
                if not os.path.isabs(expanded):
                    expanded = os.path.join(os.path.dirname(config_path), expanded)
                for candidate in glob.glob(expanded):
                    candidate_stat = os.lstat(candidate)
                    if not stat.S_ISREG(candidate_stat.st_mode):
                        raise SystemExit("active SSH Include is not a regular file")
                    resolved = os.path.realpath(candidate)
                    if not (resolved == ssh_root or resolved.startswith(ssh_root + os.sep)):
                        raise SystemExit("active SSH Include resolves outside ~/.ssh; move or separately preserve it before backup")
                    walk_config(candidate)

walk_config(os.path.join(ssh_root, "config"))
PY
}

service_loaded() {
	launchctl print "gui/$uid/$1" >/dev/null 2>&1
}

capture_agent_pid() {
	label=$1
	snapshot=$(launchctl print "gui/$uid/$label") || return 1
	pid=$(printf '%s\n' "$snapshot" | /usr/bin/awk '
		/^[[:space:]]*pid = [0-9][0-9]*[[:space:]]*$/ { count++; value=$3 }
		END { if (count != 1 || value !~ /^[1-9][0-9]*$/) exit 1; print value }
	') || return 1
	printf '%s\n' "$pid"
}

wait_for_absent_path() {
	path=$1
	attempt=0
	while [ "$attempt" -lt 225 ]; do
		if [ ! -e "$path" ] && [ ! -L "$path" ]; then
			return 0
		fi
		sleep 0.2
		attempt=$((attempt + 1))
	done
	fail "socket remained after LaunchAgent stop: $path"
}

wait_for_agent_exit() {
	pid=$1
	label=$2
	attempt=0
	while [ "$attempt" -lt 225 ]; do
		state=$(/bin/ps -p "$pid" -o state= 2>/dev/null | /usr/bin/tr -d '[:space:]' || true)
		if [ -z "$state" ]; then
			return 0
		fi
		sleep 0.2
		attempt=$((attempt + 1))
	done
	fail "old LaunchAgent process remains after stop; refusing an offline backup: $label pid=$pid state=$state"
}

stop_agent() {
	label=$1
	plist=$2
	socket=$3
	pid=$4
	if [ -L "$plist" ] || [ ! -f "$plist" ]; then
		fail "installed LaunchAgent plist is unavailable: $plist"
	fi
	launchctl bootout "gui/$uid" "$plist"
	case "$label" in
		"$router_label") router_bootout=1 ;;
		"$executor_label") executor_bootout=1 ;;
		*) fail "unrecognized Runner LaunchAgent stop target: $label" ;;
	esac
	if service_loaded "$label"; then
		fail "LaunchAgent remained loaded after stop: $label"
	fi
	wait_for_absent_path "$socket"
	wait_for_agent_exit "$pid" "$label"
}

restart_agent() {
	label=$1
	plist=$2
	if ! launchctl bootstrap "gui/$uid" "$plist" || ! launchctl kickstart -k "gui/$uid/$label"; then
		printf 'could not restore prior LaunchAgent after backup failure: %s\n' "$label" >&2
		return 1
	fi
	return 0
}

restore_on_failure() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$success" -ne 1 ]; then
		if [ "$executor_stopped" -eq 1 ]; then
			restart_agent "$executor_label" "$launch_agents/$executor_label.plist" || status=1
		fi
		if [ "$router_stopped" -eq 1 ]; then
			restart_agent "$router_label" "$launch_agents/$router_label.plist" || status=1
		elif [ "$router_bootout" -eq 1 ]; then
			printf '%s\n' 'Router bootout succeeded but its old process boundary was not proven; it was left stopped to avoid a second writer. Inspect the old process and restore it manually only after it exits.' >&2
		fi
		if [ "$executor_bootout" -eq 1 ] && [ "$executor_stopped" -ne 1 ]; then
			printf '%s\n' 'Executor bootout succeeded but its old process boundary was not proven; it was left stopped to avoid a second writer. Inspect the old process and restore it manually only after it exits.' >&2
		fi
	fi
	if [ -n "$work" ] && [ -d "$work" ]; then
		rm -rf "$work"
	fi
	if [ -n "$preflight_mailboxes" ] && [ -f "$preflight_mailboxes" ]; then
		rm -f "$preflight_mailboxes"
	fi
	exit "$status"
}
trap restore_on_failure EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

validate_source_checkout() {
	if [ ! -d "$repo_root/.git" ] || ! git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		fail "expected checked-out repository is unavailable: $repo_root"
	fi
	if [ "$(git -C "$repo_root" branch --show-current)" != dev ]; then
		fail 'repository must be on dev before creating a continuity backup'
	fi
	if [ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]; then
		fail 'repository must be clean before creating a continuity backup'
	fi
	source_revision=$(git -C "$repo_root" rev-parse HEAD) || fail 'could not resolve source revision'
	source_origin_revision=$(git -C "$repo_root" rev-parse origin/dev) || fail 'could not resolve origin/dev'
	if [ "$source_revision" != "$source_origin_revision" ]; then
		fail 'repository HEAD must match origin/dev before creating a continuity backup'
	fi
	source_origin=$(git -C "$repo_root" config --get remote.origin.url || true)
	if [ -z "$source_origin" ]; then
		fail 'repository origin must be configured before creating a continuity backup'
	fi
	case "$source_revision" in
		????????????????????????????????????????) ;;
		*) fail 'source revision has an unexpected format' ;;
	esac
}

check_router_quiescent() {
	health=$(/usr/bin/curl --silent --show-error --fail --unix-socket "$router_socket" http://runner/health/ready) || fail 'Mac Router readiness is unavailable; refusing backup'
	if ! printf '%s' "$health" | /usr/bin/python3 -c '
import json
import sys
try:
    report = json.load(sys.stdin)
    metrics = report["metrics"]
    required = ("active_session_slots", "active_command_slots", "queued_commands", "queued_intents")
    if report.get("readiness") != "ready" or any(not isinstance(metrics.get(key), int) or metrics[key] != 0 for key in required):
        raise ValueError("not quiescent")
except Exception:
    sys.exit(1)
'; then
		fail 'Mac Router reports active or queued work; wait for it to finish before backup'
	fi
}

discover_mailboxes() {
	/usr/bin/python3 - "$config_file" "$service_root" <<'PY'
import json
import os
import re
import sys

config_path, service_root = sys.argv[1:]
service_root = os.path.realpath(service_root)
mailbox_root = os.path.join(service_root, "mailbox")
mailboxes_root = os.path.join(service_root, "mailboxes")
identifier_re = re.compile(r"^  ([A-Za-z0-9][A-Za-z0-9_-]*):\s*(?:#.*)?$")
root_re = re.compile(r"^    root:\s*(.*?)\s*$")
roots = []
current = None
in_mailboxes = False

def scalar(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] == '"':
        return json.loads(value)
    if len(value) >= 2 and value[0] == value[-1] == "'":
        return value[1:-1].replace("''", "'")
    return value.split(" #", 1)[0].rstrip()

with open(config_path, encoding="utf-8") as source:
    for raw in source:
        line = raw.rstrip("\n")
        if not in_mailboxes:
            if line == "mailboxes:":
                in_mailboxes = True
            continue
        if line and not line[0].isspace():
            break
        match = identifier_re.match(line)
        if match:
            current = match.group(1)
            roots.append([current, None])
            continue
        match = root_re.match(line)
        if match and current is not None:
            if roots[-1][1] is not None:
                raise SystemExit("duplicate mailbox root for " + current)
            roots[-1][1] = scalar(match.group(1))

if not roots or any(root is None for _, root in roots):
    raise SystemExit("could not read every mailbox root from active mac.yaml")
seen = set()
for identifier, root in roots:
    if not root or not os.path.isabs(root) or os.path.normpath(root) != root or root in seen or "\t" in root or "\n" in root:
        raise SystemExit("unsafe mailbox root in active mac.yaml")
    root = os.path.realpath(root)
    if root in seen:
        raise SystemExit("duplicate physical mailbox root in active mac.yaml")
    seen.add(root)
    if root == service_root:
        raise SystemExit("mailbox root may not equal the Runner service root")
    if root == mailbox_root or root.startswith(mailbox_root + os.sep) or root == mailboxes_root or root.startswith(mailboxes_root + os.sep):
        scope = "internal"
    elif root.startswith(service_root + os.sep):
        scope = "service-extra"
    else:
        scope = "external"
    print(identifier + "\t" + root + "\t" + scope)
PY
}

snapshot_tree() {
	tree=$1
	output=$2
	find "$tree" -exec stat -f '%N\t%z\t%m\t%Lp' {} \; | LC_ALL=C sort > "$output"
}

copy_selected_ssh() {
	case "$ssh_mode" in
		none)
			printf '%s\n' 'mode=none' > "$stage/metadata/ssh.txt"
			return 0
			;;
		directory)
			ssh_root="$HOME/.ssh"
			validate_ssh_include_scope "$ssh_root"
			copy_ssh_tree "$ssh_root" "$stage/payload/ssh-directory" 'SSH directory'
			printf '%s\n' 'mode=directory' > "$stage/metadata/ssh.txt"
			return 0
			;;
	esac

	ssh_root="$HOME/.ssh"
	require_real_directory "$ssh_root" 'SSH directory'
	if /usr/bin/grep -Eiq '^[[:space:]]*Include[[:space:]]' "$ssh_root/config"; then
		fail 'SSH config uses Include; rerun with --include-ssh-directory so included files are preserved'
	fi
	mkdir -p "$stage/payload/ssh"
	: > "$stage/metadata/ssh.tsv"
	for name in config known_hosts gavrilo123456-github dev.slidestud.io remote-session-runner; do
		source="$ssh_root/$name"
		if [ "$name" = remote-session-runner ] && [ ! -e "$source" ] && [ ! -L "$source" ]; then
			continue
		fi
		copy_file "$source" "$stage/payload/ssh/$name" "SSH material $name"
		printf '%s\t%s\n' "$source" "payload/ssh/$name" >> "$stage/metadata/ssh.tsv"
	done
	printf '%s\n' 'mode=selected' > "$stage/metadata/ssh.txt"
}

preflight_ssh_scope() {
	case "$ssh_mode" in
		none)
			return 0
			;;
		directory)
			require_real_directory "$HOME/.ssh" 'SSH directory'
			reject_unsafe_ssh_tree "$HOME/.ssh" 'SSH directory'
			validate_ssh_include_scope "$HOME/.ssh"
			return 0
			;;
	esac
	require_real_directory "$HOME/.ssh" 'SSH directory'
	require_regular_file "$HOME/.ssh/config" 'SSH material config'
	if /usr/bin/grep -Eiq '^[[:space:]]*Include[[:space:]]' "$HOME/.ssh/config"; then
		fail 'SSH config uses Include; rerun with --include-ssh-directory so included files are preserved'
	fi
	for name in known_hosts gavrilo123456-github dev.slidestud.io; do
		require_regular_file "$HOME/.ssh/$name" "SSH material $name"
	done
	if [ -e "$HOME/.ssh/remote-session-runner" ] || [ -L "$HOME/.ssh/remote-session-runner" ]; then
		require_regular_file "$HOME/.ssh/remote-session-runner" 'SSH material remote-session-runner'
	fi
}

preflight_mailbox_scope() {
	preflight_mailboxes=$(mktemp "${TMPDIR:-/private/tmp}/remote-session-runner-backup-mailboxes.XXXXXX") || fail 'could not create private mailbox preflight file'
	discover_mailboxes > "$preflight_mailboxes"
	while IFS="$(printf '\t')" read -r inbox_id mailbox_root scope; do
		case "$scope" in
			internal|service-extra|external)
				require_real_directory "$mailbox_root" "configured mailbox $inbox_id"
				reject_unsafe_tree "$mailbox_root" "configured mailbox $inbox_id"
				;;
			*) fail "unrecognized mailbox scope in active config: $scope" ;;
		esac
	done < "$preflight_mailboxes"
}

reject_destination_within_mailboxes() {
	while IFS="$(printf '\t')" read -r inbox_id mailbox_root scope; do
		case "$destination" in
			"$mailbox_root"|"$mailbox_root"/*)
				fail "backup destination must not be inside configured mailbox $inbox_id: $mailbox_root"
				;;
		esac
	done < "$preflight_mailboxes"
}

validate_source_checkout
require_real_directory "$service_root" 'Runner service root'
require_regular_file "$config_file" 'active Mac configuration'
if [ "$(stat -f '%Lp' "$config_file")" != 600 ]; then
	fail "active Mac configuration must be mode 0600: $config_file"
fi
for path in config secrets state mailbox mailboxes backups toolchains; do
	require_real_directory "$service_root/$path" "Runner service-root directory"
	reject_unsafe_tree "$service_root/$path" "Runner service-root directory"
done
require_regular_file "$service_root/toolchains/go1.27.1/bin/go" 'selected Go toolchain'
if [ ! -x "$service_root/toolchains/go1.27.1/bin/go" ]; then
	fail "selected Go toolchain is not executable: $service_root/toolchains/go1.27.1/bin/go"
fi
require_regular_file "$service_root/bin/runner-local" 'installed Router binary'
require_regular_file "$service_root/bin/runner-locald" 'installed executor binary'
"$service_root/bin/runner-local" validate-config --check-mailbox-directories --config "$config_file"
preflight_mailbox_scope
reject_destination_within_mailboxes
preflight_ssh_scope

if ! service_loaded "$router_label" || ! service_loaded "$executor_label"; then
	fail 'both Mac Runner LaunchAgents must be loaded before this managed backup can establish its stop boundary'
fi
check_router_quiescent
router_pid=$(capture_agent_pid "$router_label") || fail "could not capture Router process boundary: $router_label"
executor_pid=$(capture_agent_pid "$executor_label") || fail "could not capture executor process boundary: $executor_label"

# Stop ingress before checking the local executor's durable state. This is the
# same ordering used by the managed installer when it replaces the executor.
stop_agent "$router_label" "$launch_agents/$router_label.plist" "$router_socket" "$router_pid"
router_stopped=1
"$service_root/bin/runner-locald" preflight-restart --config "$config_file"
stop_agent "$executor_label" "$launch_agents/$executor_label.plist" "$executor_socket" "$executor_pid"
executor_stopped=1

work=$(mktemp -d "$destination/.remote-session-runner-backup.XXXXXX") || fail 'could not create private backup staging directory'
stage="$work/stage"
mkdir -m 700 "$stage" "$stage/payload" "$stage/payload/service-root" "$stage/payload/service-root-extra" "$stage/payload/external-mailboxes" "$stage/metadata" "$stage/source"

for component in config secrets state mailbox mailboxes backups toolchains; do
	copy_tree "$service_root/$component" "$stage/payload/service-root/$component" "Runner $component"
	printf '%s\t%s\n' "$service_root/$component" "payload/service-root/$component" >> "$stage/metadata/service-components.tsv"
done

cp "$preflight_mailboxes" "$stage/metadata/mailboxes.tsv"
: > "$stage/metadata/external-mailboxes.tsv"
while IFS="$(printf '\t')" read -r inbox_id mailbox_root scope; do
	case "$scope" in
		internal)
			printf '%s\t%s\t%s\n' "$inbox_id" "$mailbox_root" 'included-in-service-root' >> "$stage/metadata/external-mailboxes.tsv"
			;;
		service-extra)
			before="$work/$inbox_id.before"
			after="$work/$inbox_id.after"
			snapshot_tree "$mailbox_root" "$before"
			copy_tree "$mailbox_root" "$stage/payload/service-root-extra/$inbox_id" "service-root mailbox $inbox_id"
			snapshot_tree "$mailbox_root" "$after"
			if ! cmp -s "$before" "$after"; then
				fail "service-root mailbox changed during backup; pause its publishers and retry: $mailbox_root"
			fi
			printf '%s\t%s\t%s\n' "$inbox_id" "$mailbox_root" "payload/service-root-extra/$inbox_id" >> "$stage/metadata/external-mailboxes.tsv"
			;;
		external)
			before="$work/$inbox_id.before"
			after="$work/$inbox_id.after"
			snapshot_tree "$mailbox_root" "$before"
			copy_tree "$mailbox_root" "$stage/payload/external-mailboxes/$inbox_id" "external mailbox $inbox_id"
			snapshot_tree "$mailbox_root" "$after"
			if ! cmp -s "$before" "$after"; then
				fail "external mailbox changed during backup; pause its publishers and retry: $mailbox_root"
			fi
			printf '%s\t%s\t%s\n' "$inbox_id" "$mailbox_root" "payload/external-mailboxes/$inbox_id" >> "$stage/metadata/external-mailboxes.tsv"
			;;
		*) fail "unrecognized mailbox scope in active config: $scope" ;;
	esac
done < "$stage/metadata/mailboxes.tsv"

copy_selected_ssh
git -C "$repo_root" bundle create "$stage/source/remote-session-runner.bundle" --all
git -C "$repo_root" bundle verify "$stage/source/remote-session-runner.bundle" >/dev/null

created_at=$(date -u '+%Y-%m-%dT%H%M%SZ')
{
	printf '%s\n' 'format=remote-session-runner-macos-continuity-v1'
	printf 'created_at=%s\n' "$created_at"
	printf 'account=%s\n' "$account"
	printf 'service_root=%s\n' "$service_root"
	printf 'source_revision=%s\n' "$source_revision"
	printf 'source_origin=%s\n' "$source_origin"
	printf '%s\n' 'snapshot_boundary=router-and-executor-stopped-after-quiescence-preflight'
	printf 'ssh_mode=%s\n' "$ssh_mode"
} > "$stage/metadata/manifest.txt"

(
	cd "$stage"
	find payload metadata source -type f ! -path 'metadata/SHA256SUMS' -print | LC_ALL=C sort |
	while IFS= read -r path; do
		/usr/bin/shasum -a 256 "$path"
	done > metadata/SHA256SUMS
)

archive_stage="$work/$bundle_name.tar.gz"
checksum_stage="$work/$bundle_name.tar.gz.sha256"
(
	cd "$stage"
	/usr/bin/tar -czf "$archive_stage" payload metadata source
)
archive_digest=$(/usr/bin/shasum -a 256 "$archive_stage" | /usr/bin/awk '{print $1}')
printf '%s  %s\n' "$archive_digest" "$(basename -- "$archive")" > "$checksum_stage"
/bin/sync
mv "$archive_stage" "$archive"
mv "$checksum_stage" "$checksum"
/bin/sync

success=1
printf 'Backup complete and Mac Runner services remain stopped.\narchive: %s\nchecksum: %s\nsource_revision: %s\n' "$archive" "$checksum" "$source_revision"
