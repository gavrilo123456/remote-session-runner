# BUG-027 — Mac retained-work inventory is unavailable through a supported read-only interface

## Summary

| Field | Value |
| --- | --- |
| Status | `RESOLVED FOR DEV/QAS` — installed diagnostic verified; PRD deployment and #514 recovery remain outside this assignment |
| Severity | High — blocks safe recovery and replacement work after a retained-lost result |
| Priority | High |
| Reported | 2026-10-27 |
| Discovered by | Agentic Software Factory P09-T02 verification and planning review |
| Owner | Remote Session Runner |
| Affected component/path | Mac owner-only diagnostics, retained-work inventory, and offline recovery preflight |
| Affected revision | Observed live build `43a4e675ccccb89e1ea1c333e975ea028c9600fa`; inspected checkout `75a714e8d482930ad2c56ba4baca0d15b3fff24c` |
| Fixed revision | Diagnostic source fix `f0f604db7629dc50096cd32963ae373c11a0e341` is included in accepted service release `rr-1e6c47721d-f83e3be7`; separate Mac diagnostic build `883e2af0373b7de94f172df31909434f9a23c38d` |
| Verification | Dev and QAS accepted the same sealed release; the separately installed Mac diagnostic returned complete, authority-bound Dev and QAS inventories. PRD and #514 recovery were not changed. See [BUG-027 evidence](../040-implementation-evidence/BUG-027.md). |
| Related records | [BUG-014](014-idle-mac-retained-lost-capacity-recovery.md), [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md), [BUG-010](010-mailbox-stale-ready-markers-request-lifecycle.md) |

## Reported behavior

The documented Mac retained-lost recovery procedure requires a fresh, complete
inventory of retained session/command pairs and other work that must survive.
The supported diagnostics inspected during this incident expose aggregate
counts or status for already-known IDs, without a complete identity-level
export. The operator cannot establish the required recovery input safely.

This is an operability gap between recovery prerequisites and diagnostics.
It does not establish the cause of request #514's lost teardown, prove that a
process is still running, or establish a recurrence of BUG-026.

## Expected behavior

Provide an owner-only, supported, read-only metadata export from the selected
Mac authority that identifies all work relevant to recovery, including retained
capacity and pending finalizations. It must work while retained holds exist,
identify its authority/configuration/version, and explicitly report whether the
inventory is complete.

The operator should be able to obtain the full pair list before maintenance,
without direct SQL, guessed IDs, service interruption, or an unqualified helper
built from a different source revision. An inventory is an input to guarded
recovery; it does not itself prove process cleanup or authorize releasing holds.

