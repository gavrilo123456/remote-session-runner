#!/bin/sh
set -eu
umask 077

# Install and maintain the optional permanent queued-SSH route on the selected
# Ubuntu host. This script deliberately handles only public-key material. The
# Mac dispatcher private key remains in its owner-only Mac service root.

service_root='/home/ubuntu/.local/share/remote-session-runner'
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
go_bin="$service_root/toolchains/go1.27.1/bin/go"
bin_dir="$service_root/bin"
config_dir="$service_root/config"
run_dir="$service_root/run"
tmp_dir="$service_root/tmp"
ssh_dir='/home/ubuntu/.ssh'
authorized_keys="$ssh_dir/authorized_keys"
bridge="$bin_dir/runner-ssh-bridge"
wrapper="$bin_dir/runner-ssh-bridge-forced.sh"
wrapper_source="$repo_root/deploy/ssh/runner-ssh-bridge-forced.sh"
controller_map="$config_dir/ssh-controller-map.yaml"
dispatcher_public="$config_dir/queued-ssh-dispatcher.pub"
manifest="$config_dir/queued-ssh-bridge.manifest"
socket_path="$run_dir/runnerd.sock"
lock_path="$run_dir/queued-bridge-install.lock"
max_public_key_bytes=16384

uid=''
source_commit=''
dispatcher_fingerprint=''
dispatcher_key_type=''
dispatcher_key_data=''
manifest_source_commit=''
manifest_bridge_hash=''
manifest_wrapper_hash=''
work_dir=''
input_public=''
enable_public=''
normalized_public=''
expected_map=''
expected_authorized=''
stage_bridge=''
stage_wrapper=''
stage_public=''
stage_map=''
stage_manifest=''
authorized_next=''
authorized_backup=''
authorized_before_hash=''
authorized_before_signature=''
authorized_next_hash=''
bridge_backup=''
initial_install_in_progress=0
initial_authorization_replaced=0
initial_public_installed=0
initial_map_installed=0
initial_wrapper_installed=0
initial_bridge_replaced=0
initial_manifest_installed=0
initial_bridge_was_present=0
refresh_in_progress=0
refresh_bridge_replaced=0
refresh_wrapper_replaced=0
refresh_manifest_replaced=0
refresh_bridge_backup=''
refresh_wrapper_backup=''
refresh_manifest_backup=''
refresh_bridge_stage_hash=''
refresh_wrapper_stage_hash=''
refresh_manifest_stage_hash=''
cleanup_preserve_recovery=0

die() {
	printf 'queued bridge: %s\n' "$*" >&2
	exit 1
}

usage() {
	printf '%s\n' \
		'usage:' \
		'  install-queued-bridge.sh enable --dispatcher-public-key-file PATH' \
		'  install-queued-bridge.sh enable --dispatcher-public-key-stdin' \
		'  install-queued-bridge.sh preflight' \
		'  install-queued-bridge.sh refresh' \
		'  install-queued-bridge.sh status'
}

cleanup() {
	cleanup_status=$?
	trap - EXIT
	# A second ordinary termination signal must not interrupt a rollback after
	# the first signal has already entered cleanup.
	trap '' HUP INT TERM
	if [ "$initial_install_in_progress" -eq 1 ]; then
		if ! rollback_initial; then
			cleanup_status=1
			cleanup_preserve_recovery=1
		fi
	fi
	if [ "$refresh_in_progress" -eq 1 ]; then
		if ! rollback_refresh; then
			cleanup_status=1
			cleanup_preserve_recovery=1
		fi
	fi
	for cleanup_path in \
		"$work_dir" "$input_public" "$enable_public" "$normalized_public" "$expected_map" \
		"$expected_authorized" "$stage_bridge" "$stage_wrapper" "$stage_public" \
		"$stage_map" "$stage_manifest" "$authorized_next" "$authorized_backup" \
		"$bridge_backup" "$refresh_bridge_backup" "$refresh_wrapper_backup" \
		"$refresh_manifest_backup"; do
		if [ "$cleanup_preserve_recovery" -eq 1 ] \
			&& { [ "$cleanup_path" = "$authorized_backup" ] || [ "$cleanup_path" = "$bridge_backup" ] \
				|| [ "$cleanup_path" = "$refresh_bridge_backup" ] || [ "$cleanup_path" = "$refresh_wrapper_backup" ] \
				|| [ "$cleanup_path" = "$refresh_manifest_backup" ]; }; then
			continue
		fi
		if [ -n "$cleanup_path" ] && { [ -e "$cleanup_path" ] || [ -L "$cleanup_path" ]; }; then
			rm -rf -- "$cleanup_path"
		fi
	done
	exit "$cleanup_status"
}

