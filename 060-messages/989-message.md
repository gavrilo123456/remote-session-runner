I have stopped all PRD clients and publishers. Proceed with deploying BUG-027 to PRD using the promotion toolkit. This supersedes the previous “stop after QAS” scope.

Context refresh — required:
After completing each numbered point (1–6), reopen and read `/Users/tomasz.walczuk/projects/remote-session-runner/060-messages/989-message.md` in full into your context before starting the next point. Also reread it after context compaction or resuming an interrupted session. Retain completed work and current state from the execution evidence; rereading these instructions must not reset progress or repeat completed operations.

Maintainer assistance checkpoint (2026-10-30; supersedes the missing-input blocker below):
- Both original inputs were found, not reconstructed. Their exact hashes and owner-only `0600` modes were independently verified. Originals remain at `/private/tmp/cat027-prd-309de02/bootstrap-request-corrected.json` and `/private/tmp/cat027-prd-309de02/bootstrap-approval-20261029T110127Z.json`.
- Verified byte-identical durable copies now exist under `/Users/tomasz.walczuk/projects-runtime/runner-release-operations/recovery-inputs/02bfdc603d2ea3360ecd32501fe347d1/`: use `bootstrap-request.json` and `original-approval.json`. `MANIFEST.json` records their original paths, sizes and SHA-256 values. No original, approval record, continuation, coordinator journal or live lane was modified to preserve these copies. Do not ask the owner to restore these files again.
- A separate renewal-ordering defect was identified: the latest saved controller amendment is `3187ec3`, its continuation expired at `03:07:45 UTC`, direct controller update requires a current window, and renewal requires a matching current-controller amendment. The old update-then-renew sequence cannot recover that state. The correction makes the public `prd-bootstrap-window-renew` command validate the exact owner deadline, original inputs, fresh plan and unchanged non-controller bindings before appending its matching controller amendment and renewal as an interruption-safe sequence. Ordinary controller-update expiry guards remain strict. See `040-implementation-evidence/BUG-027-bootstrap-input-unblock-2026-10-30.md` for validation and delivery status.
- Preserve the seven existing controller amendments and the eight-record bound. Complete source/evidence delivery before adopting one final controller revision, and freeze that checkout HEAD through renewal and bootstrap completion. Do not append a controller amendment and then advance HEAD for documentation, because that would require a ninth amendment. Call the corrected public renewal command directly rather than the expired direct controller-update command. Retain all original bindings and records. The owner deadline remains `2026-10-30T10:00:00Z` (11:00 Warsaw), bootstrap-only.
- Continue the same sealed release `rr-309de02fca-ddc84f50`; its Dev/QAS acceptances remain valid. Do not rebuild service bundles for this releaseflow-only correction. After bootstrap and Mac-route completion, prepare the separate fresh PRD promotion plan/approval and run the toolkit's cohort acceptance. Do not repeat #514 recovery or publish mailbox requests/ACKs.

Owner amendment and earlier checkpoint (2026-10-30; missing-input status superseded above):
- The owner extended only bootstrap operation `02bfdc603d2ea3360ecd32501fe347d1` for release `rr-309de02fca-ddc84f50` through `2026-10-30T10:00:00Z` (11:00 Europe/Warsaw). This supersedes the earlier four-hour duration/end. The extension must be written through the supported append-only renewal command; it is authorized but NOT YET RECORDED.
- The unchanged continuation file still records `2026-10-30T03:07:45.17119Z` as its end. Do not treat the owner-authorized 10:00 UTC deadline as stored until the separate renewal receipt is created and verified.
- Renewal tooling and its fresh-resume guard were delivered at source `e0ce0ffa5146bd2cd12da24f7f05ea73a9274452`; the exact GitHub mirror and all three clean Ubuntu checkouts match. Mac, Factory, Sandbox and Linux source gates passed as detailed in `040-implementation-evidence/BUG-027.md`.
- The corrected temporary Mac operator is Go 1.27.1, darwin/arm64, reports `build_revision=e0ce0ffa5146bd2cd12da24f7f05ea73a9274452`, and has SHA-256 `7287fc3e843a651841548ebd0632a36d673212ba1dd74185ea166e0f395c0c51`. It is controller tooling only; the sealed service release remains unchanged.
- Before renewal or bootstrap resume, restore the exact owner-only original request and approval files: request SHA-256 `a87e040f25e5f04f3c10dac448cd79ece259c2252ba0b51842b1e30ed7b7ef22`; original approval SHA-256 `e6cfd2a34957bda938056b6e32b0ee6cc8ac5d79b9ee289378d88dc4696d5ac9`. A read-only search of the operation store, RemoteSessionRunner support/backups, and `/private/tmp` found references to these hashes only in the existing bootstrap journal/continuation, not the source files. Do not regenerate either file or alter stored hashes.
- Existing operation journal still records `prepared`, `server_csr_ready`, `server_material_ready`, and `linux_namespace_ready`. No controller amendment, renewed deadline record, fresh operational preflight, root-mode correction, Mac route change, selector/service change, mailbox mutation, request or ACK was made during this checkpoint. Do not repeat #514 recovery.
- After the exact original inputs are restored, first confirm their hashes/mode, then use the supported controller-update and `prd-bootstrap-window-renew` commands. Renewal performs fresh four-host work/capacity/identity checks plus required Linux/Mac stopped-state checks; bootstrap resume rechecks the plan. Continue only if every guard passes and before the authorized 10:00 UTC deadline. This does not authorize PRD cohort deployment; that still needs its own release-bound approval after the route change.

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