The prerequisite is documented in
[operations: explicit Mac offline recovery](../docs/operations.md#explicit-mac-offline-recovery-for-a-fully-idle-retained-lost-set).

## Reproduction evidence

```text
Machine/account: Mac workstation — tomasz.walczuk / UID 502
Inbox: agentic-software-factory-runner-direct
Repository alias: agentic-software-factory
Execution route: mac-dev / local/mac-workstation
Request: req-asf-p09-startup-reconciliation-proof-verification-20261027-514
Command: cmd-2872a83c6411e7838bad02ca687fbfbd
Session: sess-dc857a66bccb38298a6b0b26485588c5
Job: job-55049ec48ecff3747d09f97d96a18783
Configured command timeout: 1800 seconds
```

Existing evidence is sufficient; do not replay this request to reproduce the
diagnostic gap. Read-only inspection sequence on the Mac:

1. Identify the selected live Mac authority and read its readiness and metrics.
2. Read `GET /v1/commands/{command_id}`, `GET /v1/jobs/{job_id}`, and
   `GET /v1/sessions/{session_id}` for the exact IDs above through the approved
   owner-only status route.
3. Compare these known-object reads and aggregate capacity counts with the
   recovery procedure's requirement to supply every retained pair and account
   for all running, unknown, queued, resumable, and pending-finalization work.
4. Observe that the documented status API and inspected CLI report do not
   enumerate that complete set. Do not infer that the known request is the
   only retained work.

### Preserved incident result

| Observation | Value |
| --- | --- |
| Command started | `2026-10-27T14:44:05.320329Z` |
| Pytest killed | `2026-10-27T15:14:07.184930Z`, stderr `Killed: 9` |
| Command recorded lost | `2026-10-27T15:14:07.322834Z` |
| Terminal projection | Revision 3, event 88; request `complete`, command/job `lost` |
| Output / exit | `output_complete=false`, `capture_boundary_unconfirmed`, no exit code |
| Teardown | `lost`; job reason `runtime_cleanup_unconfirmed` |
| Later session lookup | `session_not_found`; not independent exit or cleanup proof |

The timing is consistent with termination at the configured timeout boundary.
The 88 advertised events are contiguous, but they do not establish complete
command output or teardown. No completed pytest summary or usable JUnit report
was obtained.

The saved read-only snapshot at `2026-10-27T15:39:14.632672+00:00` through
`15:39:15.006992+00:00` reports live/ready ingress and executor counts of one
active session slot, one active command slot, and zero queued commands. It also
reports four queued intents and a reconciliation age of 691467 seconds.
Remote-intent observations include four uncertain intents; these are not four
identified Mac commands and must not be treated as an enumerable local job set.
The snapshot is several separate reads, not an atomic full inventory. It
explicitly records `inventory_complete=false` and `safe_to_resume=NOT_ESTABLISHED`.

Primary evidence in the Factory checkout:

- [Read-only authority snapshot](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-27-514-planner-read-only.json), SHA-256 `315dbc10a77cfb8a4dd079050100b7369362097259b0939bfae20852c8906ab4`.
- [#514 response](/Users/tomasz.walczuk/projects/agentic-software-factory/implementation/evidence/P09/runner/req-asf-p09-startup-reconciliation-proof-verification-20261027-514/response.json), SHA-256 `8187a502d20bb5f0320e77776b26e448943e8b71d34565347455c3ba21d0d7a4`.
- [#514 events](/Users/tomasz.walczuk/projects/agentic-software-factory/implementation/evidence/P09/runner/req-asf-p09-startup-reconciliation-proof-verification-20261027-514/events.ndjson), SHA-256 `c94fee9877dc23a080645fe5365efaadda1d4bfb0a67a556917c035be0423ad0`.
- [#514 reconciliation](/Users/tomasz.walczuk/projects/agentic-software-factory/implementation/evidence/P09/runner/req-asf-p09-startup-reconciliation-proof-verification-20261027-514/reconciliation.md).

These are local evidence paths, not assertions that the evidence is committed
or part of the Runner release.

## Impact and scope

Factory verification cannot safely proceed to recovery or replacement tests
because a complete current inventory cannot be established. Existing holds and
the historical lost result must remain preserved. Other mailboxes working
successfully do not establish cleanup or capacity disposition for this request.

The observations do not prove global capacity exhaustion, a still-running
pytest process, or failure of every Runner route. No supported complete
alternative inventory route was found in the inspected API, CLI, and runbook.

## Investigation

- [API endpoint inventory](../docs/api.md#endpoint-inventory) and
  [local API server](../src/internal/localapi/server.go) provide known-ID
  resource reads and mailbox lifecycle metadata. They do not expose the full
  Mac retained-work identity set across mailboxes.
- [Restart preflight](../src/internal/runnerlocald/restart_preflight.go) reports
  aggregate active-session, active-command, queued-command, and resumable-job
  counts. It cannot supply the required exact pair list.
- `runner-lane catalog-work-inventory --host mac-control-plane --lane prd`
  exists in the inspected source. Its
  [report](../src/internal/laneinstall/catalog_selector.go) and
  [CatalogWorkInventory](../src/internal/laneinstall/prd_upgrade.go) expose
  counters, including reservations and pending recovery finalizations, rather
  than identity rows. This command was source-inspected, not run against the
  incident authority.
- [Recovery implementation](../src/internal/runnerlocald/recover_stalled.go)
  checks the complete durable inventory inside the guarded recovery path.
  Those mutation-time guards do not provide the missing standalone preflight
  export to the operator.
- The live and inspected revisions differ. That alone does not establish
  incompatibility, but any diagnostic tool accessing the selected authority
  must have explicitly verified schema/runtime compatibility; an arbitrary
  newer checkout is not an established supported live diagnostic route.
- BUG-014 supplied the recovery path. This report addresses obtaining its
  prerequisite inventory and does not reopen BUG-014's historical acceptance.

## Fix and verification

Prefer extending the existing owner-only diagnostics and read-only store
queries rather than introducing a new persistence system. Required acceptance:

1. Export all recovery-relevant identities for the selected Mac authority,
   across mailboxes: retained session/command pairs, unreleased session and
   command reservations, running/queued commands, resumable jobs, and pending
   recovery finalizations, including commandless or completed-command cases.
2. Bind the export to host/lane, active authority, configuration, schema/build
   identity, observation time, and a consistent read cut. State completeness
   explicitly; pagination, missing tables, unavailable authorities, or partial
   reads must not silently produce a complete/empty verdict.
3. Return metadata only: IDs, states, ownership/cleanup classifications, and
   necessary safe provenance. Exclude scripts, output payloads, credentials,
   environment values, and secret material; enforce owner-only access.
4. Prove the operation performs no recovery, migrations, cancellation, ACK,
   replay, capacity release, or service lifecycle changes. Test nonempty held
   state, mixed active/queued/resumable work, interrupted finalizations, empty
   state, and incomplete/error responses.
5. Keep remote intent counts distinct from local execution inventory. Counts
   alone or absence of a known session must not establish safe-to-resume.
6. Document a supported installed-tool/API procedure that obtains the exact
   full list before maintenance while retained holds exist. Preserve recovery's
   fresh revalidation, process-cleanup requirements, and separate authorization.
7. Verify the installed Mac path against retained-work fixtures and capture
   live metadata-only acceptance before closing this bug. Preserve #514's
   historical `lost`/incomplete result and uncertain holds unless a separate
   supported recovery disposition proves their cleanup and release.

No wider repair campaign, timeout fix, or runtime recovery is assigned by this
registration.

## Resolution

The correction adds `runner-lane catalog-work-inventory --details` to the
existing owner-only diagnostics. It returns metadata from one bounded,
read-only schema-40 cut and explicitly reports completeness. Configuration,
database and selected-artifact identities are revalidated around the read;
incomplete or incompatible authorities fail without publishing partial rows.
The aggregate inventory command remains compatible.

Qualification and the exact supported diagnostic artifact are recorded in
[BUG-027 evidence](../040-implementation-evidence/BUG-027.md). At the initial
2026-10-27 checkpoint, the owner had assigned Dev-only deployment, Linux's
full-suite attempt had timed out, and no Dev release had been activated. That
checkpoint is historical; the 2026-10-28 addendum records the later accepted
Dev and QAS rollout.

The qualified `runner-lane` diagnostic is a separate operator executable, not
a component of the service bundles. It is installed in the Mac Dev and QAS
tool directories and verified against both selected Mac authorities. PRD was
not deployed or rerouted. Recovery of #514 and the cause of its lost runtime
boundary remain separate work.

## History

| Date | Change |
| --- | --- |
| 2026-10-27 | Registered the diagnostic/recovery inventory gap using #514 evidence and source/runbook review; status `NEW`. |
| 2026-10-27 | Delivered source fix `f0f604db`; Mac/Sandbox/Factory full source gates passed. Linux's own full-suite attempt timed out in dispatcher; remaining work was interrupted after this definite failure. Dev-only deployment remains NOT RUN; no Linux passing receipt, new sealed release or lane activation exists. |
| 2026-10-28 | Deployed and accepted sealed release `rr-1e6c47721d-f83e3be7` in Dev and QAS; installed the separate Mac diagnostic and verified complete Dev/QAS inventory reads. PRD and #514 recovery remain untouched. |