remove_temporary_path() {
	if [ -n "$1" ] && { [ -e "$1" ] || [ -L "$1" ]; }; then
		rm -rf -- "$1"
	fi
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

ignore_termination_signals() {
	# Each staged transaction changes several files. Ignore catchable termination
	# signals while committing it so cleanup cannot observe a successful mv before
	# its rollback state flag has been recorded. Power loss and SIGKILL remain
	# outside this guarantee; status detects incomplete state and refuses it.
	trap '' HUP
	trap '' INT
	trap '' TERM
}

restore_termination_traps() {
	trap 'exit 129' HUP
	trap 'exit 130' INT
	trap 'exit 143' TERM
}

require_private_directory() {
	required_directory=$1
	if [ -L "$required_directory" ] || [ ! -d "$required_directory" ]; then
		die "required private directory is missing or unsafe: $required_directory"
	fi
	required_owner_mode=$(stat -c '%u:%a' "$required_directory") || die "could not inspect directory: $required_directory"
	if [ "$required_owner_mode" != "$uid:700" ]; then
		die "required private directory is not ubuntu-owned mode 0700: $required_directory"
	fi
}

require_regular_mode() {
	required_file=$1
	required_mode=$2
	if [ -L "$required_file" ] || [ ! -f "$required_file" ]; then
		die "required regular file is missing or unsafe: $required_file"
	fi
	required_owner_mode=$(stat -c '%u:%a' "$required_file") || die "could not inspect file: $required_file"
	if [ "$required_owner_mode" != "$uid:$required_mode" ]; then
		die "required file is not ubuntu-owned mode $required_mode: $required_file"
	fi
}

require_source_checkout() {
	if [ ! -d "$repo_root/.git" ] || ! git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		die "expected checked-out repository is unavailable: $repo_root"
	fi
	if [ "$(git -C "$repo_root" branch --show-current)" != dev ]; then
		die 'repository must be on dev before queued bridge maintenance'
	fi
	if [ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]; then
		die 'repository must be clean before queued bridge maintenance'
	fi
	source_commit=$(git -C "$repo_root" rev-parse HEAD) || die 'could not resolve source commit'
	source_origin_commit=$(git -C "$repo_root" rev-parse origin/dev 2>/dev/null) || die 'repository does not have origin/dev for queued bridge maintenance'
	if ! printf '%s' "$source_commit" | grep -Eq '^[0-9a-f]{40}$'; then
		die 'source commit has an unexpected format'
	fi
	if [ "$source_commit" != "$source_origin_commit" ]; then
		die 'repository HEAD does not match origin/dev for queued bridge maintenance'
	fi
	if [ -L "$wrapper_source" ] || [ ! -f "$wrapper_source" ]; then
		die "checked-in forced-command wrapper is unavailable or unsafe: $wrapper_source"
	fi
}

require_static_runtime() {
	if [ "$(uname -s)" != Linux ] || [ "$(id -un)" != ubuntu ] || [ "$(id -u)" != 1001 ]; then
		die 'must run on the selected Linux host as ubuntu (uid 1001)'
	fi
	uid=$(id -u)
	require_source_checkout
	for required_directory in "$service_root" "$bin_dir" "$config_dir" "$run_dir" "$tmp_dir"; do
		require_private_directory "$required_directory"
	done
	if [ ! -x "$go_bin" ]; then
		die "Go 1.27.1 toolchain is unavailable: $go_bin"
	fi
	go_version=$("$go_bin" version)
	if [ "$go_version" != 'go version go1.27.1 linux/amd64' ]; then
		die "expected Go 1.27.1 linux/amd64, got: $go_version"
	fi
	require_private_directory "$ssh_dir"
	require_regular_mode "$authorized_keys" 600
	if [ -s "$authorized_keys" ]; then
		last_byte=$(tail -c 1 "$authorized_keys" | od -An -t x1 | tr -d ' \n')
		if [ "$last_byte" != 0a ]; then
			die 'authorized_keys must end with a newline'
		fi
	fi
}

require_runtime() {
	require_static_runtime
	if ! sudo -n systemctl is-enabled --quiet runnerd.service; then
		die 'runnerd.service is not enabled'
	fi
	if ! sudo -n systemctl is-active --quiet runnerd.service; then
		die 'runnerd.service is not active'
	fi
	if [ -L "$socket_path" ] || [ ! -S "$socket_path" ]; then
		die "runnerd socket is unavailable or unsafe: $socket_path"
	fi
	socket_owner_mode=$(stat -c '%u:%a' "$socket_path") || die 'could not inspect runnerd socket'
	if [ "$socket_owner_mode" != "$uid:600" ]; then
		die "runnerd socket is not ubuntu-owned mode 0600: $socket_path"
	fi
}

acquire_lock() {
	if [ -L "$lock_path" ]; then
		die "queued bridge lock is unsafe: $lock_path"
	fi
	if [ ! -e "$lock_path" ]; then
		: > "$lock_path"
		chmod 600 "$lock_path"
	fi
	require_regular_mode "$lock_path" 600
	exec 9>"$lock_path"
	if ! flock -n 9; then
		die 'another queued bridge maintenance operation is already running'
	fi
}

sha256_file() {
	hash_path=$1
	hash_output=$(sha256sum "$hash_path") || die "could not hash: $hash_path"
	hash_value=$(printf '%s\n' "$hash_output" | awk 'NR == 1 {print $1}')
	if ! printf '%s' "$hash_value" | grep -Eq '^[0-9a-f]{64}$'; then
		die "unexpected SHA-256 result for: $hash_path"
	fi
	printf '%s\n' "$hash_value"
}

valid_fingerprint() {
	case "$1" in
		SHA256:*) ;;
		*) return 1 ;;
	esac
	fingerprint_body=$(printf '%s' "$1" | sed 's/^SHA256://')
	if ! printf '%s' "$fingerprint_body" | grep -Eq '^[A-Za-z0-9+/]{43}$'; then
		return 1
	fi
	return 0
}

