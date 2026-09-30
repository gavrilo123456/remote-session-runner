# Operations runbook

Run Mac commands as `tomasz.walczuk` and commands on either accepted Ubuntu
host as `ubuntu`. Do not print private keys, edit SQLite files, manufacture
mailbox request files, or use host-gate test scripts as everyday service
controls.

## Current availability and evidence boundary

| Capability | Current state | What to verify before use |
| --- | --- | --- |
| Mac local execution | Available when both Mac LaunchAgents are healthy | Both private readiness endpoints and a safe local command. |
| Direct `linux-host` execution | Accepted on the current host | Public mTLS readiness, then a direct `linux-poc` command. |
| Queued `linux-host` execution | Permanent restricted bridge was accepted in P155 | Fresh bridge `status` at the deployed source revision, then a queued command. |
| Direct `sandbox-host` execution | Accepted in P157 | Public `sandbox-poc` mTLS Runner readiness. |
| Queued `sandbox-host` execution | Accepted in P157 | Fresh sandbox bridge `status`; `analytics` is the only mailbox inbox that permits its override. |
| `default` mailbox | Installed and accepted in P155 | Mac readiness, configured root ownership, and native terminal response/event/ACK. |
| `analytics` mailbox | Installed in P155; `sandbox-host` override accepted in P157 | Same checks; each remote override additionally needs its selected bridge status. |
| Any additional profile | **NOT RUN** | Its own P157 service, route, and end-to-end acceptance. |

P155 proved `default` local-default work and an `analytics` allowed queued
`linux-host` override. P157 independently proved the sandbox bridge, router
health, native mailbox request, event read, ACK, and zero-work state for
`sandbox-host`. A successful direct mTLS request does not prove queued mailbox
delivery.

## Fast health checks

### Mac — `tomasz.walczuk`

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
launchctl print "gui/$(id -u)/com.remote-session-runner.local"
launchctl print "gui/$(id -u)/com.remote-session-runner.locald"
test -S "$root/run/local-api.sock"
test -S "$root/run/locald.sock"
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" http://runner/health/ready
```

The `runner-local` report contains a `remote_router` check for one queued
profile. With several queued profiles it reports `remote_router/<profile>` per
profile. `ready` means the periodic read-only bridge ping reached that
configured bridge and Runner service. `degraded` means local mailbox ingress
can still be ready, while that queued profile is not currently proven
routable.

Inspect active V2 policy without changing it:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
"$root/bin/runner-local" validate-config --config "$root/config/mac.yaml"
(
  set -e
  check_mailbox_tree() {
    mailbox=$1
    test -d "$mailbox/inbox"
    test -d "$mailbox/outbox"
    test -d "$mailbox/events"
    test -d "$mailbox/acks"
  }
  check_mailbox_tree "$root/mailbox"
  check_mailbox_tree "$root/mailboxes/analytics"
)
```

