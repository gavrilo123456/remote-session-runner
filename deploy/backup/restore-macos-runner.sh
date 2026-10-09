#!/bin/sh
set -eu
umask 077

# Restore a stopped-service Mac continuity archive. All payloads are validated
# and staged before a target is created. If publication fails, the trap removes
# only paths that this invocation claimed. Runner remains stopped throughout.

account='tomasz.walczuk'
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
router_label='com.remote-session-runner.local'
executor_label='com.remote-session-runner.locald'
restore_marker='.remote-session-runner-restore-in-progress'

usage() {
	cat >&2 <<'EOF'
usage: restore-macos-runner.sh --archive /absolute/backup.tar.gz [--checksum /absolute/backup.tar.gz.sha256] --apply

Restores a verified continuity archive into the original selected Mac paths.
It never starts LaunchAgents. Run deploy/macos/install-launchagents.sh only
after this command succeeds.
EOF
}

fail() {
	printf '%s\n' "$*" >&2
	exit 1
}

archive=''
checksum=''
apply=0
while [ "$#" -gt 0 ]; do
	case "$1" in
		--archive)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			archive=$2
			shift 2
			;;
		--checksum)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			checksum=$2
			shift 2
			;;
		--apply)
			apply=1
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
	printf 'restore-macos-runner.sh must run on the selected Mac account %s\n' "$account" >&2
	exit 2
fi
if [ "$apply" -ne 1 ] || [ -z "$archive" ]; then
	usage
	exit 2
