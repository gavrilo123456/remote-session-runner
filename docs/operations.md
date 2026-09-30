# Operations runbook

Use these checks to understand the health of the running PoC. Run Mac commands
as `tomasz.walczuk` and Ubuntu commands as `ubuntu`. Do not print private keys,
manually edit SQLite databases, or use host-gate test scripts as a routine
service-control mechanism.

## What is currently available

| Capability | State | Evidence to require before use |
| --- | --- | --- |
| Mac local execution | Available when both Mac LaunchAgents are healthy | Mac private socket health and a local test command. |
| Direct Ubuntu execution | Available when `runnerd.service` and public mTLS health are healthy | Public mTLS `/health/ready`, then a direct `linux-poc` command. |
| Queued Ubuntu execution | Not configured by default | A reviewed restricted SSH bridge deployment and an explicit queued-route test. Direct mTLS success does not prove it. |
| Local file mailbox | Available with healthy `runner-local` | Marker-last exchange, terminal response, event file, and ACK. |
| Queued remote mailbox | Depends on the queued SSH bridge | Same bridge and a new queued mailbox request. |

## Fast health checks

### Mac - `tomasz.walczuk`

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
launchctl print "gui/$(id -u)/com.remote-session-runner.local"
launchctl print "gui/$(id -u)/com.remote-session-runner.locald"
curl --silent --show-error --fail --unix-socket "$root/run/local-api.sock" http://runner/health/ready
curl --silent --show-error --fail --unix-socket "$root/run/locald.sock" http://runner/health/ready
```

Inspect the current Mac service logs:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
tail -n 200 "$root/logs/local.stderr.log"
tail -n 200 "$root/logs/locald.stderr.log"
```

### Ubuntu - `ubuntu`

```sh
root='/home/ubuntu/.local/share/remote-session-runner'
sudo systemctl is-active runnerd.service
curl --silent --show-error --fail --unix-socket "$root/run/runnerd.sock" http://runner/health/ready
ss -lntH | grep '10.0.0.200:8443'
sudo journalctl -u runnerd.service -n 200 --no-pager
```

If the optional queued route is enabled, verify its permanent bridge separately:

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/ssh/install-queued-bridge.sh status
```

This check does not change the bridge authorization or durable configuration.
It validates the selected `ubuntu` account, active enabled service, owner-only
socket and files, pinned dispatcher fingerprint, exact restricted
authorization, controller map, wrapper, and source revision. A regular
`deploy/linux/install-systemd-service.sh` deployment refreshes the bridge only
when this permanent route is already configured. It preflights the permanent
identity, uses the read-only zero-active-work gate before restarting an active
service, then refreshes after the new private socket is ready. It never changes
the dispatcher key.

### Public direct path - Mac

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

The direct HTTPS readiness result is an application check. A previous
temporary TLS probe was only a transport check and must not be treated as
proof that Runner was running.

## Diagnostics

Run a doctor command when diagnosing configuration, paths, or storage:

```sh
# Mac - tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
"$root/bin/runner-local" doctor --config "$root/config/mac.yaml"
"$root/bin/runner-locald" doctor --config "$root/config/mac.yaml"

