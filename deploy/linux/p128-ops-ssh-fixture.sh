#!/bin/sh
set -eu
umask 077

action=${1:?usage: p128-ops-ssh-fixture.sh setup|cleanup token [fingerprint public-key-base64]}
token=${2:?temporary fixture token is required}
case "$token" in
	*[!A-Za-z0-9_-]*|'') printf '%s\n' 'invalid P128 fixture token' >&2; exit 2 ;;
esac

service_root='/home/ubuntu/.local/share/remote-session-runner'
repo_root='/home/ubuntu/projects/remote-session-runner'
authorized_keys='/home/ubuntu/.ssh/authorized_keys'
wrapper="$service_root/bin/runner-ssh-bridge-forced.sh"
controller_map="$service_root/config/ssh-controller-map.yaml"
permanent_manifest="$service_root/config/queued-ssh-bridge.manifest"
permanent_public="$service_root/config/queued-ssh-dispatcher.pub"
lock_path="$service_root/run/queued-bridge-install.lock"
state="/tmp/$token"

if [ "$(id -un)" != ubuntu ] || [ "$(id -u)" != 1001 ]; then
	printf '%s\n' 'P128 SSH fixture must run as ubuntu uid 1001' >&2
	exit 2
fi
if [ ! -d "$service_root/run" ] || [ -L "$service_root/run" ]; then
	printf '%s\n' 'P128 SSH fixture requires a real Runner run directory' >&2
	exit 1
fi
if [ -L "$lock_path" ]; then
	printf '%s\n' 'P128 SSH fixture refuses an unsafe queued bridge lock path' >&2
	exit 1
fi
if [ ! -e "$lock_path" ]; then
	: > "$lock_path"
	chmod 600 "$lock_path"
fi
if [ ! -f "$lock_path" ] || [ "$(stat -c '%u:%a' "$lock_path")" != '1001:600' ]; then
	printf '%s\n' 'P128 SSH fixture requires an ubuntu-owned mode-0600 queued bridge lock' >&2
	exit 1
fi
exec 9>"$lock_path"
if ! flock -n 9; then
	printf '%s\n' 'P128 SSH fixture could not acquire the queued bridge maintenance lock' >&2
	exit 1
fi
if [ -e "$permanent_manifest" ] || [ -L "$permanent_manifest" ] \
	|| [ -e "$permanent_public" ] || [ -L "$permanent_public" ] \
	|| { [ -f "$authorized_keys" ] && grep -Fq 'runner-mac-dispatcher' "$authorized_keys"; }; then
	printf '%s\n' 'P128 SSH fixture refuses to alter a host with permanent queued bridge state' >&2
	exit 1
fi

