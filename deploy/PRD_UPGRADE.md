# Existing-host PRD upgrade

**Catalog migration is not implemented by this runbook.** The selected
[Factory-style catalog design](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
and [CAT plan](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md)
are design/plan only. The commands below still operate the supported legacy
regular-file `bin` layout. CAT must deliberately adapt launch paths, selectors,
guards, backup and rollback before an approved catalog adoption; do not replace
the current layout with manual symlinks or relocate writable state.

This procedure operates the current unsuffixed PRD instance on one of the four
canonical machines. It does not select a Dev/QAS namespace, alter service
definitions or configuration, publish mailbox requests or ACKs, or install a
new host or volume. Run each command on the named Mac or Ubuntu host as the
existing Runner account. Use only the per-host bundle already accepted in Dev
and QAS; provide its exact recorded bundle SHA-256.

## Guarded command sequence

1. **Mac / selected Ubuntu host:** run `runner-lane prd-preflight --host
   HOST`. Save the JSON output. It reads the existing database with the
   historical read-only opener and reports service state, schema, configuration
   and installed-binary hashes, plus work and retained-capacity counts.
2. **Mac / selected Ubuntu host:** run `runner-lane prd-backup --host HOST
   --bundle-sha256 SHA256`. The receipt names a private backup directory under
   the existing PRD backup root. It contains a consistent SQLite online
   snapshot, the existing binaries, configuration, secrets, and the selected
   service definition. Keep the receipt and directory outside Git.
3. **Mac / selected Ubuntu host:** close external submissions under their
   owners' pause controls, then run `runner-lane prd-stop --host HOST`. Re-run
   `prd-preflight`; confirm the unsuffixed service is stopped and there are no
   active sessions, running or queued commands, or resumable jobs. Terminal
   lost work may retain slots or reservations for guarded recovery.
4. **Mac / selected Ubuntu host:** run `runner-lane prd-install --host HOST
   --bundle-dir CANDIDATE --bundle-sha256 SHA256 --backup-dir BACKUP`. This
   atomically replaces only the fixed bundle components in the existing
   unsuffixed `bin` directory. It does not change configuration, service
   definitions, database, routes, credentials, or mailboxes and does not start
   the service. New work identities or changed backup inputs cause refusal.
5. **Mac / selected Ubuntu host:** use `runner-locald recover-stalled` or
   `runnerd recover-stalled` from the synchronized, source-gated revision for
   the exact retained identities. Build this maintenance executable outside
   the checkout after that revision has been delivered to the host. The
   installed service bundle remains the exact per-host candidate accepted in
   Dev and QAS. Use `--lost-pair` for a terminal lost command and
   `--teardown-pair` for a complete succeeded or ordinary nonzero-failure
   command whose teardown was lost. On Mac, eligible sessions are local
   `local_user` sessions; on Linux workers, eligible sessions are remote
   `queued_mac` or `direct_mtls` sessions owned by that worker. The full
   identity, complete-output, terminal-event, released-command-slot, idle
   inventory, runtime cleanup, and finalization guards still apply. Recovery
   preserves results and event history and never replays work. Do not manually
   release reservations or slots. Then run `runner-lane prd-migrate --host
   HOST`; it requires a stopped service and a fully empty runtime, reservation,
   queue, and recovery-finalization inventory. Recovery commands may already
   have applied the forward migrations while opening the existing database
   for offline maintenance.
6. **Mac / selected Ubuntu host:** re-run `prd-preflight`; verify the current
   schema and empty work inventory. Start only the same unsuffixed service with
   `runner-lane prd-start --host HOST`, then perform the approved bounded
   direct and queued postflight checks. Keep external publishers paused until
   postflight is complete.

## Rollback boundary

If activation must be rolled back before new work has been accepted, stop the
same PRD service and run `runner-lane prd-rollback --host HOST --backup-dir
BACKUP`. The command refuses a running service, changed configuration or
service definition, or any new session, command, job, or local-intent identity
since the consistent backup. It restores the exact pre-migration database and
the prior binaries, leaves the service stopped, and preserves unrelated files
in `bin`. Start the old service only after verifying the restored version and
schema. Never restore this earlier snapshot over subsequently accepted work.

Retain the original and any later consistent backups, manifests, command
results, event histories, and held ACKs. Report a failed guard with its
measured inventory; do not bypass it to satisfy a numeric zero check.