# Ubuntu - ubuntu
root='/home/ubuntu/.local/share/remote-session-runner'
"$root/bin/runnerd" doctor --config "$root/config/linux.yaml"
```

A doctor command writes a timestamp-only SQLite health-probe record. It is a
safe diagnostic but not a read-only inspection.

For a controlled Ubuntu restart gate, the checked-in status test reports
active sessions, running commands, unreleased slots, and unfinished jobs:

```sh
cd /home/ubuntu/projects/remote-session-runner
make test-p128-host-status
```

Run controlled host-gate tests only with an appropriate maintenance plan; they
are evidence tools, not general service commands.

## Metrics and thresholds

Runner exposes `GET /health/live`, `GET /health/ready`, and `GET /metrics`.
Mac ingress and local execution expose metrics over owner-only Unix sockets.
Linux exposes them over its owner-only Unix socket and public direct HTTPS;
the HTTPS listener requires TLS 1.3 mTLS.

`/metrics` returns bounded JSON counters and gauges. The same metric object
is included in successful doctor output and readiness reports. It contains no
session or command IDs, user labels, paths, scripts, output, or credentials.

| Metric | Meaning | Warning threshold |
| --- | --- | ---: |
| `active_session_slots` | Session capacity reservations whose runtime cleanup is not confirmed | 16 |
| `active_command_slots` | Command execution slots whose stop is not confirmed | 4 |
| `queued_commands` | Authoritative commands still queued | 16 |
| `queued_intents` | Local intents recorded, dispatching, or uncertain | 32 |
| `dispatch_attempts_total` | Sum of recorded local-intent dispatch attempts | Reported; no absolute-total warning |
| `reconciliation_age_seconds` | Age of the oldest intent with uncertain delivery | 300 seconds |
| `event_lag_events` | Final remote event sequence minus the last locally mirrored sequence | 32 |
| `event_gaps_total` | Durable remote event gaps | 1 |
| `output_truncations_total` | Durable output truncation events | 1 |
| `storage_errors_total` | SQLite engine failures observed by this daemon process | 1 |
| `cleanup_failures_total` | Runtime or mailbox cleanup failures observed by this process | 1 |
| `mailbox_backlog` | Accepted mailbox exchanges; on Mac ingress, also safe ready markers not yet imported | 32 |

Daemons sample metrics at startup and every 30 seconds; health requests also
check thresholds. A warning is logged when a threshold is first reached and an
informational record is logged when it clears. Process-local storage and
cleanup counters reset after that daemon restarts. Retention cleanup can lower
historical dispatch, event-gap, and truncation counts.

A healthy Mac ingress proves only Mac ingress readiness. It does not make the
optional queued remote route available.

## Service lifecycle

### Refresh Mac services

Run on the **Mac** while important sessions are closed:

```sh
cd /Users/tomasz.walczuk/projects/remote-session-runner
deploy/macos/install-launchagents.sh
```

The installer rebuilds the binaries and replaces/restarts both LaunchAgents.
To stop them explicitly, use the current GUI UID:

```sh
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.local.plist"
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.locald.plist"
```

### Refresh Ubuntu service

Run on **Ubuntu** only after its checkout is fast-forwarded to the intended
Mac commit and active work has reached zero:

```sh
cd /home/ubuntu/projects/remote-session-runner
deploy/linux/install-systemd-service.sh
sudo systemctl is-active runnerd.service
```

The normal installer deploys and starts `runnerd`; when it replaces an active
service, it runs the checked-in read-only zero-active-work gate before the
build and again immediately before restart so the new binary is actually in
use. Schedule this during a maintenance window: the gate is a point-in-time
check, and a request admitted after it is handled by the service's graceful
shutdown, which stops admission, drains, or cancels remaining work truthfully.
When the restricted SSH bridge has already been enabled, the installer also
verifies its source checkout and refreshes the bridge program and forced-command
wrapper. It does not create or change the dispatcher authorization; use the
explicit bridge enable command in the setup guide for that one-time action.

## Triage guide

| Symptom | Check | Correct response |
| --- | --- | --- |
| `runner endpoint profile is unavailable or invalid` | Confirm owner-only Mac config exists at `config/mac.yaml`, then check its exact selected `linux-poc` profile and secret references. | Run the Mac installer once to create a missing config, review/provision the external files, then rerun it. Do not weaken file modes. |
| Mac local socket unavailable | `launchctl print`, Mac socket health, and `local*.stderr.log`. | Repair the LaunchAgent/config cause; rerun the Mac installer after review. |
| Direct HTTPS or mTLS failure | Run the public curl health check; inspect Ubuntu service state and journal. | Check CA, client certificate, private-key mode, principal map, server certificate, listener bind, and network path. Do not print keys. |
| Queued remote remains recorded/uncertain or reports stale view | Check whether the bridge is intentionally absent; then inspect the pinned host key, forced command, controller map, and `runnerd.service`. | Preserve the idempotency key and use the same route to observe reconciliation. Do not resend with a new key. |
| Mailbox request has no response | Verify JSON was closed before an empty matching `.ready`, correct ID/mode, and Mac logs. | Correct malformed drafts or create a new valid exchange; unsafe/malformed pairs have no guaranteed outbox response. |
| Command output incomplete | Inspect `output_complete`, `output_truncated`, cursor, and `output_unavailable_reason`. | Save available output; do not claim full output if retention or gaps prevent it. |
| Command cancellation accepted | Read events/status on the same endpoint. | Acceptance is a request, not proof of final cancellation. |

## Recovery boundaries

- Do not manually alter `local.db` or `remote.db`.
- The `backups/` directories are reserved for the tested backup/restore
  implementation, but the PoC has no supported production backup scheduler,
  backup CLI, or live restore runbook. Do not use `cp` to copy a live SQLite
  database.
- A restore does not reattach old runtime processes; affected running work is
  marked lost and reconciliation must finish before new dispatch.
- Graceful service shutdown stops new admission, drains for a bounded period,
  then uses normal cancellation/close cleanup. Clients resume retained events
  after their last validated sequence.
- After a Mac `runner-local` restart, accepted queued remote one-off jobs are
  recovered by read-only job, command, and event queries. The Router never
  resends the `run` mutation during this recovery. It publishes a terminal
  mailbox result only after the remote identity, target context, script digest,
  teardown, and complete or explicitly incomplete event boundary agree.
  Contradictory event history, including any event after a terminal event,
  remains blocked for investigation instead of being rendered as success.
  This recovery repairs missing derived files only. It preserves an already
  published immutable legacy response; do not remove it during restart
  recovery because its shared event file may still be retained for another
  response.
- Software-crash recovery is the approved durability claim. Physical power
  loss has not been tested, so it remains an unverified condition. A real
  power-off test requires a coordinated maintenance window and disposable
  representative state.

For deployment details, see [setup](setup.md). For user operations, see the
[CLI guide](user-guide.md) and [mailbox guide](mailbox.md).
