#!/bin/sh
set -eu
umask 077

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
remote_repo='/home/ubuntu/projects/remote-session-runner'
go_bin=${GO:-"$service_root/toolchains/go1.27.1/bin/go"}
expected_commit=${RSR_P140_EXPECTED_COMMIT:-}
admin_identity=${RUNNER_P128_ADMIN_SSH_IDENTITY:-/Users/tomasz.walczuk/.ssh/remote-session-runner}
dispatcher_identity=${RUNNER_P140_SSH_IDENTITY:-"$service_root/secrets/dispatcher_ed25519"}
known_hosts=${RUNNER_P140_SSH_KNOWN_HOSTS:-"$service_root/secrets/ssh_known_hosts"}
remote_fixture_token=''
remote_fixture_pending=0
authorized_keys_before=''
remote_bridge_mode=''

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != tomasz.walczuk ]; then
	printf '%s\n' 'test-p140-twohost.sh must run on the selected Mac account tomasz.walczuk' >&2
	exit 2
fi
: "${expected_commit:?set the exact P140 source commit pushed and pulled to Ubuntu}"
: "${RUNNER_P128_SSH_KNOWN_HOSTS:?set to the pinned Ubuntu host-key file for the admin identity}"
if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain is not executable: %s\n' "$go_bin" >&2
	exit 1
fi
if [ ! -r "$admin_identity" ] || [ ! -r "$dispatcher_identity" ] || [ ! -r "$dispatcher_identity.pub" ] || [ ! -r "$known_hosts" ]; then
	printf '%s\n' 'P140 requires the separate admin SSH identity, dedicated dispatcher identity/public key, and pinned host file' >&2
	exit 1
fi

ssh_admin() {
	ssh -F /dev/null -i "$admin_identity" -o IdentitiesOnly=yes -o BatchMode=yes \
		-o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o LogLevel=ERROR \
		-o "UserKnownHostsFile=\"$RUNNER_P128_SSH_KNOWN_HOSTS\"" \
		ubuntu@129.151.232.40 "$@"
}

verify_permanent_bridge() {
	remote_bridge_status=$(ssh_admin "cd '$remote_repo' && deploy/ssh/install-queued-bridge.sh status") || return 1
	remote_bridge_fingerprint=$(printf '%s\n' "$remote_bridge_status" | awk -F= '$1 == "dispatcher_fingerprint" {print $2}')
	[ "$remote_bridge_fingerprint" = "$dispatcher_fingerprint" ]
}

status=$(git -C "$repo_root" status --porcelain --untracked-files=all)
if [ "$(git -C "$repo_root" branch --show-current)" != dev ] \
	|| [ "$(git -C "$repo_root" rev-parse HEAD)" != "$expected_commit" ] \
	|| [ "$(git -C "$repo_root" rev-parse origin/dev)" != "$expected_commit" ] \
	|| [ "$status" != '?? 040-implementation-evidence/P140.md' ]; then
	printf '%s\n' 'Mac checkout must be dev at the expected P140 commit with only the phase evidence draft untracked' >&2
	printf 'Observed status: %s\n' "$status" >&2
	exit 1
fi
github_commit=$(git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev | awk 'NR == 1 {print $1}')
if [ "$github_commit" != "$expected_commit" ]; then
	printf 'GitHub dev=%s, expected P140 source commit=%s\n' "$github_commit" "$expected_commit" >&2
	exit 1
fi
if ! ssh_admin "test \"\$(git -C '$remote_repo' branch --show-current)\" = dev && test \"\$(git -C '$remote_repo' rev-parse HEAD)\" = '$expected_commit' && test \"\$(git -C '$remote_repo' rev-parse origin/dev)\" = '$expected_commit' && test -z \"\$(git -C '$remote_repo' status --porcelain)\""; then
	printf '%s\n' 'Ubuntu checkout is not clean dev at the exact pushed P140 source commit' >&2
	exit 1
