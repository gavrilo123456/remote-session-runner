# Linux forced-command SSH deployment

The queued Mac dispatcher key is authorized on Ubuntu with one exact entry:

```text
restrict,command="/home/ubuntu/.local/share/remote-session-runner/deploy/runner-ssh-bridge-forced.sh SHA256:<fingerprint-without-padding>" ssh-ed25519 <dispatcher-public-key> runner-mac-dispatcher
```

The `restrict` option disables PTY allocation, agent and X11 forwarding, TCP
forwarding, and the user startup file. The wrapper checks
`SSH_ORIGINAL_COMMAND` byte-for-byte against `runner-ssh-bridge --stdio`, the
exact command sent by the Mac client. The fingerprint argument comes from the
server-controlled forced command, not from SSH request data. The wrapper
passes it to the bridge along with fixed service paths; the bridge resolves it
through the owner-only `config/ssh-controller-map.yaml` before forwarding
frames to the owner-only runnerd socket. The map has this shape:

```yaml
version: 1
keys:
  "SHA256:<fingerprint-without-padding>":
    controller_type: queued_mac
    controller_id: tomasz.walczuk
```

The wrapper executes only
`/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge` with
fixed arguments and never evaluates the requested command as shell text. The
wrapper, binary, map, and configuration are owned by `ubuntu` and are not
writable by other users; the `authorized_keys` file remains mode `0600`.

Install the wrapper and binary under the selected Linux service root, render the
entry from the dedicated public dispatcher key, inspect the resulting line, and
append it only after verifying the existing file and a pinned-host-key SSH
check. Keep private keys outside Git. A host-side deployment may replace the
fixed paths with a reviewed service-root equivalent, but must preserve the exact
`restrict` option and wrapper checks.
