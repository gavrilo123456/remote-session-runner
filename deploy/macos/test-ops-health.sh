#!/bin/sh
set -eu
umask 077

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
go_bin=${GO:-"$service_root/toolchains/go1.27.1/bin/go"}
remote_repo='/home/ubuntu/projects/remote-session-runner'
expected_commit=${RSR_P129_EXPECTED_COMMIT:-${RSR_P128_EXPECTED_COMMIT:-}}
: "${expected_commit:?set to the exact P129 commit already pushed and pulled to Ubuntu}"
admin_identity=${RUNNER_P128_ADMIN_SSH_IDENTITY:-/Users/tomasz.walczuk/.ssh/remote-session-runner}
remote_fixture_pending=0
remote_fixture_token=''
scratch=''

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != tomasz.walczuk ]; then
	printf '%s\n' 'test-ops-health.sh must run on the selected Mac account tomasz.walczuk' >&2
	exit 2
fi
if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain is not executable: %s\n' "$go_bin" >&2
	exit 1
fi
: "${RUNNER_P128_SSH_IDENTITY:?set to the owner-only dispatcher SSH identity}"
: "${RUNNER_P128_SSH_KNOWN_HOSTS:?set to the pinned Ubuntu host-key file}"
if [ ! -r "$admin_identity" ]; then
	printf 'Ubuntu host-inspection identity is unavailable: %s\n' "$admin_identity" >&2
	exit 1
fi

ssh_admin() {
	ssh -F /dev/null -i "$admin_identity" -o IdentitiesOnly=yes -o BatchMode=yes \
		-o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o LogLevel=ERROR \
		-o "UserKnownHostsFile=\"$RUNNER_P128_SSH_KNOWN_HOSTS\"" \
		ubuntu@129.151.232.40 "$@"
}

if [ "$(git -C "$repo_root" branch --show-current)" != dev ] \
	|| [ "$(git -C "$repo_root" rev-parse HEAD)" != "$expected_commit" ] \
	|| [ "$(git -C "$repo_root" rev-parse origin/dev)" != "$expected_commit" ] \
	|| [ -n "$(git -C "$repo_root" status --porcelain)" ]; then
	printf '%s\n' 'Mac checkout must be clean dev at the exact P128 commit before host validation' >&2
	exit 1
fi
github_commit=$(git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev | awk 'NR == 1 {print $1}')
if [ "$github_commit" != "$expected_commit" ]; then
	printf 'GitHub dev=%s, expected pushed P128 commit=%s\n' "$github_commit" "$expected_commit" >&2
	exit 1
fi
if ! ssh_admin "test \"\$(git -C '$remote_repo' branch --show-current)\" = dev && test \"\$(git -C '$remote_repo' rev-parse HEAD)\" = '$expected_commit' && test \"\$(git -C '$remote_repo' rev-parse origin/dev)\" = '$expected_commit' && test -z \"\$(git -C '$remote_repo' status --porcelain)\""; then
	printf '%s\n' 'Ubuntu checkout is not clean dev at the pushed P128 commit' >&2
	exit 1
fi

for path in \
	"$service_root/bin" "$service_root/config" "$service_root/logs" \
	"$service_root/run" "$service_root/state" "$service_root/mailbox" \
	"$service_root/workspaces" "$service_root/tmp" "$service_root/backups"; do
	if [ -e "$path" ] || [ -L "$path" ]; then
		printf 'refusing to alter an existing Mac Runner path: %s\n' "$path" >&2
		exit 1
	fi
done

scratch=$(mktemp -d /private/tmp/remote-session-runner-p128-mac.XXXXXX)
cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$remote_fixture_pending" -eq 1 ]; then
		if ! ssh_admin "cd '$remote_repo' && deploy/linux/p128-ops-ssh-fixture.sh cleanup '$remote_fixture_token'"; then
			printf '%s\n' 'P128 remote SSH fixture cleanup failed; preserve phase stop and inspect Ubuntu host state' >&2
			status=1
		fi
	fi
	for name in bin config logs run state mailbox workspaces tmp backups; do
		rm -rf "$service_root/$name"
	done
	rm -rf "$scratch"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s\n' 'Running synchronized Ubuntu P128 regression and P129 operational-metrics validation before the Mac two-host acceptance gate.'
ssh_admin "cd '$remote_repo' && RSR_P128_EXPECTED_COMMIT='$expected_commit' deploy/linux/test-ops-health.sh"

dispatcher_fingerprint=$(ssh-keygen -lf "$RUNNER_P128_SSH_IDENTITY.pub" | awk '{print $2}')
dispatcher_public=$(cat "$RUNNER_P128_SSH_IDENTITY.pub")
dispatcher_public_base64=$(printf '%s' "$dispatcher_public" | base64 | tr -d '\n')
remote_fixture_token="p128ops-$(date +%s)-$$"
remote_fixture_pending=1
ssh_admin "cd '$remote_repo' && deploy/linux/p128-ops-ssh-fixture.sh setup '$remote_fixture_token' '$dispatcher_fingerprint' '$dispatcher_public_base64'"

config_file="$scratch/mac.yaml"
install -m 600 "$repo_root/deploy/macos/mac.yaml.example" "$config_file"
export RUNNER_P128_MAC_CONFIG="$config_file"
export RUNNER_P128_SERVER_CA="$service_root/secrets/poc-ca.pem"
export RUNNER_P128_CLIENT_CERT="$service_root/secrets/direct-client.pem"
export RUNNER_P128_CLIENT_KEY="$service_root/secrets/direct-client.key"
export RSR_P128_OPS_HOST_GATE=1
export GOTOOLCHAIN=local

"$go_bin" test -tags=p128opshost ./src/internal/runnerlocal \
	-run '^TestP128MacIngressAcceptsDurableIntentDuringRemoteOutage$' -count=1 -v
"$go_bin" test -tags=p128opshost ./src/internal/runnerlocald \
	-run '^TestP128MacLocalExecutorDoctorHost$' -count=1 -v
"$go_bin" test -tags=p128opshost ./src/internal/runnerd \
	-run '^TestP128PublicHealthRequiresMandatoryMTLS$' -count=1 -v

ssh_admin "cd '$remote_repo' && deploy/linux/p128-ops-ssh-fixture.sh cleanup '$remote_fixture_token'"
remote_fixture_pending=0

printf '%s\n' 'P128 regression and P129 two-host operational-metrics gates passed; temporary dispatcher authorization and Mac service paths are being removed.'
