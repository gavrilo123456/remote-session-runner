#!/bin/sh
set -eu

# This wrapper is installed outside the Git checkout under the owner-controlled
# Linux service root. It is invoked by the fixed authorized_keys command and
# never evaluates SSH_ORIGINAL_COMMAND as shell text.
BRIDGE_BIN="/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge"
EXPECTED_COMMAND="$BRIDGE_BIN --stdio"

if [ "${SSH_ORIGINAL_COMMAND-}" != "$EXPECTED_COMMAND" ]; then
	printf '%s\n' 'runner SSH bridge: requested command is not permitted' >&2
	exit 126
fi
if [ -n "${SSH_TTY-}" ]; then
	printf '%s\n' 'runner SSH bridge: PTY is not permitted' >&2
	exit 126
fi

exec "$BRIDGE_BIN" --stdio
