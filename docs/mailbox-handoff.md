# Reversible PRD-to-QAS mailbox handoff

This owner-scoped Mac maintenance operation moves the existing physical mailbox
paths to QAS without transferring a database, retiring a root or upgrading PRD.
It is separate from release promotion and does not complete CAT-06.

Stop all affected mailbox clients before starting. They must reopen their files
after activation: the root path is preserved, while its five stream directories
are archived and replaced. Do not publish a request or ACK to drive maintenance.
Use the explicitly owner-authorized Mac shell and supported maintenance tools.

## What changes

- PRD's raw configuration is backed up, then its active mailbox map becomes
  empty with `mac.mailbox_ingress_enabled: false`. Its durable registry and
  retained work remain untouched. Old PRD binaries cannot parse this new flag;
  the coordinator stops the Mac PRD agents and leaves them stopped. Restore the
  original configuration before starting the original PRD binaries.
- QAS keeps its own database, credentials, default mailbox and worker profiles.
  The transferred registrations preserve aliases and allowed targets, mapped to
  the corresponding QAS contexts. The old PRD default is named `prd-default`
  inside QAS, avoiding a collision with QAS's existing `default` registration.
- Each original `inbox`, `outbox`, `events`, `acks` and `diagnostics` directory
  moves intact to `.runner-mailbox-handoffs/OPERATION/prd/` under that root.
  Nothing is replayed, acknowledged, released or deleted. Original directory
  identities and contents are preserved; new stream directories use mode 0700.
- An owner-only receipt binds the target configuration hash and physical roots.
  Exclusive consumer leases, root markers and per-cycle guards prevent an
  unqualified or second consumer. A transfer is temporary receipt authority;
  QAS does not permanently register the transferred roots in its native registry.
- Historical request IDs, ACK identities and hashed idempotency keys are fenced.
  Explicit old environment names map to QAS only for transferred mailboxes and
  only through the approved target policies. The original explicit environment
  stays in the canonical request receipt; resolved QAS selection is recorded
  separately. Archived source files keep their original bytes. Old PRD sessions
  are not migrated into QAS.

The QAS runtime and its matching config parser must be qualified and installed
before activation. This feature does not open general PRD catalog installation.
PRD binaries, selectors, keys and SQLite data are not replaced by the handoff.

## Supported sequence

All commands below execute on **Mac**, as `tomasz.walczuk`. `TOOL` denotes the
qualified native `runner-mailbox-handoff` artifact; use its recorded exact hash,
not an unrelated temporary helper or a newly rebuilt replacement.

```sh
# Mac: after fresh read-only work checks, stop the selected Mac consumers.
# PRD terminal-lost reservations are retained; no active execution is allowed.
"$LANE_TOOL" prd-stop --host mac-control-plane
"$LANE_TOOL" stop --host mac-control-plane --lane qas

# Mac: confirm both process trees and private listeners are stopped, then back up.
"$TOOL" prepare --owner 'Tomasz Walczuk' --publishers-paused

# Mac: use the exact operation.json path returned by prepare.
"$TOOL" status --operation-file "$OPERATION_FILE"
"$TOOL" stop --operation-file "$OPERATION_FILE" --publishers-paused
"$TOOL" activate --operation-file "$OPERATION_FILE" --publishers-paused

# Mac: start only QAS through the qualified, supported lane controller.
"$LANE_TOOL" start --host mac-control-plane --lane qas
```

Preparation writes a private operation directory under PRD's existing `backups`
root, with the original configs, staged configs, consistent online SQLite
backups and durable operation journal. It requires an already registered native
QAS mailbox baseline. All source targets must have a corresponding QAS target.
Both executors hold their lifecycle locks while running, so preparation follows
the supported initial stops above. Do not remove or bypass those locks. The
handoff `stop` action is an idempotent recheck after preparation and the supported
pause when returning from active QAS ownership. Preserve a hashed copy of the
qualified operator tools with the persistent operation backup for later return.

Stopping refuses active sessions, queued/running commands, resumable jobs or
pending recovery finalizations. Existing terminal-lost PRD reservations are
preserved; this operation neither repairs them nor treats them as running work.
QAS must have a fully empty work inventory. Activation requires both selected
Mac consumers stopped, exact expected configuration hashes and unchanged root
identities, under the existing catalog and lifecycle locks.

Verify QAS via its private `/health/ready` socket and confirm the exclusive
ownership markers and actual execution context before resuming clients. A
concurrent `runner-local doctor` constructs another consumer and therefore
cannot take an active handoff lease; use the private health endpoint instead.
Readiness is distinct from mailbox request/event/result/ACK acceptance. Report
mailbox acceptance **NOT RUN** unless that exact exchange has been authorized
and observed.

## Interruption and return

If activation stops partway through, keep clients and both Mac consumers
stopped. Inspect the same operation and repeat `activate`; do not edit receipts,
copy configs by hand, delete archives or start the original PRD config early.
Retries validate mixed original/candidate config states, recover recorded rename
gaps and union archived/live historical identities before sealing ownership.

To return to the original PRD installation, first stop clients and settle and
ACK all new QAS mailbox exchanges. Then, on **Mac**:

```sh
"$TOOL" stop --operation-file "$OPERATION_FILE" --publishers-paused
"$TOOL" restore --operation-file "$OPERATION_FILE" --publishers-paused
"$LANE_TOOL" prd-start --host mac-control-plane
"$LANE_TOOL" start --host mac-control-plane --lane qas
```

Return refuses unacknowledged QAS exchanges. It archives QAS's stream directories
under the same operation's `qas/` epoch, restores the exact original PRD stream
directories and both original raw configurations, then removes only this
operation's ownership markers. QAS's database and its acknowledged history stay
in place. No database restore, authority merge or manual capacity release occurs.

The return package targets the original configuration hashes. If a subsequent
PRD upgrade changes its configuration, prepare and validate the corresponding
return package as part of that upgrade; this tool refuses to overwrite unknown
configuration bytes. Never reinterpret a configuration change as a database
rollback. New client work always needs fresh request IDs and idempotency keys.
