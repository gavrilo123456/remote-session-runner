I have stopped all PRD clients and publishers. Proceed with deploying BUG-027 to PRD using the promotion toolkit. This supersedes the previous “stop after QAS” scope.

Context refresh — required:
After completing each numbered point (1–6), reopen and read `/Users/tomasz.walczuk/projects/remote-session-runner/060-messages/989-message.md` in full into your context before starting the next point. Also reread it after context compaction or resuming an interrupted session. Retain completed work and current state from the execution evidence; rereading these instructions must not reset progress or repeat completed operations.

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

Current replacement-address support and first-install handling are limited to Dev/QAS. PRD still expects the retired Linux addresses and an existing PRD installation. Resolve those dependencies through a supported, tested PRD bootstrap and route/configuration transition.

This authorizes the necessary PRD preparation and minimal Linux route, TLS, SSH-pin and host-profile changes. Preserve existing project mailbox paths and ownership.

Do not bypass guards or assume the old worker’s data and retained work were transferred. If service-code changes are necessary, create a successor release and accept its identical bytes through Dev then QAS before PRD. Keep the existing release unchanged. A controller-only correction may reuse the accepted service bundles only when their runtime/configuration compatibility is proved.

3. Prepare the actual PRD deployment
Collect fresh work inventories, verify the submission pause, measure capacity, and create consistent configuration/database and launcher backups. Validate rollback against the exact installed predecessors.

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

Update this file’s current checkpoint to reflect actual progress and retain its context-refresh instruction. Keep the completed Dev/QAS history in `990-message.md` as historical context; use this file as the current PRD assignment.

Report clearly whether PRD is deployed, functionally accepted, open for admission and ready for client resumption. Do not equate readiness alone with completed acceptance or normal-load observation.