normalize_public_key() {
	key_source=$1
	key_destination=$2
	if [ -L "$key_source" ] || [ ! -f "$key_source" ]; then
		die "dispatcher public-key input is missing or unsafe: $key_source"
	fi
	if [ "$(wc -l < "$key_source" | tr -d ' ')" != 1 ]; then
		die 'dispatcher public-key input must contain exactly one newline-terminated line'
	fi
	if [ "$(tail -c 1 "$key_source" | od -An -t x1 | tr -d ' \n')" != 0a ]; then
		die 'dispatcher public-key input must end with a newline'
	fi
	dispatcher_key_type=$(awk 'NR == 1 {print $1}' "$key_source")
	dispatcher_key_data=$(awk 'NR == 1 {print $2}' "$key_source")
	key_fields=$(awk 'NR == 1 {print NF}' "$key_source")
	if [ "$dispatcher_key_type" != ssh-ed25519 ] || { [ "$key_fields" != 2 ] && [ "$key_fields" != 3 ]; }; then
		die 'dispatcher public-key input must be one ssh-ed25519 key with at most one comment'
	fi
	if ! printf '%s' "$dispatcher_key_data" | grep -Eq '^[A-Za-z0-9+/=]+$'; then
		die 'dispatcher public-key input has invalid base64 data'
	fi
	printf '%s %s runner-mac-dispatcher\n' "$dispatcher_key_type" "$dispatcher_key_data" > "$key_destination"
	chmod 600 "$key_destination"
	fingerprint_output=$(ssh-keygen -lf "$key_destination" -E sha256 2>/dev/null) || die 'dispatcher public-key input is not a valid SSH public key'
	dispatcher_fingerprint=$(printf '%s\n' "$fingerprint_output" | awk 'NR == 1 {print $2}')
	if ! valid_fingerprint "$dispatcher_fingerprint"; then
		die 'dispatcher public-key fingerprint has an unexpected format'
	fi
}

render_controller_map() {
	map_destination=$1
	cat > "$map_destination" <<EOF
version: 1
keys:
  "$dispatcher_fingerprint":
    controller_type: queued_mac
    controller_id: tomasz.walczuk
EOF
	chmod 600 "$map_destination"
}

render_authorized_line() {
	authorized_destination=$1
	printf 'restrict,command="%s %s" %s %s runner-mac-dispatcher\n' \
		"$wrapper" "$dispatcher_fingerprint" "$dispatcher_key_type" "$dispatcher_key_data" > "$authorized_destination"
	chmod 600 "$authorized_destination"
}

