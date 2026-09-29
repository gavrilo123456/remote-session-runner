# Linux systemd deployment

`runnerd.service` runs the remote execution authority and mandatory-mTLS HTTPS
listener as the selected `ubuntu` account. It binds the configured host-local
address `10.0.0.200:8443`; clients use the public endpoint
`https://129.151.232.40:8443`. No tunnel or Podman service is involved.

On Ubuntu, from the synchronized project checkout, run this as `ubuntu`:

```sh
deploy/linux/install-systemd-service.sh
```

The installer requires the selected Go 1.27.1 toolchain and the preprovisioned
owner-only `config/linux.yaml`, `config/client-principals.yaml`, server key and
certificate, and client CA. It never reads or prints private key contents. It
builds `runnerd` under the external service root, sets its service directories
to mode 0700 after verifying their ownership and rejecting symlinks, installs
the unit, verifies it with `systemd-analyze`, then enables and starts it. When
updating an active service, it first runs the checked-in read-only
zero-active-work gate and repeats it immediately before restart so the new
binary is used. Schedule this during a maintenance window: the gate is a
snapshot, and a request accepted after it is handled through the normal
graceful shutdown path with a truthful durable outcome. Existing runtime
configuration and database files are not copied from the repository or
replaced.

The unit sets `User=ubuntu`, `Group=ubuntu`, and `UMask=0077`. Its entrypoint
checks the account, service-directory ownership/modes, launcher and runnerd
binaries, config, and mTLS file modes before launching `runnerd`. A path check
failure exits with a status that prevents an automatic restart loop. The unit
does not add a filesystem sandbox to the host-process profile: remote commands
retain the selected `ubuntu` account's OS permissions, and a workspace remains
a starting directory rather than confinement.

On Ubuntu, inspect the systemd service with:

```sh
sudo systemctl status runnerd.service
sudo systemctl stop runnerd.service
```

Use `deploy/linux/install-systemd-service.sh` for an update: it performs the
two zero-active-work checks before restarting an active service. If a manual
restart is required for an operational reason, first run the same check during
a maintenance window:

```sh
make test-p128-host-status
sudo systemctl restart runnerd.service
```

SIGTERM closes admission on both APIs, waits for accepted requests and command
dispatch, closes any remaining Linux sessions through the normal runtime stop
path, verifies the durable audit/operational tail, then closes event followers
and removes the runnerd-owned Unix socket. The coordinator allows an 8-second
drain and up to 4 seconds for each cleanup stage; `TimeoutStopSec=30s` leaves
systemd time to finish the process-group cleanup. `KillMode=mixed` sends the
initial stop signal to runnerd so it can cancel its Bash sessions; systemd
still kills remaining service processes after shutdown or the deadline. The
P132 host gate exercises this path with an active command. Forced-command SSH
setup is documented in [the SSH deployment guide](../ssh/README.md), and must
use this active service's private socket.

If the optional permanent queued bridge has already been enabled, this installer
preflights its selected dispatcher identity before replacing `runnerd`. Once
the zero-active-work gates permit an active-service restart and the new service
has recreated its owner-only socket, it refreshes the bridge binary and fixed
wrapper from the same clean synchronized `dev` revision. It does not create,
change, or rotate the SSH authorization. Run
`deploy/ssh/install-queued-bridge.sh status` after the deployment to confirm
the bridge remains ready.
