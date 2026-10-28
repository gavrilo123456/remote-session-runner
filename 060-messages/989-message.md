I have stopped all PRD clients and publishers. Proceed with deploying BUG-027 to PRD using the promotion toolkit. This supersedes the previous “stop after QAS” scope.

Context refresh — required:
After completing each numbered point (1–6), reopen and read `/Users/tomasz.walczuk/projects/remote-session-runner/060-messages/989-message.md` in full into your context before starting the next point. Also reread it after context compaction or resuming an interrupted session. Retain completed work and current state from the execution evidence; rereading these instructions must not reset progress or repeat completed operations.

Latest owner decision — old Linux worker retired from all lanes:
- `129.151.232.40` is no longer required for Dev, QAS or PRD. Use replacement `linux-host` at `79.72.94.59`, private address `10.0.0.228`.
- Do not require restoration of old-host SSH, old-host inventory/backups, workspace transfer or a cross-host data importer before proceeding. The owner authorizes a fresh PRD installation on the replacement, without importing the old Linux database, workspaces, reservations or receipts. Existing replacement Dev/QAS state must be preserved.
- Preserve existing historical records and any available backups. Record old Linux work as not migrated, with its outcome/cleanup unknown where unproved. Do not replay old requests, claim cleanup or success, or transfer old reservations into fresh capacity. This instruction does not authorize deleting or otherwise modifying the old server.
- Remove the retired endpoint from effective active Dev/QAS/PRD routes, operator host profiles and fallback paths through supported, backed-up transitions. Historical references may remain in evidence and backups. Do not globally replace historical addresses in source or erase their provenance.
- Rollback must keep the retired endpoint disabled. For the fresh replacement PRD namespace, prove its absent predecessor and support rollback to absence or a stopped/held state while preserving new evidence. Restoring an older Mac configuration must not silently reactivate the retired route.
- #514 belongs to Mac PRD and remains a separate exact retained-work gate. Retiring the Linux worker does not authorize recovery, capacity release, replay or ACK for #514.

Current checkpoint:
- BUG-027 and the replacement Linux worker have completed Dev → QAS qualification and deployment.
- Accepted release: `rr-1e6c47721d-f83e3be7`.
- Source: `1e6c47721d98ba5af01145ff2e2ed44eb8639030`.
- Release digest: `c21b2e07763d05b5ffa77b26c0fc30ef0918135c995248df8d7374eb9adc9acd`.
- Build: `75a8d24adff7a101f4ca3c4e26e311d6`, stage `ready`.
- Dev acceptance: `db7efa3c78056440d580f8671f9c18fc`.
- QAS acceptance: `a353cb72e3f72df38cb3e64e74349841`; deployment and branch synchronization are complete.
- No PRD deployment, service change or mailbox reroute occurred during that Dev/QAS assignment.
- The owner now reports all PRD clients and publishers stopped. Fresh inventories and operational checks are still required.
- The release above is the last accepted Dev/QAS reference. A later route-changing candidate, `rr-a38cb0057e-877d49e3` at source `a38cb0057ed2c4ee7782473b8131ac95af4deab4`, has been reported as sealed. Verify its current qualification and acceptance records; the earlier Dev/QAS acceptances do not qualify changed service bytes.

Replacement worker:
- SSH alias: `remote-session-runner`.
- Public IP: `79.72.94.59`.
- Private IP: `10.0.0.228`.
- Account: `ubuntu`, UID 1001.
- Identity: `~/.ssh/remote-session-runner`.
- Ubuntu 26.04, x86_64; verified SSH and noninteractive sudo were established during Dev/QAS preparation.
- Use task-owned disk-backed staging under `~/.cache/remote-session-runner` and normal shared Go caches; `/tmp` is a small tmpfs.

1. Start from the accepted release
Verify the recorded Dev and QAS acceptance for the exact release, source and digest above. Preserve its immutable bundles.

Read the current closeout and promotion procedures:
- `040-implementation-evidence/BUG-027.md`.
- `050-bugs/027-mac-retained-work-inventory-unavailable-for-safe-recovery.md`.
- `docs/promotion-guide.md`.
- The applicable PRD bootstrap, configuration and recovery procedures in `docs/operations.md`.

2. Complete supported PRD replacement-worker preparation
Use `linux-host` at `79.72.94.59`, private address `10.0.0.228`.

Verify the current replacement-address correction and finish the supported, tested fresh PRD bootstrap and route/configuration transition. An address correction or existing-host adoption fixture alone does not prove first installation on the replacement. Do not design a state-preserving import from the retired worker as a prerequisite; no old Linux data migration is requested.

This authorizes the necessary PRD preparation and minimal Linux route, TLS, SSH-pin and host-profile changes. Preserve existing project mailbox paths and ownership.

Do not bypass guards or fabricate a predecessor. Bind fresh Linux PRD preparation to verified namespace absence on the replacement, with fresh configuration, database, workspaces and TLS/SSH identities. If service-code changes are necessary, create a successor release and accept its identical bytes through Dev then QAS before PRD. Keep existing sealed releases unchanged. A controller-only correction may reuse accepted service bundles only when their runtime/configuration compatibility is proved.

3. Prepare the actual PRD deployment
Collect fresh work inventories on the current Mac, Sandbox and Factory PRD authorities, verify the replacement Linux PRD namespace's actual state, confirm the submission pause and measure capacity. Create consistent configuration/database and launcher backups for current installations and record the replacement's fresh-install predecessor explicitly. Validate rollback for those actual baselines, keeping the retired route disabled; unavailable old-Linux backups are not a prerequisite.

Prepare the toolkit’s current release-bound approval package covering the actual release, replacement topology, backups, recovery inputs and bounded maintenance window. Follow the required owner approval mechanism before live mutation; a renewed evidence window does not silently extend the owner-approved window.

Preserve #514 and uncertain work. If retained work blocks deployment, prepare its exact supported recovery package and identify the specific additional decision required. Do not delete, replay, ACK or manually release it.

4. Deploy through the toolkit
Run the read-only PRD plan, then use `promote-to prd all` with the reviewed PRD hosts file and matching approval file.

Use supported holds, backups, receipt synchronization, selector transitions and recovery. Continue existing operations through supported resume/renewal commands where eligible. Preserve the accepted Dev/QAS lanes except where renewed qualification of a necessary successor release is required.

5. Verify before reopening
Pass all eight cohort acceptance gates on all four hosts: direct mTLS, queued SSH, Mac-local, limits, capacity, lifecycle, teardown and isolation.

Separately install the qualified Mac PRD diagnostic and verify `catalog-work-inventory --details` against the selected PRD authority. The diagnostic is separate from the service bundle. A complete inventory does not establish cleanup or authorize recovery; retain its explicit safety and recovery-authorization fields.

Keep mailbox requests and ACKs untouched unless separately authorized. Report filesystem-mailbox acceptance as `NOT RUN` where applicable.

Release maintenance holds only after successful acceptance. Leave external clients paused for me to resume.

6. Close out
Update BUG-027, deployment evidence and the promotion records with actual release hashes, operation IDs, acceptance results and remaining observations.

Update this file’s current checkpoint to reflect actual progress and retain its context-refresh instruction. Record the owner's all-lane retirement decision and supersede old-host SSH/import blockers in current preparation evidence without rewriting historical observations. Keep the completed Dev/QAS history in `990-message.md` as historical context; use this file as the current PRD assignment.

Report clearly whether PRD is deployed, functionally accepted, open for admission and ready for client resumption. Do not equate readiness alone with completed acceptance or normal-load observation.
