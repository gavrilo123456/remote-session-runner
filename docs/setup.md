# Setup runbook

Use this runbook to install or refresh the selected two-host PoC. Make
versioned changes in the Mac checkout, commit and push them, then fast-forward
the Ubuntu checkout before building or installing there. Keep all runtime
configuration, keys, certificates, database files, and logs outside Git.

## Before you start

| Requirement | Mac | Ubuntu |
| --- | --- | --- |
| Selected account | `tomasz.walczuk` | `ubuntu` (UID `1001`) |
| Checkout | `/Users/tomasz.walczuk/projects/remote-session-runner` | `/home/ubuntu/projects/remote-session-runner` |
| Go toolchain | `.../toolchains/go1.27.1/bin/go` below the Mac service root | `.../toolchains/go1.27.1/bin/go` below the Ubuntu service root |
| Service manager | GUI launchd | systemd with noninteractive `sudo` for unit installation |
| Direct remote transport | Direct client CA, certificate, and private key | Server certificate/key, trusted client CA, principal map |

The Linux host must have the configured address `10.0.0.200:8443`; clients use
the public URL `https://129.151.232.40:8443`. The listener binds to the former,
not the latter.

The deployment scripts intentionally do not download a compiler. Provision the
selected verified Go 1.27.1 toolchain at the exact service-root path before
installing a service, then verify it on each host:

```sh
# Mac - tomasz.walczuk
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" version
# Expected platform suffix: darwin/arm64

# Ubuntu - ubuntu
"/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go" version
# Expected platform suffix: linux/amd64
```

## 1. Synchronize source before a host deployment

Run these commands only after reviewing and committing the intended change.

### Mac - `tomasz.walczuk`

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
git status --short --branch
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git rev-parse HEAD
```

### Ubuntu - `ubuntu`

Only pull into a clean `dev` checkout. Do not edit tracked Runner files on
Ubuntu and do not copy source files there.

```sh
cd /home/ubuntu/projects/remote-session-runner
git status --short --branch
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
git rev-parse HEAD
```

Compare the two commit IDs before an Ubuntu build or test. If either worktree
is dirty or the pull cannot fast-forward, stop and resolve that Git state first.

## 2. Install the Mac services

### Mac - `tomasz.walczuk`

The first installer run creates the selected config file then intentionally
stops for review:

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

Review the created file:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml
```

Before the second run, provision these owner-only regular files beneath the
Mac service root. Do not print their contents:

```text
secrets/poc-ca.pem
secrets/direct-client.pem
secrets/direct-client.key
secrets/dispatcher_ed25519
secrets/ssh_known_hosts
```

Set config and secrets to mode `0600`, then rerun the installer:

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

It builds `runner`, `runner-local`, and `runner-locald`, installs two
LaunchAgents, and restarts them in the current GUI user domain. Schedule a
refresh while no important work is running, because shutdown first drains then
cancels or closes remaining work within its bounded shutdown window.

Verify the two services and their private health endpoints:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
launchctl print "gui/$(id -u)/com.remote-session-runner.local"
launchctl print "gui/$(id -u)/com.remote-session-runner.locald"
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" http://runner/health/ready
```

## 3. Install the Ubuntu service

### Ubuntu - `ubuntu`

Before running the Linux installer, provision these regular owner-only files
with mode `0600`:

```text
/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml
/home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
/home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
/home/ubuntu/.local/share/remote-session-runner/secrets/server.key
/home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
```

Use the selected values in [configuration](configuration.md). All service
directories must be owned by `ubuntu` and mode `0700`.

Install from the synchronized checkout:

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/linux/install-systemd-service.sh
sudo systemctl status runnerd.service --no-pager
```

The installer builds `runnerd`, verifies its systemd unit, and enables the
service. When upgrading an already active instance, it runs the checked-in
read-only zero-active-work gate before the build and again immediately before
restart, then restarts the service itself so the new binary is in use. It
refuses the upgrade if either gate finds live sessions, commands, unreleased
slots, or unfinished jobs. Schedule this during a maintenance window because
the gate is not an admission fence; a request accepted after it is handled by
the normal graceful shutdown path and has a truthful durable outcome.

Verify the private socket and expected listener:

