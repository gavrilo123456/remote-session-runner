#!/bin/sh
set -eu

# This wrapper is installed outside the Git checkout under the owner-controlled
# Linux service root. It is invoked by the fixed authorized_keys command and
# never evaluates SSH_ORIGINAL_COMMAND as shell text.
BRIDGE_BIN="/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge"
CONTROLLER_MAP="/home/ubuntu/.local/share/remote-session-runner/config/ssh-controller-map.yaml"
RUNNERD_SOCKET="/home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock"
EXPECTED_COMMAND="runner-ssh-bridge --stdio"

if [ "$#" -ne 1 ]; then
	printf '%s\n' 'runner SSH bridge: authenticated key identity is not permitted' >&2
	exit 126
fi
AUTHENTICATED_KEY=$1
KEY_FINGERPRINT=${AUTHENTICATED_KEY#SHA256:}
case "$AUTHENTICATED_KEY" in
	SHA256:*) ;;
	*) printf '%s\n' 'runner SSH bridge: authenticated key identity is not permitted' >&2; exit 126 ;;
esac
case "$KEY_FINGERPRINT" in
	''|*[!A-Za-z0-9+/]*) printf '%s\n' 'runner SSH bridge: authenticated key identity is not permitted' >&2; exit 126 ;;
esac
if [ "${#KEY_FINGERPRINT}" -ne 43 ]; then
	printf '%s\n' 'runner SSH bridge: authenticated key identity is not permitted' >&2
	exit 126
fi

if [ "${SSH_ORIGINAL_COMMAND-}" != "$EXPECTED_COMMAND" ]; then
	printf '%s\n' 'runner SSH bridge: requested command is not permitted' >&2
	exit 126
fi
if [ -n "${SSH_TTY-}" ]; then
	printf '%s\n' 'runner SSH bridge: PTY is not permitted' >&2
	exit 126
fi

exec "$BRIDGE_BIN" --stdio --authenticated-key "$AUTHENTICATED_KEY" --controller-map "$CONTROLLER_MAP" --runnerd-socket "$RUNNERD_SOCKET"
