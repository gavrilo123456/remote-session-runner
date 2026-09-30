# Linux systemd deployment

This guide describes the current accepted `linux-host` only:
`ubuntu@oracle-yuta-konopka-ubuntu-micro-02`, with listener bind
`10.0.0.200:8443` and public client endpoint `https://129.151.232.40:8443`.
`runnerd.service` runs as `ubuntu` with mandatory TLS 1.3 mTLS. No tunnel or
Podman service is involved.

On the synchronized project checkout, run as `ubuntu`:

```sh
deploy/linux/install-systemd-service.sh
```

The installer requires the selected Go 1.27.1 toolchain and preprovisioned
owner-only `config/linux.yaml`, `config/client-principals.yaml`, server
certificate/key, and client CA. It never prints private key material. It builds
`runnerd` below the external service root, verifies owner/mode/no-symlink
requirements, installs and checks the unit, then enables and starts the
service. Runtime configuration and SQLite state are not copied from Git.

The unit uses `User=ubuntu`, `Group=ubuntu`, and `UMask=0077`. Its entrypoint
checks the account, selected paths, launcher/runnerd binaries, config, and mTLS
file modes before startup. Scripts retain the selected Ubuntu OS permissions;
the workspace is not a containment boundary.

For an active-service upgrade, the installer runs the checked-in read-only
zero-active-work gate before the build and immediately before restart. It
refuses the restart when sessions, commands, unreleased slots, or unfinished
jobs are present. Plan a maintenance window because the gate is a snapshot and
normal graceful shutdown handles work admitted afterward truthfully.

```sh
# Current Ubuntu — ubuntu
sudo systemctl status runnerd.service --no-pager
make test-p128-host-status
```

If the permanent queued bridge already exists, the installer preflights its
identity before replacing `runnerd`; once the new private socket is ready, it
refreshes the bridge binary and forced wrapper for the same clean synchronized
revision. It does not create, change, or rotate the dispatcher authorization.
Then verify:

```sh
# Current Ubuntu — ubuntu
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

SIGTERM closes admission, drains accepted requests, cleans up through normal
runtime paths, validates the durable tail, then removes the private socket.
`TimeoutStopSec=30s` and `KillMode=mixed` give Runner time to clean its Bash
children before systemd enforces the boundary.

A new physical Ubuntu machine needs its own configuration, state, service,
certificate/principal or bridge materials, host-key pin, and P157 evidence. Do
not copy `linux-host` state or credentials and do not treat this host's
listener or successful test as evidence for it.
