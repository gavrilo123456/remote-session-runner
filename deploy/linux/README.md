# Linux systemd deployment

This guide applies to the two accepted Ubuntu profiles. Each has an independent
owner-only configuration, state database, certificate material, and queued
bridge authorization.

| Profile | Host | Listener bind | Public client endpoint | Architecture |
| --- | --- | --- | --- | --- |
| `linux-host` | `ubuntu@oracle-yuta-konopka-ubuntu-micro-02` | `10.0.0.200:8443` | `https://129.151.232.40:8443` | `linux/amd64` |
| `sandbox-host` | `ubuntu@oracle-gustaw-janecki-ubuntu-flex-02` | `10.0.0.14:8443` | `https://132.226.205.205:8443` | `linux/arm64` |

`runnerd.service` runs as `ubuntu` with mandatory TLS 1.3 mTLS. No tunnel or
Podman service is involved. The sandbox acceptance is recorded in
[`P157-sandbox-host.md`](../../040-implementation-evidence/P157-sandbox-host.md).

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

The installer accepts only a native Go 1.27.1 `linux/amd64` toolchain on
`x86_64`/`amd64`, or `linux/arm64` on `aarch64`/`arm64`; it rejects any other
architecture or a mismatched cross toolchain. It creates a private temporary
Go build/module cache below the Runner service-root `tmp/` directory for one
invocation and restores owner write permission on Go's read-only module files
before removing only that task-owned cache. For a stopped service it cleans the
cache before start; for an active service it preserves the immediate
zero-active-work-to-restart boundary and cleans it after the healthy transition.

The unit uses `User=ubuntu`, `Group=ubuntu`, and `UMask=0077`. Its entrypoint
checks the account, selected paths, launcher/runnerd binaries, config, and mTLS
file modes before startup. Scripts retain the selected Ubuntu OS permissions;
the workspace is not a containment boundary.

The installer requires a clean `dev` checkout at the synchronized `origin/dev`
revision. It embeds that full revision in `runnerd` and verifies the live
private-socket `/health/ready` `build_revision` after systemd starts the
service. This attests the running process, unlike `runnerd --version`, a bridge
manifest, a binary checksum, or a separately launched `doctor` command. Use
the exact [Ubuntu revision-attestation command](../../docs/setup.md#attest-the-running-ubuntu-revision)
when recording an install.

For an active-service upgrade, the installer runs the checked-in read-only
zero-active-work gate before the build and immediately before restart. It
refuses the restart when sessions, commands, unreleased slots, or unfinished
jobs are present. Plan a maintenance window because the gate is a snapshot and
normal graceful shutdown handles work admitted afterward truthfully.

Do not use this restart-based installer to clear retained capacity while ready
sessions with queued commands must survive. Keep `runnerd.service` active and
use the guarded owner-only
[queue-preserving online recovery](../../docs/operations.md#queue-preserving-online-retained-capacity-recovery)
instead. That procedure prohibits this installer, offline recovery,
cancellation, and replay until the retained capacity is safely released.

```sh
# Target Ubuntu host — ubuntu
sudo systemctl status runnerd.service --no-pager
make test-p128-host-status
```

If the permanent queued bridge already exists, the installer preflights its
identity before replacing `runnerd`; once the new private socket is ready, it
refreshes the bridge binary and forced wrapper for the same clean synchronized
revision. It does not create, change, or rotate the dispatcher authorization.
Then verify:

```sh
# Target Ubuntu host — ubuntu
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

SIGTERM closes admission, drains accepted requests, cleans up through normal
runtime paths, validates the durable tail, then removes the private socket.
`TimeoutStopSec=30s` and `KillMode=mixed` give Runner time to clean its Bash
children before systemd enforces the boundary.

A new physical Ubuntu machine needs its own configuration, state, service,
certificate/principal or bridge materials, host-key pin, and P157 evidence. Do
not copy either accepted host's state or credentials and do not treat another
host's listener or successful test as evidence for it.

A matching `build_revision` proves only which service binary is live. It does
not replace direct-route, queued-bridge, mailbox terminal-response, or B008-P6
end-to-end evidence.