fi
case "$archive" in
	/*) ;;
	*) printf '%s\n' 'archive must be an absolute path' >&2; exit 2 ;;
esac
if [ -z "$checksum" ]; then
	checksum="$archive.sha256"
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
verify_script="$repo_root/deploy/backup/verify-macos-runner-backup.sh"
if [ ! -x "$verify_script" ]; then
	printf 'backup verifier is unavailable: %s\n' "$verify_script" >&2
	exit 1
fi
"$verify_script" --archive "$archive" --checksum "$checksum"

uid=$(id -u)
if [ -e "$service_root" ] || [ -L "$service_root" ]; then
	fail "refusing to overlay an existing Runner service root: $service_root"
fi
for label in "$router_label" "$executor_label"; do
	if launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
		fail "refusing restore while a Runner LaunchAgent is loaded: $label"
	fi
done

work=$(mktemp -d "${TMPDIR:-/private/tmp}/remote-session-runner-restore.XXXXXX") || exit 1
success=0
staged_paths="$work/staged-paths.tsv"
published_paths="$work/published-paths.tsv"
service_extras="$work/service-extras.tsv"
external_roots="$work/external-roots.tsv"
seen_mailboxes="$work/seen-mailboxes.tsv"
ssh_stage=''
: > "$staged_paths"
: > "$published_paths"
: > "$service_extras"
: > "$external_roots"
: > "$seen_mailboxes"
: > "$work/external-stages.tsv"

remove_recorded_paths() {
	paths_file=$1
	[ -f "$paths_file" ] || return 0
	/usr/bin/awk '{ lines[NR] = $0 } END { for (index = NR; index >= 1; index--) print lines[index] }' "$paths_file" |
	while IFS="$(printf '\t')" read -r record_type record_target record_aux; do
		[ -n "$record_target" ] || continue
		case "$record_type" in
			tree)
				if [ -d "$record_target" ] && [ ! -L "$record_target" ] && [ -f "$record_target/$restore_marker" ] && [ ! -L "$record_target/$restore_marker" ]; then
					/bin/rm -rf "$record_target" || :
				fi
				;;
			file)
				if [ -f "$record_target" ] && [ ! -L "$record_target" ] && [ -f "$record_aux" ] && [ ! -L "$record_aux" ]; then
					restored_identity=$(stat -f '%d:%i' "$record_target" 2>/dev/null || true)
					staged_identity=$(stat -f '%d:%i' "$record_aux" 2>/dev/null || true)
					if [ -n "$restored_identity" ] && [ "$restored_identity" = "$staged_identity" ]; then
						/bin/rm -f "$record_target" || :
					fi
				fi
				;;
		esac
	done
}

cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$success" -ne 1 ]; then
		remove_recorded_paths "$published_paths"
	fi
	remove_recorded_paths "$staged_paths"
	if [ -n "$ssh_stage" ] && [ -d "$ssh_stage" ] && [ ! -L "$ssh_stage" ] && [ -f "$ssh_stage/$restore_marker" ] && [ ! -L "$ssh_stage/$restore_marker" ]; then
		/bin/rm -rf "$ssh_stage" || :
	fi
	/bin/rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

expected=$(/usr/bin/awk 'NR == 1 {print $1; exit}' "$checksum")
actual=$(/usr/bin/shasum -a 256 "$archive" | /usr/bin/awk '{print $1}')
if [ "$actual" != "$expected" ]; then
	fail 'archive changed after verification; refusing restore'
fi
listing="$work/archive-members.txt"
/usr/bin/tar -tzf "$archive" > "$listing"
while IFS= read -r member; do
	case "$member" in
		''|/*|../*|*/../*|..|*'/../'*)
			fail "archive contains an unsafe member path: $member"
			;;
	esac
done < "$listing"
/usr/bin/tar -xzf "$archive" -C "$work"
if [ -n "$(find "$work" -type l -print -quit)" ]; then
	fail 'archive contains a symlink and cannot be restored'
fi
if [ -n "$(find "$work" ! -type d ! -type f -print -quit)" ]; then
	fail 'archive contains a non-regular entry and cannot be restored'
fi
if ! (cd "$work" && /usr/bin/shasum -a 256 -c metadata/SHA256SUMS >/dev/null); then
	fail 'archive member checksum mismatch after extraction'
fi

source_revision=$(/usr/bin/sed -n 's/^source_revision=//p' "$work/metadata/manifest.txt")
case "$source_revision" in
	????????????????????????????????????????) ;;
	*) fail 'archive source revision has an unexpected format' ;;
esac
if [ ! -d "$repo_root/.git" ] || ! git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	fail "restore must run from the Runner source checkout restored from this archive bundle: $repo_root"
fi
if [ "$(git -C "$repo_root" branch --show-current)" != dev ] || [ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]; then
	fail 'restore requires a clean Runner dev checkout'
fi
if [ "$(git -C "$repo_root" rev-parse HEAD)" != "$source_revision" ] || [ "$(git -C "$repo_root" rev-parse origin/dev)" != "$source_revision" ]; then
	fail 'restore checkout does not match the backup source revision; clone the bundled source before applying the runtime restore'
fi

require_real_directory() {
	directory=$1
	label=$2
	if [ -L "$directory" ] || [ ! -d "$directory" ] || [ "$(stat -f '%u' "$directory")" != "$uid" ]; then
		fail "$label must be a real directory owned by the selected account: $directory"
	fi
}

require_absent_target_parent() {
	target=$1
	label=$2
	if [ -e "$target" ] || [ -L "$target" ]; then
		fail "restore refuses to overlay existing $label: $target"
	fi
	parent=$(dirname -- "$target")
	if [ -L "$parent" ] || [ ! -d "$parent" ] || [ "$(stat -f '%u' "$parent")" != "$uid" ]; then
		fail "restore requires an existing selected-account parent for $label: $parent"
	fi
}

ensure_safe_tree() {
	tree=$1
	label=$2
	if [ -L "$tree" ] || [ ! -d "$tree" ] || [ -n "$(find "$tree" -type l -print -quit)" ]; then
		fail "$label contains an unsafe symlink: $tree"
	fi
	if [ -n "$(find "$tree" ! -type d ! -type f -print -quit)" ]; then
		fail "$label contains a non-regular entry: $tree"
	fi
}

stage_tree_for_target() {
	stage_source=$1
	stage_target=$2
	stage_label=$3
	stage_parent=$(dirname -- "$stage_target")
	stage_name=$(basename -- "$stage_target")
	staged_tree=$(mktemp -d "$stage_parent/.${stage_name}.remote-session-runner-restore.XXXXXX") || fail "could not create staging directory for $stage_label"
	/usr/bin/ditto "$stage_source/." "$staged_tree"
	ensure_safe_tree "$staged_tree" "staged $stage_label"
	: > "$staged_tree/$restore_marker"
	chmod 600 "$staged_tree/$restore_marker"
	printf 'tree\t%s\n' "$staged_tree" >> "$staged_paths"
}

publish_staged_tree() {
	publish_stage=$1
	publish_target=$2
	publish_label=$3
	if [ -e "$publish_target" ] || [ -L "$publish_target" ]; then
		fail "restore target appeared while staging $publish_label: $publish_target"
	fi
	printf 'tree\t%s\n' "$publish_target" >> "$published_paths"
	if ! /usr/bin/python3 - "$publish_stage" "$publish_target" <<'PY'
import ctypes
import os
import sys

renamex_np = ctypes.CDLL("/usr/lib/libSystem.B.dylib", use_errno=True).renamex_np
renamex_np.argtypes = (ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint)
renamex_np.restype = ctypes.c_int
result = renamex_np(os.fsencode(sys.argv[1]), os.fsencode(sys.argv[2]), 0x00000004)
if result != 0:
    error_number = ctypes.get_errno()
    raise OSError(error_number, os.strerror(error_number), sys.argv[2])
PY
	then
		fail "could not atomically publish restore target for $publish_label: $publish_target"
	fi
	if [ ! -f "$publish_target/$restore_marker" ] || [ -L "$publish_target/$restore_marker" ]; then
		fail "restore publication marker is missing for $publish_label: $publish_target"
	fi
}

normalize_service_modes() {
	tree=$1
	chmod 700 "$tree" "$tree/config" "$tree/secrets" "$tree/state" "$tree/mailbox" "$tree/mailboxes" "$tree/backups" "$tree/toolchains"
	chmod 600 "$tree/config/mac.yaml"
	find "$tree/secrets" -type f -exec chmod 600 {} \;
	find "$tree/state" -type f -exec chmod 600 {} \;
}

for component in config secrets state mailbox mailboxes backups toolchains; do
	if [ ! -d "$work/payload/service-root/$component" ] || [ -L "$work/payload/service-root/$component" ]; then
		fail "archive service component is unavailable: $component"
	fi
	ensure_safe_tree "$work/payload/service-root/$component" "archive service component $component"
done
if [ ! -x "$work/payload/service-root/toolchains/go1.27.1/bin/go" ]; then
	fail 'archive is missing the selected executable Go toolchain'
fi
require_absent_target_parent "$service_root" 'Runner service root'

external_manifest="$work/metadata/external-mailboxes.tsv"
if [ ! -f "$external_manifest" ] || [ -L "$external_manifest" ]; then
	fail 'archive external-mailbox manifest is unavailable'
fi
while IFS="$(printf '\t')" read -r inbox_id target relative extra; do
	[ -n "$inbox_id" ] || continue
	if [ -n "${extra:-}" ]; then
		fail 'archive external-mailbox manifest has too many fields'
	fi
	case "$inbox_id" in
		''|*[!A-Za-z0-9_-]*) fail "archive has an unsafe mailbox identifier: $inbox_id" ;;
	esac
	case "$target" in
		/*) ;;
		*) fail "archive mailbox target is not absolute: $target" ;;
	esac
	case "$target/" in
		*'/../'*|*'//'*) fail "archive has an unsafe mailbox target: $target" ;;
	esac
	if /usr/bin/awk -F '\t' -v candidate="$inbox_id" '$1 == candidate { found = 1 } END { exit(found ? 0 : 1) }' "$seen_mailboxes"; then
		fail "archive repeats a mailbox identifier: $inbox_id"
	fi
	printf '%s\t%s\n' "$inbox_id" "$target" >> "$seen_mailboxes"

	if [ "$relative" = 'included-in-service-root' ]; then
		case "$target" in
			"$service_root/mailbox"|"$service_root/mailbox"/*|"$service_root/mailboxes"|"$service_root/mailboxes"/*) ;;
			*) fail "archive internal mailbox is outside the backed-up service components: $target" ;;
		esac
		continue
	fi

	service_extra_payload="payload/service-root-extra/$inbox_id"
	external_payload="payload/external-mailboxes/$inbox_id"
	if [ "$relative" = "$service_extra_payload" ]; then
		case "$target" in
			"$service_root"/*) ;;
			*) fail "archive service-root mailbox is outside the service root: $target" ;;
		esac
		case "$target" in
			"$service_root/config"|"$service_root/config"/*|"$service_root/secrets"|"$service_root/secrets"/*|"$service_root/state"|"$service_root/state"/*|"$service_root/mailbox"|"$service_root/mailbox"/*|"$service_root/mailboxes"|"$service_root/mailboxes"/*|"$service_root/backups"|"$service_root/backups"/*|"$service_root/toolchains"|"$service_root/toolchains"/*)
				fail "archive service-root mailbox collides with a fixed Runner component: $target"
				;;
		esac
		if [ ! -d "$work/$relative" ] || [ -L "$work/$relative" ]; then
			fail "archive service-root mailbox payload is unavailable: $relative"
		fi
		ensure_safe_tree "$work/$relative" "archive service-root mailbox $inbox_id"
		while IFS="$(printf '\t')" read -r existing_target existing_relative; do
			case "$target" in
				"$existing_target"|"$existing_target"/*) fail "archive has overlapping service-root mailbox targets: $target and $existing_target" ;;
			esac
			case "$existing_target" in
				"$target"/*) fail "archive has overlapping service-root mailbox targets: $target and $existing_target" ;;
			esac
		done < "$service_extras"
		printf '%s\t%s\n' "$target" "$relative" >> "$service_extras"
		continue
	fi

	if [ "$relative" != "$external_payload" ]; then
		fail "archive external-mailbox payload does not match its manifest: $relative"
	fi
	case "$target" in
		"$service_root"|"$service_root"/*) fail "archive external mailbox is within the Runner service root: $target" ;;
	esac
	if [ ! -d "$work/$relative" ] || [ -L "$work/$relative" ]; then
		fail "archive external-mailbox payload is unavailable: $relative"
	fi
	ensure_safe_tree "$work/$relative" "archive external mailbox $inbox_id"
	while IFS="$(printf '\t')" read -r existing_target existing_relative; do
		case "$target" in
			"$existing_target"|"$existing_target"/*) fail "archive has overlapping external mailbox targets: $target and $existing_target" ;;
		esac
		case "$existing_target" in
			"$target"/*) fail "archive has overlapping external mailbox targets: $target and $existing_target" ;;
		esac
	done < "$external_roots"
	require_absent_target_parent "$target" "external mailbox $inbox_id"
	printf '%s\t%s\n' "$target" "$relative" >> "$external_roots"
done < "$external_manifest"

ssh_mode=$(/usr/bin/sed -n 's/^mode=//p' "$work/metadata/ssh.txt" 2>/dev/null || true)
case "$ssh_mode" in
	none)
		;;
	directory)
		if [ ! -d "$work/payload/ssh-directory" ] || [ -L "$work/payload/ssh-directory" ]; then
			fail 'archive SSH-directory payload is unavailable'
		fi
		ensure_safe_tree "$work/payload/ssh-directory" 'archive SSH directory'
		require_absent_target_parent "$HOME/.ssh" 'SSH directory'
		;;
	selected)
		if [ ! -d "$work/payload/ssh" ] || [ -L "$work/payload/ssh" ] || [ ! -f "$work/metadata/ssh.tsv" ] || [ -L "$work/metadata/ssh.tsv" ]; then
			fail 'archive selected SSH payload is unavailable'
		fi
		ensure_safe_tree "$work/payload/ssh" 'archive selected SSH payload'
		if [ -L "$HOME/.ssh" ]; then
			fail "SSH root must not be a symlink: $HOME/.ssh"
		fi
		if [ -e "$HOME/.ssh" ]; then
			require_real_directory "$HOME/.ssh" 'SSH root'
		else
			require_absent_target_parent "$HOME/.ssh" 'SSH root'
		fi
		selected_count=0
		for selected_file in "$work"/payload/ssh/*; do
			[ -e "$selected_file" ] || continue
			if [ -L "$selected_file" ] || [ ! -f "$selected_file" ]; then
				fail "archive selected SSH entry is not a regular file: $selected_file"
			fi
			selected_name=$(basename -- "$selected_file")
			case "$selected_name" in
				config|known_hosts|gavrilo123456-github|dev.slidestud.io|remote-session-runner) ;;
				*) fail "archive has an unexpected selected SSH entry: $selected_name" ;;
			esac
			if [ -e "$HOME/.ssh/$selected_name" ] || [ -L "$HOME/.ssh/$selected_name" ]; then
				fail "refusing to overwrite existing SSH material: $HOME/.ssh/$selected_name"
			fi
			selected_count=$((selected_count + 1))
		done
		if [ "$selected_count" -eq 0 ]; then
			fail 'archive selected SSH payload is empty'
		fi
		;;
	*)
		fail "archive has an unrecognized SSH backup mode: $ssh_mode"
		;;
esac

# Build the complete service-root image before creating any final target.
stage_tree_for_target "$work/payload/service-root" "$service_root" 'Runner service root'
service_stage=$staged_tree
while IFS="$(printf '\t')" read -r service_extra_target service_extra_relative; do
	[ -n "$service_extra_target" ] || continue
	service_extra_suffix=${service_extra_target#"$service_root/"}
	service_extra_stage="$service_stage/$service_extra_suffix"
	if [ -e "$service_extra_stage" ] || [ -L "$service_extra_stage" ]; then
		fail "staged service-root mailbox collides with another restored path: $service_extra_target"
	fi
	mkdir -p "$(dirname -- "$service_extra_stage")"
	/usr/bin/ditto "$work/$service_extra_relative" "$service_extra_stage"
	ensure_safe_tree "$service_extra_stage" "staged service-root mailbox $service_extra_target"
	chmod 700 "$service_extra_stage"
done < "$service_extras"
normalize_service_modes "$service_stage"

# Build every external root beside its eventual parent. Those staging paths
# remain private until all archive and SSH preflights have passed.
while IFS="$(printf '\t')" read -r external_target external_relative; do
	[ -n "$external_target" ] || continue
	stage_tree_for_target "$work/$external_relative" "$external_target" "external mailbox $external_target"
	external_stage=$staged_tree
	chmod 700 "$external_stage"
	printf '%s\t%s\n' "$external_target" "$external_stage" >> "$work/external-stages.tsv"
done < "$external_roots"

case "$ssh_mode" in
	directory)
		stage_tree_for_target "$work/payload/ssh-directory" "$HOME/.ssh" 'SSH directory'
		ssh_tree_stage=$staged_tree
		chmod 700 "$ssh_tree_stage"
		find "$ssh_tree_stage" -type f -exec chmod 600 {} \;
		printf '%s\t%s\n' "$HOME/.ssh" "$ssh_tree_stage" > "$work/ssh-tree-stage.tsv"
		;;
	selected)
		if [ ! -e "$HOME/.ssh" ]; then
			mkdir -m 700 "$work/selected-ssh-root"
			for selected_file in "$work"/payload/ssh/*; do
				[ -e "$selected_file" ] || continue
				/usr/bin/ditto "$selected_file" "$work/selected-ssh-root/$(basename -- "$selected_file")"
			done
			stage_tree_for_target "$work/selected-ssh-root" "$HOME/.ssh" 'selected SSH root'
			ssh_tree_stage=$staged_tree
			chmod 700 "$ssh_tree_stage"
			find "$ssh_tree_stage" -type f -exec chmod 600 {} \;
			printf '%s\t%s\n' "$HOME/.ssh" "$ssh_tree_stage" > "$work/ssh-tree-stage.tsv"
		else
			ssh_stage=$(mktemp -d "$HOME/.ssh/.remote-session-runner-restore.XXXXXX") || fail 'could not create selected SSH staging directory'
			: > "$ssh_stage/$restore_marker"
			chmod 600 "$ssh_stage/$restore_marker"
			for selected_file in "$work"/payload/ssh/*; do
				[ -e "$selected_file" ] || continue
				/usr/bin/ditto "$selected_file" "$ssh_stage/$(basename -- "$selected_file")"
				chmod 600 "$ssh_stage/$(basename -- "$selected_file")"
			done
		fi
		;;
esac

# Publish only after every possible late conflict has been checked. Each
# claimed target contains the marker needed for safe rollback if a later
# publication fails.
publish_staged_tree "$service_stage" "$service_root" 'Runner service root'
while IFS="$(printf '\t')" read -r external_target external_stage; do
	[ -n "$external_target" ] || continue
	publish_staged_tree "$external_stage" "$external_target" "external mailbox $external_target"
done < "$work/external-stages.tsv"

case "$ssh_mode" in
	directory|selected)
		if [ -f "$work/ssh-tree-stage.tsv" ]; then
			IFS="$(printf '\t')" read -r ssh_target ssh_tree_stage < "$work/ssh-tree-stage.tsv"
			publish_staged_tree "$ssh_tree_stage" "$ssh_target" 'SSH directory'
		else
			for selected_file in "$ssh_stage"/*; do
				[ -e "$selected_file" ] || continue
				selected_name=$(basename -- "$selected_file")
				[ "$selected_name" != "$restore_marker" ] || continue
				ssh_target="$HOME/.ssh/$selected_name"
				printf 'file\t%s\t%s\n' "$ssh_target" "$selected_file" >> "$published_paths"
				if ! ln "$selected_file" "$ssh_target"; then
					fail "could not publish selected SSH material without overwrite: $ssh_target"
				fi
			done
		fi
		;;
esac

# All durable material is now complete. The ownership markers have protected
# rollback through every publish step; after this point a failed marker removal
# leaves a complete stopped restore rather than deleting a valid one.
success=1
/usr/bin/awk -F '\t' '$1 == "tree" { print $2 }' "$published_paths" |
while IFS= read -r published_tree; do
	[ -n "$published_tree" ] || continue
	if [ -f "$published_tree/$restore_marker" ] && [ ! -L "$published_tree/$restore_marker" ]; then
		if ! /bin/rm -f "$published_tree/$restore_marker"; then
			printf 'warning: complete restore retained its marker: %s\n' "$published_tree/$restore_marker" >&2
		fi
	else
		printf 'warning: complete restore marker is unavailable: %s\n' "$published_tree/$restore_marker" >&2
	fi
done

printf '%s\n' 'Runtime restore complete. Runner remains stopped.'
printf 'Next: cd %s && deploy/macos/install-launchagents.sh\n' "$repo_root"