render_manifest() {
	manifest_destination=$1
	manifest_commit=$2
	manifest_bridge=$3
	manifest_wrapper=$4
	public_hash=$(sha256_file "$dispatcher_public_for_manifest")
	bridge_hash=$(sha256_file "$manifest_bridge")
	wrapper_hash=$(sha256_file "$manifest_wrapper")
	cat > "$manifest_destination" <<EOF
version=1
source_commit=$manifest_commit
fingerprint=$dispatcher_fingerprint
public_key_sha256=$public_hash
bridge_sha256=$bridge_hash
wrapper_sha256=$wrapper_hash
EOF
	chmod 600 "$manifest_destination"
}

require_manifest_shape() {
	manifest_lines=$(wc -l < "$manifest" | tr -d ' ')
	if [ "$manifest_lines" != 6 ]; then
		die 'queued bridge manifest has an unexpected line count'
	fi
	if [ "$(sed -n '1p' "$manifest")" != 'version=1' ]; then
		die 'queued bridge manifest has an unsupported version'
	fi
	manifest_source_line=$(sed -n '2p' "$manifest")
	manifest_fingerprint_line=$(sed -n '3p' "$manifest")
	manifest_public_hash_line=$(sed -n '4p' "$manifest")
	manifest_bridge_hash_line=$(sed -n '5p' "$manifest")
	manifest_wrapper_hash_line=$(sed -n '6p' "$manifest")
	case "$manifest_source_line" in source_commit=*) ;; *) die 'queued bridge manifest has an invalid source-commit field' ;; esac
	case "$manifest_fingerprint_line" in fingerprint=*) ;; *) die 'queued bridge manifest has an invalid fingerprint field' ;; esac
	case "$manifest_public_hash_line" in public_key_sha256=*) ;; *) die 'queued bridge manifest has an invalid public-key hash field' ;; esac
	case "$manifest_bridge_hash_line" in bridge_sha256=*) ;; *) die 'queued bridge manifest has an invalid bridge hash field' ;; esac
	case "$manifest_wrapper_hash_line" in wrapper_sha256=*) ;; *) die 'queued bridge manifest has an invalid wrapper hash field' ;; esac
	manifest_source_commit=$(printf '%s' "$manifest_source_line" | cut -d= -f2-)
	manifest_fingerprint=$(printf '%s' "$manifest_fingerprint_line" | cut -d= -f2-)
	manifest_public_hash=$(printf '%s' "$manifest_public_hash_line" | cut -d= -f2-)
	manifest_bridge_hash=$(printf '%s' "$manifest_bridge_hash_line" | cut -d= -f2-)
	manifest_wrapper_hash=$(printf '%s' "$manifest_wrapper_hash_line" | cut -d= -f2-)
	if ! printf '%s' "$manifest_source_commit" | grep -Eq '^[0-9a-f]{40}$'; then
		die 'queued bridge manifest has an invalid source commit'
	fi
	if [ "$manifest_fingerprint" != "$dispatcher_fingerprint" ]; then
		die 'queued bridge manifest does not match the dispatcher fingerprint'
	fi
	current_public_hash=$(sha256_file "$dispatcher_public")
	if [ "$manifest_public_hash" != "$current_public_hash" ]; then
		die 'queued bridge manifest does not match the dispatcher public key'
	fi
	for declared_hash in "$manifest_bridge_hash" "$manifest_wrapper_hash"; do
		if ! printf '%s' "$declared_hash" | grep -Eq '^[0-9a-f]{64}$'; then
			die 'queued bridge manifest has an invalid artifact hash'
		fi
	done
}

count_exact_lines() {
	needle=$1
	line_count=$(grep -Fxc "$needle" "$authorized_keys" || true)
	if [ -z "$line_count" ]; then
		line_count=0
	fi
	printf '%s\n' "$line_count"
}

count_containing_lines() {
	needle=$1
	line_count=$(grep -Fc "$needle" "$authorized_keys" || true)
	if [ -z "$line_count" ]; then
		line_count=0
	fi
	printf '%s\n' "$line_count"
}

render_expected_identity() {
	remove_temporary_path "$expected_map"
	remove_temporary_path "$expected_authorized"
	expected_map=$(mktemp "$config_dir/.queued-bridge-map.XXXXXX")
	expected_authorized=$(mktemp "$config_dir/.queued-bridge-authorized.XXXXXX")
	render_controller_map "$expected_map"
	render_authorized_line "$expected_authorized"
	expected_authorized_line=$(cat "$expected_authorized")
}

