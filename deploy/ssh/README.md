# Permanent Linux forced-command SSH bridge

Use [`install-queued-bridge.sh`](install-queued-bridge.sh) to enable, inspect,
and refresh the optional queued route on an accepted remote profile. It is
installed independently on `linux-host` and `sandbox-host`; their controller
authorization, dispatcher key, host-key pin, socket, and manifest are not
shared. Do not use the P128 fixture as a deployment path: it is temporary and
refuses to alter a host with a permanent bridge manifest.

The permanent installer runs as `ubuntu` from a clean synchronized `dev`
checkout. It requires an enabled `runnerd.service`, its owner-only `runnerd.sock`,
the selected Go toolchain, and owner-only `authorized_keys`. It accepts one
canonical `ssh-ed25519` **public** key. It never accepts or prints private-key
material.

It accepts only native Go 1.27.1 `linux/amd64` on `x86_64`/`amd64`, or
`linux/arm64` on `aarch64`/`arm64`; a mismatched cross toolchain fails closed.
Bridge builds reuse the selected `ubuntu` account's normal Go build and module
caches at `/home/ubuntu/.cache/go-build` and `/home/ubuntu/go/pkg/mod`. The
bridge never deletes those shared account caches or creates a disposable
per-invocation cache tree.

```text
install-queued-bridge.sh enable --dispatcher-public-key-file PATH
install-queued-bridge.sh enable --dispatcher-public-key-stdin
install-queued-bridge.sh refresh
install-queued-bridge.sh status
```

`enable` creates Ubuntu-owned paths:

```text
bin/runner-ssh-bridge                         mode 0700
bin/runner-ssh-bridge-forced.sh               mode 0700
config/ssh-controller-map.yaml                mode 0600
config/queued-ssh-dispatcher.pub              mode 0600
config/queued-ssh-bridge.manifest             mode 0600
```

It appends one exact restricted forced-command entry to
`/home/ubuntu/.ssh/authorized_keys`, preserving unrelated entries. It stages
and validates all files privately, changes authorization last, and fails closed
for partial state, duplicate keys, or identity mismatch. Key rotation needs an
explicit reviewed procedure; `enable` and `refresh` never replace it silently.

The `restrict` option disables PTY allocation, agent/X11 forwarding, TCP
forwarding, and startup files. The wrapper checks `SSH_ORIGINAL_COMMAND`
byte-for-byte against `runner-ssh-bridge --stdio`, rejects PTY use, and
forwards only to the configured owner-only Runner socket. It does not provide a
general remote shell.

Run `status` after enable and each controlled Linux deployment. When a manifest
exists, `deploy/linux/install-systemd-service.sh` preflights its existing
identity, runs two zero-active-work checks before an active restart, waits for
the new socket, and calls `refresh`. Refresh updates only the bridge program
and fixed wrapper for that source revision; it does not alter the public key,
controller map, or authorization line.

This bridge proves only the profile installed on that host. `sandbox-host`
passed its separate P157 gate with a bridge on
`ubuntu@oracle-gustaw-janecki-ubuntu-flex-02`; its Mac route is
`ubuntu-sandbox`, which the active policy permits as an `analytics` override
and uses as the `slidestud-io` mailbox default. See
[`P157-sandbox-host.md`](../../040-implementation-evidence/P157-sandbox-host.md)
and [`P158-slidestud-external-mailbox.md`](../../040-implementation-evidence/P158-slidestud-external-mailbox.md).

Every later remote profile needs separate key pinning, controller authorization,
service deployment, and P157 end-to-end acceptance. A profile not yet attempted
is `NOT RUN`; a required P157 gate that runs and fails is recorded as `FAIL`,
not as accepted evidence.
