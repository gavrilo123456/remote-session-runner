I have stopped all PRD clients and publishers. Proceed with deploying BUG-027 to PRD using the promotion toolkit. This supersedes the previous “stop after QAS” scope.

Context refresh — required:
After completing each numbered point (1–6), reopen and read `/Users/tomasz.walczuk/projects/remote-session-runner/060-messages/989-message.md` in full into your context before starting the next point. Also reread it after context compaction or resuming an interrupted session. Retain completed work and current state from the execution evidence; rereading these instructions must not reset progress or repeat completed operations.

Maintainer assistance checkpoint (2026-10-30; supersedes the missing-input blocker below):
- Both original inputs were found, not reconstructed. Their exact hashes and owner-only `0600` modes were independently verified. Originals remain at `/private/tmp/cat027-prd-309de02/bootstrap-request-corrected.json` and `/private/tmp/cat027-prd-309de02/bootstrap-approval-20261029T110127Z.json`.
- Verified byte-identical durable copies now exist under `/Users/tomasz.walczuk/projects-runtime/runner-release-operations/recovery-inputs/02bfdc603d2ea3360ecd32501fe347d1/`: use `bootstrap-request.json` and `original-approval.json`. `MANIFEST.json` records their original paths, sizes and SHA-256 values. No original, approval record, continuation, coordinator journal or live lane was modified to preserve these copies. Do not ask the owner to restore these files again.
- A separate renewal-ordering defect was identified: the latest saved controller amendment is `3187ec3`, its continuation expired at `03:07:45 UTC`, direct controller update requires a current window, and renewal requires a matching current-controller amendment. The old update-then-renew sequence cannot recover that state. The correction makes the public `prd-bootstrap-window-renew` command validate the exact owner deadline, original inputs, fresh plan and unchanged non-controller bindings before appending its matching controller amendment and renewal as an interruption-safe sequence. Ordinary controller-update expiry guards remain strict. See `040-implementation-evidence/BUG-027-bootstrap-input-unblock-2026-10-30.md` for validation and delivery status.
- The correction is committed as `51bee111dbb78c4be4cd73228777cb2b1cab50ec`, pushed to Gitea and verified on the GitHub mirror. Complete releaseflow-package tests, vet, native CLI build and help/version checks passed on Mac and all three workers; all workers were clean at that source SHA. The final evidence-only delivery follows those checks. Use the private `controller-manifest.json` next to `/private/tmp/rsr-bug027-maintainer-unblock-20261030/resume-bootstrap.py` for the final frozen controller HEAD and binary digest. The helper verifies inputs/HEAD and calls public renewal and bootstrap. It was unrun when this checkpoint was first recorded; see the current progress below.
- Preserve the seven existing controller amendments and the eight-record bound. Complete source/evidence delivery before adopting one final controller revision, and freeze that checkout HEAD through renewal and bootstrap completion. Do not append a controller amendment and then advance HEAD for documentation, because that would require a ninth amendment. Call the corrected public renewal command directly rather than the expired direct controller-update command. Retain all original bindings and records. The owner deadline remains `2026-10-30T10:00:00Z` (11:00 Warsaw), bootstrap-only.
- Continue the same sealed release `rr-309de02fca-ddc84f50`; its Dev/QAS acceptances remain valid. Do not rebuild service bundles for this releaseflow-only correction. After bootstrap and Mac-route completion, prepare the separate fresh PRD promotion plan/approval and run the toolkit's cohort acceptance. Do not repeat #514 recovery or publish mailbox requests/ACKs.