validate_identity() {
	require_regular_mode "$dispatcher_public" 600
	require_regular_mode "$controller_map" 600
	require_regular_mode "$manifest" 600
	require_regular_mode "$bridge" 700
	require_regular_mode "$wrapper" 700
	remove_temporary_path "$normalized_public"
	normalized_public=$(mktemp "$config_dir/.queued-bridge-public.XXXXXX")
	normalize_public_key "$dispatcher_public" "$normalized_public"
	if ! cmp -s "$dispatcher_public" "$normalized_public"; then
		die 'persisted dispatcher public key is not canonical'
	fi
	render_expected_identity
	if ! cmp -s "$controller_map" "$expected_map"; then
		die 'controller map does not contain exactly the selected dispatcher mapping'
	fi
	exact_count=$(count_exact_lines "$expected_authorized_line")
	wrapper_count=$(count_containing_lines "$wrapper")
	comment_count=$(count_containing_lines 'runner-mac-dispatcher')
	key_count=$(count_containing_lines "$dispatcher_key_data")
	if [ "$exact_count" != 1 ] || [ "$wrapper_count" != 1 ] || [ "$comment_count" != 1 ] || [ "$key_count" != 1 ]; then
		die 'authorized_keys does not contain exactly one restricted selected dispatcher identity'
	fi
	require_manifest_shape
}

validate_strict() {
	validate_identity
	current_bridge_hash=$(sha256_file "$bridge")
	current_wrapper_hash=$(sha256_file "$wrapper")
	if [ "$manifest_bridge_hash" != "$current_bridge_hash" ] || [ "$manifest_wrapper_hash" != "$current_wrapper_hash" ]; then
		die 'queued bridge artifacts do not match the recorded manifest; run a reviewed refresh'
	fi
	if [ "$manifest_source_commit" != "$source_commit" ]; then
		die 'queued bridge has not been refreshed for the checked-out source revision'
	fi
	if ! cmp -s "$wrapper" "$wrapper_source"; then
		die 'queued bridge wrapper does not match the checked-in forced-command wrapper'
	fi
}

managed_authorization_present() {
	if grep -Fq "$wrapper" "$authorized_keys" || grep -Fq 'runner-mac-dispatcher' "$authorized_keys"; then
		return 0
	fi
	return 1
}

state_kind() {
	state_count=0
	for state_path in "$dispatcher_public" "$controller_map" "$wrapper" "$manifest"; do
		if [ -e "$state_path" ] || [ -L "$state_path" ]; then
			state_count=$((state_count + 1))
		fi
	done
	if [ "$state_count" -eq 0 ] && ! managed_authorization_present; then
		printf '%s\n' absent
		return
	fi
	if [ "$state_count" -eq 4 ] && managed_authorization_present; then
		printf '%s\n' present
		return
	fi
	printf '%s\n' partial
}

assert_fresh_authorization() {
	if managed_authorization_present || grep -Fq "$dispatcher_key_data" "$authorized_keys"; then
		die 'dispatcher identity or managed bridge marker already appears in authorized_keys'
	fi
}

authorized_keys_signature() {
	stat -c '%d:%i:%s:%Y:%Z' "$authorized_keys"
}

authorized_keys_unchanged() {
	current_authorized_hash=$(sha256_file "$authorized_keys")
	current_authorized_signature=$(authorized_keys_signature) || return 1
	[ "$current_authorized_hash" = "$authorized_before_hash" ] \
		&& [ "$current_authorized_signature" = "$authorized_before_signature" ]
}

prepare_initial_authorization() {
	render_expected_identity
	authorized_before_hash=$(sha256_file "$authorized_keys")
	authorized_before_signature=$(authorized_keys_signature) || die 'could not inspect authorized_keys before installation'
	authorized_backup=$(mktemp "$ssh_dir/.authorized_keys.queued-bridge-backup.XXXXXX")
	authorized_next=$(mktemp "$ssh_dir/.authorized_keys.queued-bridge-next.XXXXXX")
	cat "$authorized_keys" > "$authorized_backup"
	cat "$authorized_keys" > "$authorized_next"
	printf '%s\n' "$expected_authorized_line" >> "$authorized_next"
	chmod 600 "$authorized_backup" "$authorized_next"
	authorized_next_hash=$(sha256_file "$authorized_next")
}

preserve_initial_bridge() {
	initial_bridge_was_present=0
	if [ -e "$bridge" ] || [ -L "$bridge" ]; then
		require_regular_mode "$bridge" 700
		initial_bridge_was_present=1
		bridge_backup=$(mktemp "$bin_dir/.runner-ssh-bridge-backup.XXXXXX")
		cp -p "$bridge" "$bridge_backup"
		chmod 700 "$bridge_backup"
	fi
}