if [ "$action" = setup ]; then
	fingerprint=${3:?public-key fingerprint is required}
	public_key_base64=${4:?public dispatcher key is required}
	case "$fingerprint" in
		SHA256:*) ;;
		*) printf '%s\n' 'invalid P128 public-key fingerprint' >&2; exit 2 ;;
	esac
	if [ -e "$state" ] || [ -L "$state" ] || [ -e "$wrapper" ] || [ -L "$wrapper" ] || [ -e "$controller_map" ] || [ -L "$controller_map" ]; then
		printf '%s\n' 'refusing to overwrite existing P128 SSH fixture state' >&2
		exit 1
	fi
	if [ ! -f "$authorized_keys" ] || [ -L "$authorized_keys" ] || [ "$(stat -c '%u:%a' "$authorized_keys")" != '1001:600' ]; then
		printf '%s\n' 'Ubuntu authorized_keys must be a regular ubuntu-owned mode-0600 file' >&2
		exit 1
	fi
	if [ "$(tail -c 1 "$authorized_keys" | od -An -t x1 | tr -d ' \n')" != 0a ]; then
		printf '%s\n' 'Ubuntu authorized_keys must end with a newline; refusing to alter its formatting' >&2
		exit 1
	fi
	if ! sudo -n systemctl is-active --quiet runnerd.service || [ ! -S "$service_root/run/runnerd.sock" ] || [ ! -x "$service_root/bin/runner-ssh-bridge" ]; then
		printf '%s\n' 'P128 dispatcher fixture requires the active runnerd socket and bridge binary' >&2
		exit 1
	fi
	if grep -Fq "$fingerprint" "$authorized_keys"; then
		printf '%s\n' 'dispatcher identity already appears in authorized_keys; refusing duplicate authorization' >&2
		exit 1
	fi

	mkdir -m 700 "$state"
	printf '%s' "$public_key_base64" | base64 --decode > "$state/dispatcher.pub"
	if [ "$(awk 'NR == 1 {print $1}' "$state/dispatcher.pub")" != ssh-ed25519 ] \
		|| [ "$(ssh-keygen -lf "$state/dispatcher.pub" | awk '{print $2}')" != "$fingerprint" ]; then
		printf '%s\n' 'provided dispatcher public key does not match its fingerprint' >&2
		exit 1
	fi
	public_key_type=$(awk 'NR == 1 {print $1}' "$state/dispatcher.pub")
	public_key_data=$(awk 'NR == 1 {print $2}' "$state/dispatcher.pub")
	printf 'version: 1\nkeys:\n  "%s":\n    controller_type: queued_mac\n    controller_id: tomasz.walczuk\n' "$fingerprint" > "$state/controller-map.yaml"
	cp "$repo_root/deploy/ssh/runner-ssh-bridge-forced.sh" "$state/runner-ssh-bridge-forced.sh"
	printf 'restrict,command="%s %s" %s %s runner-p128-temporary\n' \
		"$wrapper" "$fingerprint" "$public_key_type" "$public_key_data" > "$state/authorized-line"
	sha256sum "$authorized_keys" | awk '{print $1}' > "$state/authorized_keys.before.sha256"

	install -o ubuntu -g ubuntu -m 600 "$state/controller-map.yaml" "$controller_map"
	install -o ubuntu -g ubuntu -m 700 "$state/runner-ssh-bridge-forced.sh" "$wrapper"
	cat "$state/authorized-line" >> "$authorized_keys"
	chmod 600 "$authorized_keys"
	printf '%s\n' 'P128 temporary restricted dispatcher authorization installed.'
	 exit 0
fi

if [ "$action" != cleanup ]; then
	printf '%s\n' 'action must be setup or cleanup' >&2
	exit 2
fi

if [ ! -d "$state" ] || [ -L "$state" ]; then
	printf '%s\n' 'P128 temporary SSH fixture state is already absent.'
	exit 0
fi
cleanup_error=0
if [ -f "$state/authorized-line" ] && [ -f "$authorized_keys" ] && [ ! -L "$authorized_keys" ]; then
	line=$(cat "$state/authorized-line")
	if grep -Fqx "$line" "$authorized_keys"; then
		AUTHORIZED_LINE="$line" awk '$0 != ENVIRON["AUTHORIZED_LINE"]' "$authorized_keys" > "$state/authorized_keys.next"
		chmod 600 "$state/authorized_keys.next"
		chown ubuntu:ubuntu "$state/authorized_keys.next"
		mv "$state/authorized_keys.next" "$authorized_keys"
	fi
	if grep -Fqx "$line" "$authorized_keys"; then
		printf '%s\n' 'P128 dispatcher authorization remains in authorized_keys' >&2
		cleanup_error=1
	fi
fi
if [ -f "$state/controller-map.yaml" ] && [ -e "$controller_map" ]; then
	if cmp -s "$state/controller-map.yaml" "$controller_map"; then
		rm -f "$controller_map"
	else
		printf '%s\n' 'P128 controller map changed during the host test; preserving it for inspection' >&2
		cleanup_error=1
	fi
fi
if [ -f "$state/runner-ssh-bridge-forced.sh" ] && [ -e "$wrapper" ]; then
	if cmp -s "$state/runner-ssh-bridge-forced.sh" "$wrapper"; then
		rm -f "$wrapper"
	else
		printf '%s\n' 'P128 forced-command wrapper changed during the host test; preserving it for inspection' >&2
		cleanup_error=1
	fi
fi
if [ -f "$state/authorized_keys.before.sha256" ]; then
	current_hash=$(sha256sum "$authorized_keys" | awk '{print $1}')
	expected_hash=$(cat "$state/authorized_keys.before.sha256")
	if [ "$current_hash" != "$expected_hash" ]; then
		printf '%s\n' 'Ubuntu authorized_keys differs from its pre-test hash after removing the temporary line' >&2
		cleanup_error=1
	fi
fi
rm -rf "$state"
if [ "$cleanup_error" -ne 0 ]; then
	exit 1
fi
printf '%s\n' 'P128 temporary dispatcher authorization, controller map, and forced-command wrapper removed; authorized_keys hash restored.'
