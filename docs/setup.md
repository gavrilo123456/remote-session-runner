# Setup and upgrade runbook

Use this runbook to install or refresh the controlled PoC. Versioned source,
tests, deployment files, and documentation are changed only in the Mac
checkout. Commit and push the Mac work first; then fast-forward the clean
Ubuntu checkout before any Ubuntu build, installation, or test. Runtime
configuration, credentials, logs, databases, and output stay outside Git.

## Before you start

| Requirement | Mac | Current Ubuntu host |
| --- | --- | --- |
| Account | `tomasz.walczuk` | `ubuntu` |
| Checkout | `/Users/tomasz.walczuk/projects/remote-session-runner` | `/home/ubuntu/projects/remote-session-runner` |
| Go toolchain | `.../RemoteSessionRunner/toolchains/go1.27.1/bin/go` | `.../remote-session-runner/toolchains/go1.27.1/bin/go` |
| Service manager | GUI launchd | systemd with noninteractive `sudo` for unit installation |
| Direct route | Client CA, certificate, and private key | Server certificate/key, trusted client CA, principal map |

The current Linux host binds `10.0.0.200:8443`; clients use
`https://129.151.232.40:8443`. These values apply only to `linux-host`.

```sh
# Mac — tomasz.walczuk
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" version

# Current Ubuntu — ubuntu
"/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go" version
```

Expected platform suffixes are `darwin/arm64` and `linux/amd64`. The deployment
scripts do not download Go.

## 1. Synchronize a versioned revision

Perform this after reviewing and committing the intended change.

### Mac — `tomasz.walczuk`

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
git status --short --branch
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git rev-parse HEAD
```

### Current Ubuntu — `ubuntu`

Only pull into a clean `dev` checkout. Do not edit tracked Runner files on
Ubuntu or copy source there.

```sh
cd /home/ubuntu/projects/remote-session-runner
git status --short --branch
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
git rev-parse HEAD
```

Compare the resulting commit IDs. If either checkout is dirty or the pull
cannot fast-forward, stop at that Git state. Do not reset, force-push, or run a
host deployment against an unpushed revision.

## 2. First Mac installation

### Mac — `tomasz.walczuk`

The first installer invocation creates the V1 compatibility config and stops:

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

Review the created owner-only file:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml
```

Before the second invocation, provision these regular owner-only files beneath
the Mac service root. Do not print their contents:

```text
secrets/poc-ca.pem
secrets/direct-client.pem
secrets/direct-client.key
secrets/dispatcher_ed25519
secrets/ssh_known_hosts
```

Set config and secret files to mode `0600`, then install:

```sh
# Mac — tomasz.walczuk
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

The installer builds `runner`, `runner-local`, and `runner-locald`, installs
two LaunchAgents, and starts them in the current GUI user domain. Use a quiet
window because service shutdown first drains and then cancels or closes bounded
remaining work.

Verify both services:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
launchctl print "gui/$(id -u)/com.remote-session-runner.local"
launchctl print "gui/$(id -u)/com.remote-session-runner.locald"
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" http://runner/health/ready
```

## 3. Upgrade to version 2 or add an inbox

### Mac — `tomasz.walczuk`

Use a reviewed candidate, not an in-place edit of the active config. Create
one only when it does not already exist:

```sh
candidate='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml'
if test -e "$candidate"; then
  printf '%s\n' 'Candidate already exists; review it instead of overwriting it.'
else
  cp deploy/macos/mac.v2.yaml.example "$candidate"
  chmod 600 "$candidate"
fi
```