Current progress — bootstrap complete, PRD promotion package prepared (2026-10-30; supersedes the unrun statuses below):
- Checkout was clean at frozen controller revision `e26495ef34a7a14076f4fa36f33a3d0490a9fa9f`. The pinned Go 1.27.1 `runner-release` controller was verified against `controller-manifest.json`. A first restricted-shell invocation could not open `prd.lock` (`operation not permitted`); the same supported helper then ran with host-access permission, without deleting or changing the lock.
- Supported renewal succeeded for bootstrap operation `02bfdc603d2ea3360ecd32501fe347d1`, release `rr-309de02fca-ddc84f50`. It preserved original approval SHA `e6cfd2a34957bda938056b6e32b0ee6cc8ac5d79b9ee289378d88dc4696d5ac9`; continuation SHA `a3a601b3ea63738c98778d7fac6fb49187d0e04f6fc64fef6438c60a09c42f09`; fresh-preflight SHA `3c9a274acbc458d993906640810f5b6817e348d8321a19fe184fed5a1b347789`; renewal record SHA `4eccc6e920f6b330734b8774ef1d7a2e52a4575b2835d361ff1674610ff93a61`. Record: `~/projects-runtime/runner-release-operations/operations/prd-replacement-bootstrap/02bfdc603d2ea3360ecd32501fe347d1.window-renewal.json`. The authorized deadline remains `2026-10-30T10:00:00Z`, for bootstrap only.
- The same supported bootstrap resume completed all five stages through `mac_route_complete`, returning `ready_for_catalog_promotion`. Linux replacement PRD namespace is fresh, schema 40, stopped and unselected; configuration SHA `4499724aa7586332a82947c42aa32d7b6f73078eb3beea87c72410cb877de71d`; empty-work SHA `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b`; release bundle SHA `e05cc3d1a96ded5cde7e018fb73e589c9b53fe3e05c1c3174a42e8b6ac411b03`. The Mac route is complete at configuration SHA `2b35d897a7df65b13e77fbcdc0749da6f7df8d654d16a535a33faac5afbf2eb2`; its consistent bootstrap backup is under `~/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261030T062956.933944000Z/`, manifest SHA `26c5f7eabbf72245d5b16a50e99fb32ecc016b8eee4c6cf5e4d7822b4d7faf84`. Bootstrap journal SHA `8aeeb589a4bf0b6b1fefc0f47566ee38db504f67487f049149b8685a8e9cd9bf`. No selector was activated; no new Linux service was started.
- Sealed service release `rr-309de02fca-ddc84f50`, digest `4d650244e8dd9f08390cdb1619399ba40eb948f4db1adcc14f24ad6ff0bcf651`, is unchanged. Dev acceptance `c7fd67f8abc7dc59625885d4f2cb0fdf` and QAS acceptance `0869ab572185a699fd35e0e46206f425` both pass all eight release-bound gates for the same four bundle hashes. No source or service bundle was rebuilt for the bootstrap correction.
- A fresh read-only `promote-to prd all --release rr-309de02fca-ddc84f50 --plan --json` completed at approximately `2026-10-30T06:36Z` with outcome `planned_no_effects`. Private plan: `/private/tmp/rsr-prd-promotion-review-20261030/prd-plan.json`, SHA `dbde724198fa7143b82a5bc485e93e593509478085c93cac41b8e31d9983a320`. Its backup/recovery package SHA is `f085889883aa37af373ee92a330f8da252bb109a1236f229cb14645c76ad210c`; plan requires 500,000,000-byte operating buffer plus 67,108,864-byte selector reserve. All four work inventories were empty with the same inventory SHA. Current capacities were Linux 38,730,178,560 bytes, Sandbox 1,538,662,400, Factory 33,979,994,112, and Mac 110,573,334,528; each exceeded 567,108,864 bytes. Linux and Mac PRD services were stopped; Sandbox and Factory PRD remained active. Linux had no selected PRD release; Sandbox, Factory, and Mac remained on `rr-20261024-43a4e675`. `backup_verified=false` is expected in this no-effect plan; promotion preparation must take and verify fresh consistent backups before any selector switch. Recheck all capacity immediately before writes.
- A private `promote-to` controller built from frozen source `e26495ef34a7a14076f4fa36f33a3d0490a9fa9f` reports that revision and SHA `5b7e426fefe5385f65011ba55aa1048ede42ed990ede0c0d734ea7995545a139`. The initial `go run` using the Makefile's configured module cache failed because extracted module source files were missing; the successful plan used the already-existing `$HOME/go/pkg/mod` with the shared build cache. No source files changed.
- No PRD cohort promotion hold, cohort backup, selector switch, or service restart has occurred. QAS services, configuration, databases and mailbox ownership are unchanged. No mailbox requests or ACKs were published; #514 was not recovered again. Clients remain paused. The bootstrap deadline is not a PRD deployment approval. The separate release-bound PRD approval must use the current plan and an owner-authorized bounded PRD maintenance window; the deployment scope is authorized, but that PRD window has not yet been specified.

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