rollback_initial() {
	rollback_error=0
	if [ "$initial_authorization_replaced" -eq 1 ]; then
		current_authorized_hash=$(sha256_file "$authorized_keys") || rollback_error=1
		if [ "$rollback_error" -eq 0 ] && [ "$current_authorized_hash" = "$authorized_next_hash" ] \
			&& [ -n "$authorized_backup" ] && [ -f "$authorized_backup" ] && [ ! -L "$authorized_backup" ]; then
			if mv -f -- "$authorized_backup" "$authorized_keys"; then
				authorized_backup=''
			else
				rollback_error=1
			fi
		else
			rollback_error=1
		fi
		if [ "$rollback_error" -ne 0 ]; then
			printf '%s\n' 'queued bridge: rollback could not safely restore authorized_keys; preserving recovery files' >&2
			return 1
		fi
	fi
	if [ "$initial_manifest_installed" -eq 1 ]; then
		rm -f -- "$manifest" || rollback_error=1
	fi
	if [ "$initial_map_installed" -eq 1 ]; then
		rm -f -- "$controller_map" || rollback_error=1
	fi
	if [ "$initial_wrapper_installed" -eq 1 ]; then
		rm -f -- "$wrapper" || rollback_error=1
	fi
	if [ "$initial_public_installed" -eq 1 ]; then
		rm -f -- "$dispatcher_public" || rollback_error=1
	fi
	if [ "$initial_bridge_replaced" -eq 1 ]; then
		if [ "$initial_bridge_was_present" -eq 1 ]; then
			if [ -n "$bridge_backup" ] && [ -f "$bridge_backup" ] && [ ! -L "$bridge_backup" ]; then
				if mv -f -- "$bridge_backup" "$bridge"; then
					bridge_backup=''
				else
					rollback_error=1
				fi
			else
				rollback_error=1
			fi
		else
			rm -f -- "$bridge" || rollback_error=1
		fi
	fi
	initial_install_in_progress=0
	if [ "$rollback_error" -ne 0 ]; then
		printf '%s\n' 'queued bridge: rollback did not finish safely; preserving recovery files' >&2
		return 1
	fi
	return 0
}

prepare_refresh_backups() {
	refresh_bridge_backup=$(mktemp "$bin_dir/.runner-ssh-bridge-refresh-backup.XXXXXX")
	refresh_wrapper_backup=$(mktemp "$bin_dir/.runner-ssh-bridge-forced-refresh-backup.XXXXXX")
	refresh_manifest_backup=$(mktemp "$config_dir/.queued-ssh-bridge-refresh-backup.XXXXXX")
	cp -p "$bridge" "$refresh_bridge_backup"
	cp -p "$wrapper" "$refresh_wrapper_backup"
	cp -p "$manifest" "$refresh_manifest_backup"
	chmod 700 "$refresh_bridge_backup" "$refresh_wrapper_backup"
	chmod 600 "$refresh_manifest_backup"
	refresh_bridge_stage_hash=$(sha256_file "$stage_bridge")
	refresh_wrapper_stage_hash=$(sha256_file "$stage_wrapper")
	refresh_manifest_stage_hash=$(sha256_file "$stage_manifest")
}

restore_refresh_artifact() {
	restore_target=$1
	restore_expected_hash=$2
	restore_backup=$3
	if [ -z "$restore_backup" ] || [ -L "$restore_target" ] || [ ! -f "$restore_target" ] \
		|| [ -L "$restore_backup" ] || [ ! -f "$restore_backup" ]; then
		return 1
	fi
	restore_current_hash=$(sha256sum "$restore_target" | awk 'NR == 1 {print $1}') || return 1
	if ! printf '%s' "$restore_current_hash" | grep -Eq '^[0-9a-f]{64}$' \
		|| [ "$restore_current_hash" != "$restore_expected_hash" ]; then
		return 1
	fi
	mv -f -- "$restore_backup" "$restore_target"
}