```sh
root='/home/ubuntu/.local/share/remote-session-runner'
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" http://runner/health/ready
ss -lntH | grep '10.0.0.200:8443'
```

## 4. Verify direct mTLS from the Mac

### Mac - `tomasz.walczuk`

This checks the running Runner application through the public endpoint while
keeping private-key contents private:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

A successful result proves the Runner readiness endpoint, mTLS identity, and
public path at that moment. It does not configure the optional queued SSH
route.

## Optional: enable the queued SSH route

The queued route is opt-in. It uses a permanent, restricted SSH bridge on
Ubuntu and the existing owner-only Mac dispatcher key. It is separate from the
direct mTLS route: a successful public HTTPS check does not make queued CLI or
mailbox work available.

Perform these steps only when this route is required and the Ubuntu service is
already healthy.

### Mac - `tomasz.walczuk`: install the permanent bridge

The command derives and streams only the dispatcher **public** key. It does
not print or copy the private key:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
ssh-keygen -y -f "$root/secrets/dispatcher_ed25519" |
  ssh -F /dev/null \
    -i /Users/tomasz.walczuk/.ssh/remote-session-runner \
    -o IdentitiesOnly=yes \
    -o BatchMode=yes \
    -o StrictHostKeyChecking=yes \
    -o GlobalKnownHostsFile=/dev/null \
    -o 'UserKnownHostsFile="/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts"' \
    ubuntu@129.151.232.40 \
    'cd /home/ubuntu/projects/remote-session-runner && deploy/ssh/install-queued-bridge.sh enable --dispatcher-public-key-stdin'
```

The installer refuses a different or duplicate dispatcher identity, unsafe
paths or modes, a partial earlier installation, a dirty Ubuntu checkout, and a
missing active `runnerd.service` socket. It creates these Ubuntu-owned files:

```text
/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge
/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge-forced.sh
/home/ubuntu/.local/share/remote-session-runner/config/ssh-controller-map.yaml
/home/ubuntu/.local/share/remote-session-runner/config/queued-ssh-dispatcher.pub
/home/ubuntu/.local/share/remote-session-runner/config/queued-ssh-bridge.manifest
```

It records one exact `restrict,command=...` key in
`/home/ubuntu/.ssh/authorized_keys` only after the bridge files have been
staged and validated. The bridge permits only `runner-ssh-bridge --stdio`,
forwards only to `runnerd.sock`, and does not create a general remote shell.

### Ubuntu - `ubuntu`: verify the permanent route

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

`status` does not change the bridge authorization or durable configuration. It
prints the public-key fingerprint and artifact hashes, never private-key
material. It verifies the selected account, service, socket, path modes, one
exact restricted key line, controller map, and current source revision.

Future regular Ubuntu deployments refresh the bridge automatically:

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/linux/install-systemd-service.sh
deploy/ssh/install-queued-bridge.sh status
```

The Linux installer calls `refresh` only when the permanent manifest already
exists. Before it replaces `runnerd`, it preflights the existing bridge
identity and runs the read-only zero-active-work gate before restarting an
active service, including an immediate pre-restart repeat; after the new
`runnerd` has created its owner-only socket, refresh updates the bridge binary
and fixed wrapper for the checked-out revision. It does not add, replace, or
rotate the dispatcher authorization. The Ubuntu checkout must be clean `dev`
with `HEAD` equal to `origin/dev`; identity mismatch or partial state fails
closed for inspection.

### Mac - `tomasz.walczuk`: confirm the queued route

```sh
runner='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner'
"$runner" --endpoint local run \
  --environment linux-dev \
  --target remote \
  --profile linux-host \
  -- 'id -un && hostname'
```

The output must identify `ubuntu`. If it fails, leave the request and service
state intact for diagnosis rather than relaxing SSH restrictions. The direct
route remains usable independently.

## Confirm installed versions

### Mac - `tomasz.walczuk`

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
git rev-parse HEAD
```

### Ubuntu - `ubuntu`

```sh
cd /home/ubuntu/projects/remote-session-runner
git rev-parse HEAD
```

The commit IDs must match before remote testing. For everyday use, continue
with the [CLI user guide](user-guide.md) or [mailbox guide](mailbox.md).