## Latest checkpoint — 2026-10-30, after controller retry correction

This checkpoint supersedes the stale instruction above to rerun the pinned
bootstrap helper. The bootstrap operation
`02bfdc603d2ea3360ecd32501fe347d1` already completed through
`mac_route_complete`, as recorded earlier in this file. The requested helper
was invoked; it stopped before mutation because its manifest pins controller
`e26495ef34a7a14076f4fa36f33a3d0490a9fa9f` while the delivered checkout is
now `272c3b5684bce5d1b565a79a1d233f9c931e1b21`. Its exact message was
`Checkout changed after controller validation; stop before consuming the
final amendment.` Do not rerun or rebuild that temporary bootstrap helper.

The separate PRD promotion attempt for `rr-309de02fca-ddc84f50`, operation
`8474c9b8e550bec2a900dc954824e9df`, remains blocked at Linux rollback receipt
synchronization. Its three other host selectors were rolled back to
`rr-20261024-43a4e675`; replacement Linux was rolled back to an unselected
fresh-install namespace. The supported retry correction is source commit
`272c3b5684bce5d1b565a79a1d233f9c931e1b21`. Mac, Factory, Sandbox and Linux
source gates passed as recorded in `040-implementation-evidence/BUG-027.md`;
the previous Linux full-suite interruptions remain NOT PASS.

The sealed successor `rr-20261030-e3d3f5a` remains unchanged and has completed
Dev acceptance (operation `b7a87e98f78c80c95cc47306851911ee`) and QAS
acceptance (operation `74518a25360a2364f37e83f52c88b83e`). QAS and its mailbox
ownership remain unchanged. No PRD rollback retry or new PRD plan has yet been
run. Next: refresh the four-host PRD inventory and capacities, retry supported
rollback/synchronization of `8474...` under its still-valid approval for
`rr-309`, then create a fresh PRD plan and owner approval bound to `e3d3f5a`.
The four-hour window supplied for `rr-309` must not be reused as the successor
release approval. Keep clients paused; publish no mailbox requests or ACKs.

## Latest checkpoint — held fresh-install rollback correction delivered

The retry correction described above is now delivered as Mac `dev` commit
`e8bec93dd049d1f1cfc8da28aeb55e20cba815b2`. It is controller-only; the sealed
service releases and bundle bytes are unchanged. The correction prevents
rollback closeout from opening admissions or attempting to start an unselected
fresh-install predecessor. It preserves the hold and stopped services after
verifying the synchronized rollback receipt, per-host selector/journal,
configuration/schema, empty-work identity and capacity. Regression coverage
uses the public backend path and rejects incomplete absence evidence.

The source-gate matrix at this exact revision passed: Mac full test/vet/build/
smoke; Factory Linux/AMD64 full test/vet/build/smoke; Sandbox Linux/ARM64 full
test/vet/build/smoke; replacement Linux focused CAT-06 and changed packages,
P025, P101, native vet/build/smoke. Linux's prior interrupted full-suite runs
remain NOT PASS and were not repeated. Details, Go 1.27.1 platform settings,
and captured log hashes are in `040-implementation-evidence/BUG-027.md`.
Primary Gitea, verified GitHub mirror and all three clean worker checkouts
were verified at `e8bec93`. The first remote command wrapper had an
`ambiguous redirect` quoting error before starting tests; the corrected runs
are the recorded gates.

