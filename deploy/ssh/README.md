# Permanent Linux forced-command SSH bridge

Use [`install-queued-bridge.sh`](install-queued-bridge.sh) to enable, inspect,
and refresh the optional queued route. Do not use the P128 fixture as a normal
deployment path: it is intentionally temporary and refuses to alter a host
with a permanent bridge manifest.

The permanent installer runs as `ubuntu` from a clean synchronized `dev`
checkout. It requires an enabled and active `runnerd.service`, its owner-only
`runnerd.sock`, the selected Go toolchain, and an owner-only `authorized_keys`
file. It accepts one canonical `ssh-ed25519` **public** key. It never accepts
or prints private-key material.

```text
install-queued-bridge.sh enable --dispatcher-public-key-file PATH
install-queued-bridge.sh enable --dispatcher-public-key-stdin
install-queued-bridge.sh refresh
install-queued-bridge.sh status
```

`enable` records these persistent, Ubuntu-owned paths:

```text
bin/runner-ssh-bridge                         mode 0700
bin/runner-ssh-bridge-forced.sh               mode 0700
config/ssh-controller-map.yaml                mode 0600
config/queued-ssh-dispatcher.pub              mode 0600
config/queued-ssh-bridge.manifest             mode 0600
```

It also appends one exact restricted line to
`/home/ubuntu/.ssh/authorized_keys`, preserving unrelated entries:

```text
restrict,command="/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge-forced.sh SHA256:<fingerprint>" ssh-ed25519 <dispatcher-public-key> runner-mac-dispatcher
```

The installer stages files privately, atomically replaces each destination,
and changes `authorized_keys` last. A partial installation, a duplicate key,
or a different dispatcher identity fails closed. Changing the dispatcher key
requires an explicit reviewed rotation procedure; `enable` and `refresh` will
not replace it silently.

The `restrict` option disables PTY allocation, agent and X11 forwarding, TCP
forwarding, and the user startup file. The wrapper checks
`SSH_ORIGINAL_COMMAND` byte-for-byte against `runner-ssh-bridge --stdio`, the
exact command sent by the Mac client. It rejects a PTY and executes only the
fixed bridge with the server-supplied key fingerprint, controller map, and
owner-only Runner socket.

Run `status` after enabling and after every controlled Linux deployment. When
a permanent manifest exists, the normal
`deploy/linux/install-systemd-service.sh` path preflights the existing identity
before replacing `runnerd`, runs the read-only zero-active-work gate before
restarting an active service and repeats it immediately before restart, waits
for `runnerd.service` to recreate its private socket, then calls `refresh`.
Refresh updates the bridge binary and fixed wrapper for that source revision
without changing the public key, controller map, or authorization line.