rollback_refresh() {
	refresh_rollback_error=0
	if [ "$refresh_manifest_replaced" -eq 1 ]; then
		if restore_refresh_artifact "$manifest" "$refresh_manifest_stage_hash" "$refresh_manifest_backup"; then
			refresh_manifest_backup=''
		else
			refresh_rollback_error=1
		fi
	fi
	if [ "$refresh_wrapper_replaced" -eq 1 ]; then
		if restore_refresh_artifact "$wrapper" "$refresh_wrapper_stage_hash" "$refresh_wrapper_backup"; then
			refresh_wrapper_backup=''
		else
			refresh_rollback_error=1
		fi
	fi
	if [ "$refresh_bridge_replaced" -eq 1 ]; then
		if restore_refresh_artifact "$bridge" "$refresh_bridge_stage_hash" "$refresh_bridge_backup"; then
			refresh_bridge_backup=''
		else
			refresh_rollback_error=1
		fi
	fi
	refresh_in_progress=0
	if [ "$refresh_rollback_error" -ne 0 ]; then
		printf '%s\n' 'queued bridge: refresh rollback did not finish safely; preserving recovery files' >&2
		return 1
	fi
	return 0
}

prepare_staged_artifacts() {
	dispatcher_public_for_manifest=$1
	stage_bridge=$(mktemp "$bin_dir/.runner-ssh-bridge.XXXXXX")
	stage_wrapper=$(mktemp "$bin_dir/.runner-ssh-bridge-forced.sh.XXXXXX")
	stage_map=$(mktemp "$config_dir/.ssh-controller-map.yaml.XXXXXX")
	stage_manifest=$(mktemp "$config_dir/.queued-ssh-bridge.manifest.XXXXXX")
	if [ "$dispatcher_public_for_manifest" != "$dispatcher_public" ]; then
		stage_public=$(mktemp "$config_dir/.queued-ssh-dispatcher.pub.XXXXXX")
		cp "$dispatcher_public_for_manifest" "$stage_public"
		chmod 600 "$stage_public"
		dispatcher_public_for_manifest="$stage_public"
	fi
	(cd "$repo_root" && GOTOOLCHAIN=local "$go_bin" build -o "$stage_bridge" ./src/cmd/runner-ssh-bridge)
	chmod 700 "$stage_bridge"
	install -m 700 "$wrapper_source" "$stage_wrapper"
	render_controller_map "$stage_map"
	render_manifest "$stage_manifest" "$source_commit" "$stage_bridge" "$stage_wrapper"
}

install_initial() {
	assert_fresh_authorization
	prepare_initial_authorization
	preserve_initial_bridge
	prepare_staged_artifacts "$enable_public"
	initial_install_in_progress=1
	ignore_termination_signals
	if ! mv -f -- "$stage_public" "$dispatcher_public"; then
		die 'could not install dispatcher public key'
	fi
	stage_public=''
	initial_public_installed=1
	if ! mv -f -- "$stage_map" "$controller_map"; then
		die 'could not install controller map'
	fi
	stage_map=''
	initial_map_installed=1
	if ! mv -f -- "$stage_wrapper" "$wrapper"; then
		die 'could not install forced-command wrapper'
	fi
	stage_wrapper=''
	initial_wrapper_installed=1
	if ! mv -f -- "$stage_bridge" "$bridge"; then
		die 'could not install SSH bridge'
	fi
	stage_bridge=''
	initial_bridge_replaced=1
	if ! mv -f -- "$stage_manifest" "$manifest"; then
		die 'could not install queued bridge manifest'
	fi
	stage_manifest=''
	initial_manifest_installed=1
	if ! authorized_keys_unchanged; then
		die 'authorized_keys changed during bridge installation; refusing to replace it'
	fi
	if ! mv -f -- "$authorized_next" "$authorized_keys"; then
		die 'could not install restricted dispatcher authorization'
	fi
	authorized_next=''
	initial_authorization_replaced=1
	validate_strict
	initial_install_in_progress=0
	restore_termination_traps
	remove_temporary_path "$authorized_backup"
	authorized_backup=''
	remove_temporary_path "$bridge_backup"
	bridge_backup=''
}

refresh_bridge() {
	validate_identity
	prepare_staged_artifacts "$dispatcher_public"
	prepare_refresh_backups
	refresh_in_progress=1
	ignore_termination_signals
	if ! mv -f -- "$stage_bridge" "$bridge"; then
		die 'could not refresh SSH bridge'
	fi
	stage_bridge=''
	refresh_bridge_replaced=1
	if ! mv -f -- "$stage_wrapper" "$wrapper"; then
		die 'could not refresh forced-command wrapper'
	fi
	stage_wrapper=''
	refresh_wrapper_replaced=1
	if ! mv -f -- "$stage_manifest" "$manifest"; then
		die 'could not refresh queued bridge manifest'
	fi
	stage_manifest=''
	refresh_manifest_replaced=1
	validate_strict
	refresh_in_progress=0
	restore_termination_traps
	remove_temporary_path "$refresh_bridge_backup"
	refresh_bridge_backup=''
	remove_temporary_path "$refresh_wrapper_backup"
	refresh_wrapper_backup=''
	remove_temporary_path "$refresh_manifest_backup"
	refresh_manifest_backup=''
}

