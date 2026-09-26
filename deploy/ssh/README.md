# Linux forced-command SSH deployment

The queued Mac dispatcher key is authorized on Ubuntu with one exact entry:

```text
restrict,command="/home/ubuntu/.local/share/remote-session-runner/deploy/runner-ssh-bridge-forced.sh" ssh-ed25519 <dispatcher-public-key> runner-mac-dispatcher
```

The `restrict` option disables PTY allocation, agent and X11 forwarding, TCP
forwarding, and the user startup file. The wrapper checks
`SSH_ORIGINAL_COMMAND` against `/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge --stdio`
and then executes that fixed binary with one fixed argument. It never evaluates
the requested command as shell text. The wrapper, binary, and configuration are
owned by `ubuntu` and are not writable by other users; the `authorized_keys`
file remains mode `0600`.

Install the wrapper and binary under the selected Linux service root, render the
entry from the dedicated public dispatcher key, inspect the resulting line, and
append it only after verifying the existing file and a pinned-host-key SSH
check. Keep private keys outside Git. A host-side deployment may replace the
fixed paths with a reviewed service-root equivalent, but must preserve the exact
`restrict` option and wrapper checks.
