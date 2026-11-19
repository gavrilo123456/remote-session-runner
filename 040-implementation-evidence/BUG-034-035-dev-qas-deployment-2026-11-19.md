# BUG-034/035 — completed Dev and QAS deployment

## Release and source

The same sealed release passed Dev and then QAS on the existing Mac, Linux,
Sandbox and Factory hosts. All project clients/publishers remain owner-paused
until the requested Dev → QAS → PRD rollout is complete.

| Identity | Value |
| --- | --- |
| Service source | `7b11dbc08ad09eb5783c8ab3e5b33917ad2901d6` |
| Initial controller/source delivery | `6dae3e8c3ab62748e443eed065886c13159a7027` |
| Build | `01f1a2233728d3564e54fdd1df6d282d`, ready |
| Release | `rr-20261119-bug034035-7b11dbc` |
| Release SHA256 | `eeb3305b33de5e208693fe4622a4be6dee0a56bd2d717d4e410a83d8a99f9cc1` |
| Provenance SHA256 | `56332d179419c4573b7ff9f42cf21e8ac35d4fd59c1bf4cb9296f450d9437cd0` |

| Host | Sealed bundle SHA256 |
| --- | --- |
| Linux | `ec98c8225d232ea1cd5c73da7867ae8dbe0da750a986a6109a11f519569fac15` |
| Sandbox | `9a3a9700d29e3bc1c4d3e3c1b770273d3e130a7a6e5c6d3c219762dce2e2d55d` |
| Factory | `e5c32d158f5add25387f990ce889e71914a20ef8796fbab611c4da527d98aa61` |
| Mac | `fdf83edc3b1c9ce8276116199b144505c34bb0a5228e87fa5781f8542df8fe57` |

Exact source passed uncached full native test/vet/build/smoke on all four
hosts, including Linux's own full suite. The source records retain earlier
failed/interrupted attempts. Four new canonical build-input receipt copies
corrected JSON key ordering while preserving the original facts, evidence
hashes and timestamps; the original receipts remain unchanged. Build duration
was 182.998 seconds. Bundles were published and inspected before activation.

The initial Mac controller build exposed incomplete expanded shared module
cache directories. Verified cached ZIPs/go.sum identities supplied 7,339
missing files across 12 modules; no existing file was overwritten or deleted.
Built-in `go mod verify` passed afterward. This was additive cache restoration;
the original failed builds and verify output remain preserved.

## Dev

- Operation: `9f176fa303654108b00794b9f5f453fb`, complete.
- Preparation journal created: 2026-11-19 05:52:14 UTC.
- Cohort accepted: 06:04:57 UTC; released/completed: 06:08:11 UTC.
- Acceptance evidence:
  `b3746f14369da2b1d589df06a5b6cde2b51ea81d174ab6e15daaf2b515f813d1`.
- Completed operation-file SHA256:
  `e037731b43a33a52e2748bf34add859f8edf7a70a4185f2baadee2dca8a9dde7`.

All four hosts switched in the documented order and passed verification.
Capacity, direct-mTLS, isolation, lifecycle, limits, Mac-local, queued-SSH and
teardown all passed under `catalog-functional-v1`. The release closeout
synchronized admissions and restarted the empty selected services. CLI exit 0
reported `completed`; Dev branch sync is `not_applicable`.

An initial read-only plan failed on a transient Factory SSH reset before any
operation began. Its failure was preserved; a strict follow-up connection and
fresh public plan succeeded. No cause such as OOM was established. A separate
approval-review timeout occurred before process creation; the approved direct
retry started the single Dev operation above, with no duplicate execution.

## QAS

- Operation: `91c95785e310472a9b34398de81cce40`, complete, branch sync complete.
- Preparation journal created: 2026-11-19 06:13:42 UTC.
- Cohort accepted: 06:26:34 UTC; runtime released: 06:30:08 UTC.
- Acceptance evidence:
  `2ffba246490a1f32288c6565971a12aabd0f8c01f8a5b26d6feeb59565dacff7`.
- Completed operation-file SHA256:
  `fe3b9adabc87e280ac1b6395b2291e9d7d4ad60e946dd8f6d921ac3d8eddedcd`.

All eight gates passed with the exact Dev-accepted release and bundle hashes.
The runtime closeout completed before source-marker synchronization. The
first CLI returned `runtime_accepted_branch_sync_pending` because the primary
had advanced its marker and the automatic GitHub mirror was pending. A public
`runner-release branch-sync` call completed only that marker synchronization,
without repeating deployment. Its CLI exited 0 with `completed`; the completed
record was observed at 06:31:31 UTC. The record's `updated_utc` retains the
earlier pending event timestamp, so it is not used as a mirror-completion time.

## PRD and historical-request boundary

PRD remains on `rr-20261117-bug033-fd20b1e`. No new PRD cohort operation,
selector change, service restart or deployment acceptance has occurred.
Its read-only plan found capacity verified on all four hosts; Linux alone had
nonempty work because one original GPU LOST pair retains one reservation and
one command slot. Mac/Linux PRD remain stopped from the approved investigation;
Sandbox/Factory PRD remain active on the prior release, with clients paused.

The owner separately approved exact Linux cleanup. Its verified empty
workspace/marker archive, original result/event hashes and both unACKed GPU
exchanges were preserved. The first supported backup refused before recovery
due to [BUG-037](BUG-037.md). No recovery was invoked or capacity released.
The correction is limited to the operator entry points and is separately
qualified; the sealed service above remains unchanged.

The original two GPU outboxes remain accepted/revision 2 without cursors while
Mac PRD is stopped. They were not replayed, replaced or ACKed. No project
mailbox request or ACK was published during these rollouts. Filesystem-mailbox
acceptance, original PRD result-delivery acceptance and the historical-load
CPU comparison remain pending. BUG-036 is TRIAGED and unfixed.

## Evidence locations

- Runtime build/operation/acceptance records:
  `~/projects-runtime/runner-release-operations/` on the Mac.
- Source proof: [BUG-034](BUG-034.md), [BUG-035](BUG-035.md), with the durable
  source-qualification archive under that workflow root.
- Build, CLI output, plans and controller hashes:
  `/private/tmp/rsr-bug034035-dev-qas-prd-20261119-2_ejnmmy/`.
- Complete exact Dev/QAS operation copies and owner cleanup package:
  `~/projects-runtime/runner-release-operations/maintenance/bug034-linux-lostpair-approval-20261119/`.

This checkpoint introduces no new service source qualification or rebuild.