read_enable_input() {
	input_mode=$1
	input_value=$2
	input_public=$(mktemp "$config_dir/.queued-bridge-input.XXXXXX")
	if [ "$input_mode" = file ]; then
		if [ -L "$input_value" ] || [ ! -f "$input_value" ]; then
			die "dispatcher public-key input file is missing or unsafe: $input_value"
		fi
		input_bytes=$(wc -c < "$input_value" | tr -d ' ')
		if [ "$input_bytes" -gt "$max_public_key_bytes" ]; then
			die 'dispatcher public-key input exceeds the size limit'
		fi
		cat "$input_value" > "$input_public"
	else
		dd bs=1 count=$((max_public_key_bytes + 1)) 2>/dev/null > "$input_public"
		input_bytes=$(wc -c < "$input_public" | tr -d ' ')
		if [ "$input_bytes" -gt "$max_public_key_bytes" ]; then
			die 'dispatcher public-key input exceeds the size limit'
		fi
	fi
	chmod 600 "$input_public"
	enable_public=$(mktemp "$config_dir/.queued-bridge-normalized.XXXXXX")
	normalize_public_key "$input_public" "$enable_public"
}

if [ "$#" -lt 1 ]; then
	usage >&2
	exit 2
fi
action=$1
shift

case "$action" in
	enable)
		if [ "$#" -eq 1 ] && [ "$1" = --dispatcher-public-key-stdin ]; then
			enable_input_mode=stdin
			enable_input_value=''
		elif [ "$#" -eq 2 ] && [ "$1" = --dispatcher-public-key-file ]; then
			enable_input_mode=file
			enable_input_value=$2
		else
			usage >&2
			exit 2
		fi
		require_runtime
		acquire_lock
		read_enable_input "$enable_input_mode" "$enable_input_value"
		case "$(state_kind)" in
			absent)
				install_initial
				;;
			present)
				validate_identity
				if ! cmp -s "$dispatcher_public" "$enable_public"; then
					die 'existing permanent dispatcher identity differs; explicit reviewed key rotation is required'
				fi
				refresh_bridge
				;;
			*)
				die 'partial queued bridge state is present; inspect and repair it before enabling'
				;;
		esac
		printf 'queued_bridge_status=ready\n'
		printf 'dispatcher_fingerprint=%s\n' "$dispatcher_fingerprint"
		printf 'source_commit=%s\n' "$source_commit"
		;;
	refresh)
		if [ "$#" -ne 0 ]; then
			usage >&2
			exit 2
		fi
		require_runtime
		acquire_lock
		if [ "$(state_kind)" != present ]; then
			die 'permanent queued bridge is absent or partial; refresh will not enable it'
		fi
		refresh_bridge
		printf 'queued_bridge_status=ready\n'
		printf 'dispatcher_fingerprint=%s\n' "$dispatcher_fingerprint"
		printf 'source_commit=%s\n' "$source_commit"
		;;
	preflight)
		if [ "$#" -ne 0 ]; then
			usage >&2
			exit 2
		fi
		require_static_runtime
		acquire_lock
		if [ "$(state_kind)" != present ]; then
			die 'permanent queued bridge is absent or partial; preflight will not enable it'
		fi
		validate_identity
		printf 'queued_bridge_preflight=ready\n'
		printf 'dispatcher_fingerprint=%s\n' "$dispatcher_fingerprint"
		printf 'source_commit=%s\n' "$source_commit"
		;;
	status)
		if [ "$#" -ne 0 ]; then
			usage >&2
			exit 2
		fi
		require_runtime
		if [ "$(state_kind)" != present ]; then
			die 'permanent queued bridge is absent or partial'
		fi
		validate_strict
		printf 'queued_bridge_status=ready\n'
		printf 'dispatcher_fingerprint=%s\n' "$dispatcher_fingerprint"
		printf 'source_commit=%s\n' "$manifest_source_commit"
		printf 'bridge_sha256=%s\n' "$manifest_bridge_hash"
		printf 'wrapper_sha256=%s\n' "$manifest_wrapper_hash"
		;;
	*)
		usage >&2
		exit 2
		;;
esac