fi
if ! ssh_admin "sudo -n systemctl is-active --quiet runnerd.service && test -S /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock && test -x /home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge"; then
	printf '%s\n' 'Ubuntu production runnerd or its SSH bridge is not ready; P140 will not alter or restart it' >&2
	exit 1
fi
authorized_keys_before=$(ssh_admin "sha256sum /home/ubuntu/.ssh/authorized_keys | awk '{print \$1}'")

cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$remote_fixture_pending" -eq 1 ]; then
		if ! ssh_admin "cd '$remote_repo' && deploy/linux/p128-ops-ssh-fixture.sh cleanup '$remote_fixture_token'"; then
			printf '%s\n' 'P140 forced SSH fixture cleanup failed; stop and inspect Ubuntu host state' >&2
			status=1
		fi
		remote_fixture_pending=0
	fi
	if [ -n "$authorized_keys_before" ]; then
		current_hash=$(ssh_admin "sha256sum /home/ubuntu/.ssh/authorized_keys | awk '{print \$1}'") || status=1
		if [ "$current_hash" != "$authorized_keys_before" ]; then
			printf '%s\n' 'Ubuntu authorized_keys hash did not return to its exact P140 pre-test value' >&2
			status=1
		fi
		if [ "$remote_bridge_mode" = fixture ]; then
			if ! ssh_admin "test ! -e /home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge-forced.sh && test ! -e /home/ubuntu/.local/share/remote-session-runner/config/ssh-controller-map.yaml"; then
				printf '%s\n' 'P140 temporary forced-command wrapper or controller map remains on Ubuntu' >&2
				status=1
			fi
		elif [ "$remote_bridge_mode" = permanent ]; then
			if ! verify_permanent_bridge; then
				printf '%s\n' 'P140 permanent queued bridge did not remain ready for the selected dispatcher key' >&2
				status=1
			fi
		fi
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

dispatcher_fingerprint=$(ssh-keygen -lf "$dispatcher_identity.pub" | awk '{print $2}')
dispatcher_public=$(cat "$dispatcher_identity.pub")
dispatcher_public_base64=$(printf '%s' "$dispatcher_public" | base64 | tr -d '\n')
if ssh_admin "test -e /home/ubuntu/.local/share/remote-session-runner/config/queued-ssh-bridge.manifest || test -L /home/ubuntu/.local/share/remote-session-runner/config/queued-ssh-bridge.manifest"; then
	if ! verify_permanent_bridge; then
		printf '%s\n' 'permanent queued bridge is not ready for the selected dispatcher key; P140 will not replace it with a fixture' >&2
		exit 1
	fi
	remote_bridge_mode=permanent
	printf '%s\n' 'Using the verified permanent queued bridge; P140 will preserve it.'
else
	remote_fixture_token="p140-$(date +%s)-$$"
	remote_fixture_pending=1
	ssh_admin "cd '$remote_repo' && deploy/linux/p128-ops-ssh-fixture.sh setup '$remote_fixture_token' '$dispatcher_fingerprint' '$dispatcher_public_base64'"
	remote_bridge_mode=fixture
fi

export RUNNER_P140_SSH_IDENTITY="$dispatcher_identity"
export RUNNER_P140_SSH_KNOWN_HOSTS="$known_hosts"
export RSR_P140_MAC_HOST_GATE=1
export GOTOOLCHAIN=local
"$go_bin" test -tags=p140twohost ./src/internal/localapi \
	-run '^TestP140MacLinuxOrdinalGapSurvivesRestoreAndReconcilesBeforeNextDispatch$' -count=1 -v

if [ "$remote_bridge_mode" = fixture ]; then
	ssh_admin "cd '$remote_repo' && deploy/linux/p128-ops-ssh-fixture.sh cleanup '$remote_fixture_token'"
	remote_fixture_pending=0
else
	verify_permanent_bridge
fi
printf '%s\n' 'P140 Mac/Ubuntu backup, restore, ID reconciliation, ordered dispatch, and selected queued bridge preservation passed.'