Edit the candidate as the owner to include the complete V2 policy. The current
active P155 policy contains the legacy `default` root and the extra `analytics`
root; see [configuration](configuration.md#version-2-installed-multi-inbox-policy).
Keep every existing registered inbox ID/root unchanged. Do not write a
candidate over `config/mac.yaml`.

Activate the candidate:

```sh
# Mac — tomasz.walczuk
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh --config \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The upgrade sequence is deliberate:

1. validate the candidate while the old ingress is live;
2. quiesce the services and perform a final **read-only** retained-mailbox
   check;
3. enter the no-rollback activation boundary, register the candidate roots,
   hand off `mac.yaml`, and start the V2 services.

For a schema-24 database, the final pre-boundary check treats retained work as
implicit `default` without writing it. The post-boundary activation can migrate
to schema 27 and record the inbox registry. A V1 binary cannot safely reopen
that namespaced database. Before the boundary, a failed candidate restores the
prior services. After it, the staged candidate is retained for repair/re-run;
do not attempt to revive V1. The independently reviewed `mac.next.yaml`
remains after a successful install while a staged copy becomes active
`mac.yaml`.

Verify both V2 mailbox trees and the running service health before publishing
new work. Use the native mailbox-client integration in [the mailbox guide](mailbox.md),
not terminal-created request files.

## 4. Install or refresh the current Ubuntu service

### Current Ubuntu — `ubuntu`

Before first installation, provision these regular owner-only `0600` files:

```text
/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml
/home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
/home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
/home/ubuntu/.local/share/remote-session-runner/secrets/server.key
/home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
```

Install from the synchronized checkout:

```sh
# Current Ubuntu — ubuntu
cd /home/ubuntu/projects/remote-session-runner
deploy/linux/install-systemd-service.sh
sudo systemctl status runnerd.service --no-pager
```

On an active service, the installer runs the checked-in zero-active-work test
before build and immediately before restart. It refuses deployment if it sees
sessions, commands, unreleased slots, or unfinished jobs. This is a
point-in-time maintenance gate; normal graceful shutdown gives an honest
outcome to work accepted after the check.

Verify the private service and selected listener:

```sh
# Current Ubuntu — ubuntu
root='/home/ubuntu/.local/share/remote-session-runner'
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" http://runner/health/ready
ss -lntH | grep '10.0.0.200:8443'
```

## 5. Verify the direct Runner application route

### Mac — `tomasz.walczuk`

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

A successful result proves the **current** Runner readiness endpoint, mTLS
identity, and public path. The earlier temporary TLS probe proved transport
only. Neither result proves queued bridge routing or mailbox delivery.

## 6. Enable or refresh the queued bridge for `linux-host`

The queued route is a separate, permanent restricted SSH bridge. It is required
for `runner --endpoint local` remote work and mailbox remote overrides. It is
not a general SSH shell and is independent of direct mTLS.

### Current Ubuntu — `ubuntu`: verify or refresh

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

The status command is read-only with respect to bridge authorization and
persistent configuration. It checks the selected account, active service,
private socket, path modes, pinned dispatcher identity, exact restricted
`authorized_keys` entry, controller map, wrapper, and source revision. A
source checkout advance must be followed by the normal Linux deployment so the
bridge binary and manifest match the checked-out source revision.

For a first installation, follow the bridge guide's public-key streaming
procedure exactly:

```text
deploy/ssh/install-queued-bridge.sh enable --dispatcher-public-key-stdin
```

It accepts only the dispatcher **public** key, records a constrained forced
command, and never prints or transfers the private key. See
[deploy/ssh/README.md](../deploy/ssh/README.md) for the exact controlled
procedure.

### Mac — `tomasz.walczuk`: route smoke test

```sh
runner='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner'
"$runner" --endpoint local run \
  --environment linux-dev \
  --target remote \
  --profile linux-host \
  -- 'id -un && hostname'
```

The output must identify `ubuntu`. This is a queued route check. It is separate
from the direct mTLS health check.

## 7. Onboard each additional remote host (P157)

A new SSH alias, endpoint, or config profile is only a candidate. For example,
`sandbox.env` is supplied as a future Ubuntu candidate and is **NOT RUN**
because its P157 gate has not begun. Do not route ordinary work to it. A
reviewed candidate activation and one controlled safe request may be part of
P157; they do not make the host generally available until the gate passes.

For each new profile, complete a separate P157 evidence record:

1. create independent owner-only Linux configuration, state root, mTLS
   certificate/principal map or pinned bridge materials, and service;
2. add its exact environment, target profile, and `remote_hosts` route to a
   reviewed Mac V2 candidate. Add it to a mailbox allow-list only when it has
   a queued bridge; a direct-only profile is validated by its direct endpoint
   and CLI rather than a mailbox request;
3. activate that reviewed candidate for the controlled P157 test, and
   fast-forward the exact host's clean checkout before installing its service
   from that checkout;
4. prove `ubuntu`, the expected listener or restricted bridge, host-key pin,
   per-profile route health, and a safe end-to-end request selecting that exact
   target; and
5. record the evidence, including the target profile, without printing private
   keys or treating another host's acceptance as evidence.

A host that is unavailable or has not been attempted is `NOT RUN`. A required
gate that runs and fails is `FAIL`: stop, preserve the exact evidence, and do
not mark the profile available. It must never reuse `linux-host` certificates,
state database, bridge authorization, or proof.

For normal use after setup, follow the [CLI guide](user-guide.md) or the
native [mailbox guide](mailbox.md). The current accepted status is indexed in
[current-host evidence](current-host-evidence.md).