`validate-config` checks file safety and policy only when invoked without
activation flags. It does not make a candidate active. Use the V2 candidate
procedure in [setup](setup.md#3-upgrade-to-version-2-or-add-an-inbox) for a
policy change.

Mac logs:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
tail -n 200 "$root/logs/local.stderr.log"
tail -n 200 "$root/logs/locald.stderr.log"
```

### `linux-host` Ubuntu — `ubuntu`

```sh
root='/home/ubuntu/.local/share/remote-session-runner'
sudo systemctl is-active runnerd.service
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" http://runner/health/ready
ss -lntH | grep '10.0.0.200:8443'
sudo journalctl -u runnerd.service -n 200 --no-pager
```

Verify the permanent bridge separately:

```sh
# Current Ubuntu — ubuntu
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

`status` checks account, enabled service, private socket, modes, pinned
identity, exact restricted authorization, controller map, wrapper, and checked
out source revision. It does not alter bridge authorization or durable
configuration. If the source revision advanced, use the normal Linux deployer
in a maintenance window so it refreshes the bridge artifact. Do not treat an
old `ready` result as evidence for a later checkout.

### `sandbox-host` Ubuntu — `ubuntu`

Run these commands through the selected bootstrap alias for the sandbox host:

```sh
# Sandbox Ubuntu — ubuntu
root='/home/ubuntu/.local/share/remote-session-runner'
sudo systemctl is-active runnerd.service
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" http://runner/health/ready
ss -lntH | grep '10.0.0.14:8443'
sudo journalctl -u runnerd.service -n 200 --no-pager
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

### Accepted public direct paths — Mac

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

For `sandbox-host`, substitute `sandbox-direct-client.pem`,
`sandbox-direct-client.key`, and `https://132.226.205.205:8443/health/ready`.
Each check covers its selected Runner application, public route, mTLS identity,
and readiness. The earlier temporary TLS probe was transport-only. Neither
check confirms a queued bridge or a mailbox request.

## Diagnostics and metrics

Use a doctor command for configuration, paths, or storage diagnosis:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
"$root/bin/runner-local" doctor --config "$root/config/mac.yaml"
"$root/bin/runner-locald" doctor --config "$root/config/mac.yaml"

# Selected Ubuntu host — ubuntu
root='/home/ubuntu/.local/share/remote-session-runner'
"$root/bin/runnerd" doctor --config "$root/config/linux.yaml"
```

A doctor writes a timestamp-only SQLite health record, so it is diagnostic and
not read-only.

`/metrics` is available through each private socket and the mTLS HTTPS
listener. It includes bounded counters and no scripts, paths, output,
credentials, or resource IDs.

| Metric | Meaning | Warning threshold |
| --- | --- | ---: |
| `active_session_slots` | Session capacity reservations awaiting cleanup confirmation | 16 |
| `active_command_slots` | Command slots awaiting stop confirmation | 4 |
| `queued_commands` | Authoritative commands still queued | 16 |
| `queued_intents` | Local intents recorded, dispatching, or uncertain | 32 |
| `reconciliation_age_seconds` | Oldest uncertain intent | 300 seconds |
| `event_lag_events` | Remote final sequence minus locally mirrored sequence | 32 |
| `event_gaps_total`, `output_truncations_total` | Durable retained-output problems | 1 |
| `storage_errors_total`, `cleanup_failures_total` | Observed process/database cleanup errors | 1 |
| `mailbox_backlog` | Aggregate durable accepted exchanges plus safely published ready markers | 32 |
| `mailbox_backlog_by_inbox` | Same backlog, split by configured safe inbox IDs such as `default` and `analytics` | Inspect each nonzero value |

A zero backlog does not prove an importer, bridge, or request succeeded. It
only shows no current counted work. Process-local error counters reset after a
daemon restart; retained counters can fall after cleanup.

For a controlled accepted-host restart gate, run this on the selected Ubuntu
host:

```sh
# Selected Ubuntu host — ubuntu
cd /home/ubuntu/projects/remote-session-runner
make test-p128-host-status
```

This evidence test reports active sessions, running commands, unreleased slots,
and unfinished jobs. Use it in a maintenance plan; it is not a general
service-control command.

## Refresh services

### Mac — `tomasz.walczuk`

For an unchanged active policy:

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

For mailbox, context, or route policy changes, use an owner-only V2 candidate
and `install-launchagents.sh --config <mac.next.yaml>` as described in
[setup](setup.md#3-upgrade-to-version-2-or-add-an-inbox). Do not modify active
`mac.yaml`, remove a registered inbox, or attempt a V1 rollback after a V2
activation boundary.

To unload the services explicitly:

```sh
# Mac — tomasz.walczuk
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.local.plist"
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.locald.plist"
```

### Selected Ubuntu host — `ubuntu`

First prove its checkout is the pushed Mac commit, then deploy during a quiet
window:

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/linux/install-systemd-service.sh
sudo systemctl is-active runnerd.service
deploy/ssh/install-queued-bridge.sh status
```

The normal installer repeats the zero-active-work check before restart and
refreshes an already enabled permanent bridge after the new private socket is
ready. It does not create or rotate dispatcher authorization.

## Triage guide

| Symptom | Check | Correct response |
| --- | --- | --- |
| `runner endpoint profile is unavailable or invalid` | Active config, endpoint name, and V2 direct endpoint definition | Review the owner-only config and its secret paths; use candidate activation for changes. |
| `endpoint/target mismatch` | Direct endpoint's bound target profile | Choose the endpoint that is configured for that exact remote profile; do not retry a mutation with a changed target. |
| Partial mailbox selection | Request has only `environment` or `execution_target` | Submit neither to use the inbox default, or submit the complete allowed pair. |
| Mailbox selection or alias rejected | Root, context allow-list, repository alias, response `inbox_id` | Use the intended root and its configured policy; do not invent aliases or host names. |
| No mailbox response | Native client error/logs, root tree, response state | Keep the request identity, inspect owner/mode/path failure, and use `mailboxclient`; malformed unsafe pairs have no guaranteed response. |
| Direct HTTPS/mTLS failure | Public health, service journal, CA/certificate/principal map/bind | Repair host configuration without printing keys. |
| Queued remote remains recorded, uncertain, or stale | Selected bridge `status`, host-key pin, wrapper, controller map, `runnerd.service` | Preserve the idempotency key and observe the same route; do not resend with a new key. |
| New host has no route | Its P157 record and per-host service/materials | Keep it `NOT RUN`; accepted `linux-host` and `sandbox-host` evidence does not transfer. |
| Command output incomplete | Cursor, `output_complete`, `output_truncated`, `output_unavailable_reason` | Save the available prefix and do not call it complete. |

## Recovery boundaries

- Do not manually edit `local.db` or `remote.db`, or copy a live SQLite file.
- `backups/` is reserved for the tested backup/restore implementation; this
  PoC has no public backup scheduler, backup CLI, or live-restore runbook.
- A restore does not reattach previous runtime processes. Lost work and
  reconciliation must finish before new dispatch.
- Graceful shutdown stops admission, drains for a bounded period, then uses
  normal cancellation/close cleanup. Clients resume retained events after
  their last validated sequence.
- After a Mac process restart, queued one-off recovery reads remote state and
  does not resend the mutation.
- Software-crash recovery is evidenced. Physical power-loss survival remains
  unverified until a coordinated physical power-cut test passes.

See [current-host evidence](current-host-evidence.md) for exact P155/P157 scope
and [setup](setup.md) for deployment steps.