At the last read-only source-gate check Sandbox had 812,974,080 bytes free;
its next write requires a fresh capacity measurement. No PRD operation was
retried and no PRD state changed during source validation. Next: reread this
file, recheck the current `rr-309` owner window and fresh four-host PRD state,
then use the supported controller retry to synchronize rollback operation
`8474c9b8e550bec2a900dc954824e9df` without releasing its holds. Afterward,
prepare a fresh release-bound PRD plan and approval for the already Dev/QAS
accepted successor `rr-20261030-e3d3f5a`; the `rr-309` approval/window does
not authorize that successor. Keep clients paused, QAS and mailbox ownership
unchanged, and publish no mailbox requests or ACKs.

## Latest checkpoint — rollback closeout compatibility follow-up

The post-`45df706` supported rollback retry verified the existing rollback
journals and synchronized `rollback-complete`, then failed the final
fresh-install predecessor check. The source correction in
`45df7068429c687183e6ecf2f7418b51742945e4` now makes the final closeout
compatible with the legacy Linux fresh-install snapshot: either all four
optional bootstrap provenance fields must be present, or all four absent
with valid core digests; partial provenance remains rejected. The final
postflight re-reads and matches the current fresh-install baseline against
the durable plan and synchronized receipt.

Mac, Factory, Sandbox, and replacement-Linux required source gates passed at
this exact source revision. The replacement Linux full-suite interruptions
remain NOT PASS under the approved Factory-substitution assignment. Gitea,
the verified GitHub mirror, and all three clean Ubuntu checkouts are at
`45df7068429c687183e6ecf2f7418b51742945e4`. Corrected Mac controller
executables are outside Git; sealed service bundles are unchanged. See
`040-implementation-evidence/BUG-027.md` for individual platform settings and
test-log hashes.

No new PRD operation was attempted after this correction. The earlier
10:09:48Z preflight is stale and cannot authorize a new write. Next: check the
current owner-approved PRD window, run a fresh read-only four-host plan and
capacity/work/identity check using the corrected controller, then retry the
same supported rollback closeout for `8474c9b8e550bec2a900dc954824e9df`.
Keep the cohort held/stopped and preserve the receipt history. Only after
closeout succeeds should a separate plan and release-bound approval be
prepared for the Dev/QAS-accepted successor `rr-20261030-e3d3f5a`. QAS and
its mailbox ownership remain unchanged; clients remain paused; publish no
mailbox requests or ACKs.

## Latest checkpoint — rollback complete, successor PRD plan ready

Supported rollback closeout succeeded for operation
`8474c9b8e550bec2a900dc954824e9df` at `2026-10-30T10:34:49.763353Z`:
`rollback_complete`. All four hosts are rolled back. The earlier failed
events remain recorded. A fresh successor plan still reports the exact old
operation hold on all four hosts, stopped PRD services, and empty inventories;
no admission reopened and no service was started.

The existing `rr-20261030-e3d3f5a` Dev and QAS acceptance receipts were
verified: operation `b7a87e98f78c80c95cc47306851911ee` and
`74518a25360a2364f37e83f52c88b83e`, respectively. Both bind digest
`15de9ab8e33749e00b6c5121d691b23bfa79ead4ee64ea5b7b930611404f96df`; all
eight gates passed in both lanes. QAS remains unchanged.

