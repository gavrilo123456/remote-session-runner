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
the unit, verifies it with `systemd-analyze`, then enables and starts it.
Existing runtime configuration and database files are not copied from the
repository or replaced.

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
sudo systemctl restart runnerd.service
sudo systemctl stop runnerd.service
```

SIGTERM closes the HTTP listeners and removes the runnerd-owned Unix socket so
systemd can restart cleanly. The lifecycle smoke uses an idle service; it does
not prove safe shutdown of active sessions or commands. P130/P132 add the
shared command drain/cancellation coordinator. Forced-command SSH setup is
documented in [the SSH deployment guide](../ssh/README.md), and must use this
active service's private socket.
