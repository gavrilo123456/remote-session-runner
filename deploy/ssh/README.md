# Linux forced-command SSH deployment

Before authorizing the queued Mac key, install and verify `runnerd.service` on
Ubuntu with `deploy/linux/install-systemd-service.sh`. The unit runs as
`ubuntu`; its startup entrypoint refuses to run with an invalid service
account, a non-owner-only service directory, or a group/other-readable config
or mTLS credential file. `runnerd` owns `/home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock`
with mode 0600 beneath the owner-only `run/` directory. The bridge below uses
that socket and cannot start the execution authority itself. A systemd stop
closes the private and HTTPS listeners and removes the socket; P130/P132 add
the shared graceful command-drain behavior.

On Ubuntu, the installer is run from the synchronized project checkout as
`ubuntu`, without `sudo`:

```sh
deploy/linux/install-systemd-service.sh
sudo systemctl status runnerd.service
```

It builds the checked-out `runnerd` into the external service root, installs
the versioned unit under `/etc/systemd/system`, and starts it. It does not
authorize a key or modify `authorized_keys`.

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