Read-only PRD plan for this successor passed at `2026-10-30T10:35:43Z` with
all four services stopped, empty-work SHA
`92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b`,
schema 40, matching current configurations, and the old operation's hold on
all four hosts. Private plan:
`/private/tmp/rsr-prd-promotion-review-20261030/fresh-e3-prd-plan-20261030T103543Z.json`,
SHA `0cbda831287053e97b94aba7754a6f066b678aeadd3953ce546c124ab3cb81de`.
Its backup/recovery package is
`4b349106bbc6146a7d8127d9a2cd549d95689a1fa1d72bde82beb6499f6cb4b9`.
Sandbox had 755,126,272 bytes available against the required
567,108,864-byte post-backup floor; measure again before writing and use the
post-backup report as the final capacity gate. No backup or new operation has
been created yet.

The owner extended the PRD maintenance cutoff to 23:00 Europe/Warsaw today
(`2026-10-30T22:00:00Z`). Bind the new approval to e3 and this fresh plan;
do not reuse the `rr-309` approval. Next: create the owner-only approval,
prepare the new cohort with `--prepare-only`, verify fresh backup and capacity
evidence, then continue the supported ordered switches and eight acceptance
gates. Keep clients paused, QAS and all mailbox ownership unchanged; publish
no mailbox requests or ACKs.

## Latest checkpoint — legacy fresh-install rollback compatibility

The first supported retry under the `rr-309` approval verified Linux's
already-applied rollback journal and synchronized the cohort's
`rollback-complete` receipt, then stopped at final validation with
`linux-host fresh-install predecessor evidence is incomplete or conflicts
with the recorded selector`. It did not open admissions or change a selector.

## Latest checkpoint — rolled-back-predecessor preflight correction

The current release-bound owner approval for operation
`01d9088c62e705632c9523346e14099d` ends at `2026-10-30T22:00:00Z`, which is
23:00 Europe/Warsaw today. The owner's latest instruction does not require a
renewal because that deadline already matches the requested cutoff.

The current operation was created and durably blocked during Linux host
preflight with `invalid or inconsistent catalog selector operation`. This
revealed a second controller gap: host preflight/fencing accepted a fully
switched `verifying` predecessor but rejected a fully synchronized
`rolled_back` predecessor, even though the supported supersession state
machine allows the latter. No backup or host mutation occurred during the
failed attempt. Its history remains in the operation journal.

The source correction accepts only a release-eligible PRD predecessor in
`verifying` or `rolled_back` state with the exact matching hold. It validates
host-local selector journals and binds either the verifying target bundle or
the rollback target/bundle to the receipt; fresh-install evidence must still
match the unselected replacement Linux baseline. A synchronized four-host
fixture exercises rollback, retry/synchronization, and rejection of selector
drift. The sealed release `rr-20261030-e3d3f5a` and its Dev/QAS acceptance
remain unchanged.

On Mac with Go 1.27.1, focused rollback/replacement tests, the complete
`laneinstall` package, the `runner-lane` CLI package, full `make test`,
`make vet`, `make build`, and `make smoke` pass. The first restricted full
test attempt is retained as NOT PASS: the sandbox denied an unrelated
test-owned `/bin/ps` subprocess; rerunning the same full suite with normal
process permissions passed. See `040-implementation-evidence/BUG-027.md` for
the details.

Next: complete this source/evidence change through the required Git delivery,
fast-forward the three clean Ubuntu checkouts, and run native Linux focused
validation. Then rebuild only the promotion controller from the delivered
source and resume operation `01d9088c62e705632c9523346e14099d` with fresh
capacity/work/service/identity checks. Preserve operation
`8474c9b8e550bec2a900dc954824e9df`, all held admissions, the sealed release,
QAS, mailbox ownership, and clients-paused state. Continue the ordered PRD
deployment and acceptance only when each supported guard passes; do not
renew the already-valid approval or repeat completed work.
The old operation snapshot has valid fresh-install and namespace-absence
digests but predates four optional bootstrap-provenance fields. It recorded
all four as absent. See `040-implementation-evidence/BUG-027.md` for the exact
fields, previous attempt and synchronized source evidence.