Latest PRD replacement-bootstrap checkpoint (2026-10-29):
- Current operation: `02bfdc603d2ea3360ecd32501fe347d1`; accepted release: `rr-309de02fca-ddc84f50`, digest `4d650244e8dd9f08390cdb1619399ba40eb948f4db1adcc14f24ad6ff0bcf651`.
- The prior owner window expired at 15:01:27 UTC. A fresh read-only preflight at 16:05 UTC failed before writing continuation evidence because the replacement Linux PRD service root was mode `0775`; strict catalog inspection rejected it. No continuation window was created by that failed preflight.
- The owner has now authorized a narrow continuation record and a new four-hour Mac-route window. Its clock starts only after a successful fresh preflight. The original approval file and its SHA/history remain unchanged. This continuation does not approve the subsequent PRD cohort deployment; that requires a new plan and release-bound approval after the Mac route/configuration changes.
- The supported correction is delivered at source commit `df711cc03bc0bd32b2c87df834785bc6d212940b`. It adds a bounded read-only observer for the already bootstrapped, unselected Linux PRD namespace and an exact-operation, journaled `0775`→`0700` service-root correction. It verifies root device/inode/owner, bootstrap journal/release, original request/approval, fresh-install config/schema/empty-work identity, absent selector, stopped unit, absent listener and two stable process scans. It preserves the existing Linux bootstrap journal and never activates the selector or starts the service.
- Mac Go 1.27.1 gates: `make test`, `make vet`, `make build`, and `make smoke` PASS. The first sandboxed suite attempt failed only because `/bin/ps` could not inspect a test-owned process; the same package and full suite passed outside the sandbox. Linux native changed-package tests (`laneinstall`, `releaseflow`, `runner-lane`) and full `make vet`, `make build`, `make smoke` PASS at the delivered commit. Exact output and the observed 0775 failure are appended to `040-implementation-evidence/BUG-027.md`.
- Gitea `origin/dev`, verified GitHub `dev`, and clean fast-forwarded Linux, Sandbox and Factory worktrees all resolve to `df711cc03bc0bd32b2c87df834785bc6d212940b`.
- No root-mode correction, continuation record/window, selector change, service start/restart, PRD route change, mailbox mutation, request, or ACK has occurred yet. Next: run a new `prd-bootstrap-window-continue` with the owner-confirmed pause, obtain its successful fresh preflight and four-hour record, then use supported bootstrap resume to correct the Linux service-root mode and complete the Mac route. Reread this file after that point before starting the separate PRD deployment planning step.

Latest source and host-validation checkpoint (2026-10-29):
- The observer at `df711cc03bc0bd32b2c87df834785bc6d212940b` failed on the already-created replacement Linux PRD namespace because it reused the empty pre-install directory validator. Exact observer result: `invalid or inconsistent catalog selector operation`.
- Controller/config correction delivered at `0466c90d86c50fd1dff8313989840a035afc93df`. It adds a separate completed-namespace validator, binds a stable artifact digest into observations and root-mode correction records, and explicitly requires the validated Linux target profile to be `linux-host`. Service-release bytes remain unchanged.
- Mac Go 1.27.1 `make test`, `make vet`, `make build`, and `make smoke`: PASS. The first sandboxed full-suite attempt was NOT PASS because the existing BUG-020 fixture could not execute `/bin/ps`; the complete suite then passed outside the sandbox. Focused observer regressions PASS.
- Replacement Linux focused native tests (`config`, `laneinstall`, `releaseflow`, `runner-lane`) and native `make vet`, `make build`, `make smoke`: PASS.
- Factory full `make test`, `make vet`, `make build`, `make smoke`: PASS. The first smoke command's SSH wrapper did not return; a confirmed no-process/no-temp status-capturing rerun returned status 0.
- Sandbox full `make test`, `make vet`, `make build`, `make smoke`: PASS. Its first fast-forward attempt did not complete while SSH was failing before the banner; a fresh state check showed it remained clean at `df711cc`, and a subsequent supported `--ff-only` pull passed.
- Gitea primary and verified GitHub mirror resolve to `0466c90d86c50fd1dff8313989840a035afc93df`. All three Ubuntu worktrees were clean on `dev` at this commit before their native checks.
- No continuation record/window, root-mode, selector, service, route, mailbox, request, or ACK change has occurred. Next: fresh supported PRD continuation preflight; start the four-hour route window only if all four hosts and the owner-confirmed pause pass. Any later PRD cohort deployment still needs a fresh plan and release-bound approval. Keep clients paused and reread this file after the continuation step before proceeding to the next numbered point.

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