The compatibility correction is source commit
`45df7068429c687183e6ecf2f7418b51742945e4`. It accepts only the complete
legacy shape (all four optional fields absent) with the existing core digests
valid, re-reads the host baseline and matches its operation/evidence/absence
hashes, and continues to reject partial provenance. Public-path regression
tests cover both complete current and legacy snapshots. Required Mac, Factory,
Sandbox and Linux source checks passed at this exact revision. Linux's prior
interrupted full-suite runs remain NOT PASS. Gitea, exact GitHub mirror, and
all three clean workers are synchronized at `45df706`. Temporary corrected
`runner-release`/`promote-to` executables were built from that revision.

After the worker gates, Sandbox `/dev/sda1` reported 769,286,144 bytes free;
the rollback minimum is 567,108,864 bytes. Recheck immediately before writing.
The `rr-309` owner approval SHA remains
`7a1c9290dd30e2c47395c8036f15709ecbba1fc42b5f6f598109bc421acbbd44`, bound
to the existing plan/package, and expires at `2026-10-30T10:58:13Z`. Recheck
current time and approval validity before any operation. Next, refresh the
four-host read-only plan/state and invoke the supported rollback retry for
`8474c9b8e550bec2a900dc954824e9df` with the corrected controller. Keep the
cohort held and stopped. Then prepare a fresh, separately approved PRD plan for
the Dev/QAS-accepted `rr-20261030-e3d3f5a`; do not reuse `rr-309` approval for
that release. Preserve QAS/mailbox ownership and keep clients stopped.

## Latest checkpoint — rollback-complete predecessor supersession fix

The owner-approved PRD window already ends at 23:00 Warsaw on 2026-10-30
(`2026-10-30T22:00:00Z`); the requested extension does not require another
window record. The first supported `--prepare-only` attempt for
`rr-20261030-e3d3f5a` failed before creating an operation or changing host
state. Exact error and captured stderr hash are recorded in
`040-implementation-evidence/BUG-027.md`.

Cause: the four-host predecessor operation
`8474c9b8e550bec2a900dc954824e9df` is consistently `rollback_complete` and
still holds admissions. The low-level receipt supports guarded supersession
after a complete rollback, but the releaseflow controller only allowed a
`complete` predecessor. This was not a live selector or receipt mismatch.

The controller now permits superseding either a synchronized complete cohort
or a fully rolled-back cohort, with different exact-state checks. Rollback
supersession requires restored previous selectors/bundle identities, stopped
services, unchanged configuration/schema and empty-work identity, plus exact
unselected fresh-install evidence on replacement Linux. The successor hold is
still established first; the old hold is retired only after supported
per-host observations synchronize. Incomplete/divergent histories and changed
or unsupported baselines still fail closed. Sealed release bytes are unchanged.

New regressions cover full rollback, per-host supersession interruption/retry,
preserved rollback history, changed selector/work/config/schema/service state,
and the unselected fresh-install baseline. Mac Go 1.27.1 full `make test`,
`make vet`, `make build`, `make smoke`, and focused releaseflow tests passed.
The first sandboxed full-suite attempt is preserved as NOT PASS because its
unrelated BUG-020 test could not execute its test-owned `/bin/ps` process;
the full suite passed when rerun with normal host process permissions.

Next: finish Git delivery for this controller correction, fast-forward and
validate the three clean Ubuntu checkouts, then rebuild the controller from
the delivered revision. Re-run a fresh PRD plan; if the approval-bound release,
host set, predecessor bundle map and backup package remain identical, reuse the
existing approval through its 22:00Z deadline. Run `--prepare-only`, verify
post-backup capacity (especially Sandbox), receipt synchronization and exact
holds, then continue the same release in Linux → Sandbox → Factory → Mac order.
Do not repeat #514 recovery. QAS services/mailbox ownership remain unchanged;
clients stay paused; publish no mailbox requests or ACKs.
