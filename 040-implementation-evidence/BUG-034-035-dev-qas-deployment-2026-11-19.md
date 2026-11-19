# BUG-034/035 — Dev/QAS rollout and PRD closeout

## Release and source

The same sealed release passed Dev → QAS → PRD on the existing Mac, Linux,
Sandbox and Factory hosts. Fresh postflights support route clearance; clients
were left paused and were not resumed by this rollout.

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

## Earlier PRD and historical-request checkpoint — before cleanup/promotion

At that Dev/QAS checkpoint, PRD remained on `rr-20261117-bug033-fd20b1e`. No new PRD cohort operation,
selector change, service restart or deployment acceptance had occurred.
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

## PRD closeout — 2026-11-19

The same sealed release/bundle hashes above are selected and active on all
four PRD hosts. Controller `0bb2e4f` includes the separately qualified BUG-037
maintenance correction; no service bundle was rebuilt. Operation
`31ab741f3e7f44ffbd2aa2c0ed90ebdd` passed all eight `catalog-functional-v1`
gates at **07:53:14 UTC**, evidence SHA256
`6e6d7f7e9e5824dc75c25cbfd76dc485af76d9ae788b6d6bed4d2cc16f360571`.
Runtime release completed at **07:56:36 UTC**. The operation and branch sync
were freshly observed complete at **07:57:09 UTC**; completed-operation SHA256
is `319d38a874ebe9baecbfd2c13a43d2b491fc7881f108ec69a3ec2665f685c548`.
Its `updated_utc` retains the 07:56:40 pending-event timestamp, which is not
used as the completion time. Branch-only synchronization resolved the pending
marker; deployment was not repeated.

The released cohort receipt is identical on all four hosts, with **20
transitions**, SHA256
`6488e8de6ca7266b9836d19f5fbd7f21510c6c29206cc037710bf77e58fbd1a0`.
Fresh native checks confirmed the exact selectors/bundles, active services,
schema40, empty active-work inventories, complete detailed exports, open
admissions and ready Mac ingress/executor plus worker Unix endpoints. The
Sandbox details read initially exceeded its 10-second deadline (**NOT PASS**);
a focused **08:01:49 UTC** retry passed with the exact source/schema/selection
and empty work, SHA256
`c76865a563486828d3123bf18528fac8795ea694ec24e5f3cda764da5205ec50`.
Sandbox free space was **958,377,984 bytes**, above the measured **584,208,480**
restore budget; only the archived diagnostic executable was temporarily
restored and removed after its reads exited.

Authenticated TLS1.3 health GETs returned HTTP200/ready for all three workers,
using configured CA, client identity and hostname verification plus the
previously verified leaf pin. The first system-curl attempt failed locally
because that curl did not support TLS1.3; its failure is preserved. The existing
native Go helper then passed the strict configured transport checks.

The **07:41:50 UTC** Factory `after_send` acceptance failure remains preserved.
A later read-only check found its scope ready/unaccepted and expired at
07:43:37 UTC, with fresh work empty. Supported resume of the **same operation**
succeeded. A restricted-shell lock `EPERM` refusal occurred before effect;
the authorized native-context resume then proceeded. Neither failure was
relabeled PASS or used to justify a guard bypass or a duplicate operation.

Before PRD promotion, separately approved exact Linux cleanup released one
reservation and one slot at **07:08:13 UTC**, preserving original history,
other records and the workspace/marker archive; see [BUG-037](BUG-037.md#four-host-operator-and-live-backup-closeout--2026-11-19).
At **07:57:41 UTC**, existing GPU outboxes reconciled at revision3: the API test
remains LOST/no exit/cursor24/incomplete capture-boundary output/LOST teardown;
the checkout observation succeeded/exit0/cursor7/complete output/CLOSED teardown.
Both exact event prefixes passed and both exchanges remain terminal/unACKed.
This restores truthful delivery and does not turn the LOST result into test
success. No original request was replayed, replaced or ACKed. Historical remote
intent records were preserved; their projections may advance through normal
GET-only observation.

No new project/filesystem-mailbox request or ACK was published. New synthetic
filesystem-mailbox acceptance is **NOT RUN**. PRD clients were left paused;
resumption clearance rests on the completed operation and fresh postflights,
not readiness alone. The bounded BUG-035 CPU observation below is separate
from functional acceptance; BUG-036 remains TRIAGED and unfixed.

### Bounded Linux idle CPU observation

From **08:04:34.242387–08:05:04.250639 UTC**, the actual sealed `runnerd`
PID217781/start3497039 used **3 ticks at HZ100** over **30.00824164 seconds**:
0.03 CPU seconds, **0.0999725% of one CPU** (about 0.10%). Its sealed
log-capture supervisor (systemd MainPID217770/start3497031) used zero ticks.
Both component hashes, UID1001, direct parent/cgroup, selector, boot and process
incarnations stayed stable; the unit was active with NRestarts0. Separate
aggregate steal was **6.8335254%**, on two CPUs. No history was purged; this
sampler opened no database and sent no signal. The first private observer
refused its MainPID-as-daemon assumption; only the observer was corrected.
This is a bounded observation, not a controlled before/after benchmark or an
explanation of changed steal. BUG-036's separate startup cost is still unfixed.
CPU proof: `/private/tmp/rsr-bug037-operator-qualification-7zuvmrjt/linux-idle-cpu-v2-20261119T080433Z/sample.json`,
SHA256 `98ac31e3545a6681f83ccb08b5ff6d673b7864dd9e9352158b80acf2dd31dca4`.

The combined `PRD-POSTFLIGHT-COMPLETE.json` proof is SHA256
`43e6306f48f460f3e7268d14adaf8ed06fc74eda0147d93141b4d1504cfca8d4`.
It retains the initial native summary's failed Sandbox detail flag and accepts
only the separately preserved successful focused retry; the initial summary
is not relabeled PASS.

Private runtime proof: `native-prd-postflight-0a9011aa/summary.json`,
`sandbox-details-retry-478bf426/summary.json` and
`postflight-go-tls-summary.json` under
`/private/tmp/rsr-bug034035-dev-qas-prd-20261119-2_ejnmmy/`.
Original GPU proof: `/private/tmp/rsr-bug034-gpu-delivery-90m0or7y/summary.json`,
SHA256 `e6e72b2061ddcbc1895d59dbba640dc9a24d21ae2f042c93f2bb11ef6879d616`.
The owner-only durable final-evidence archive is
`~/projects-runtime/runner-release-operations/maintenance/bug034035-prd-promotion-20261119/final-evidence-20261119/`:
133 verified files / 13,273,848 bytes, `MANIFEST.json` SHA256
`4ba1e556c1a3210a485eab59386a4a58a9585f4e223df97b87ff1a4aa452feb0`.
It preserves operation/acceptance records, postflights and the focused Sandbox
retry, original GPU response/events, TLS/curl failures, both CPU observations
and operator qualification proofs. Original runtime/source/backup/cache data
remain untouched by this archival copy.

Durable approval/proof and final documentation-delivery record are under
`~/projects-runtime/runner-release-operations/maintenance/bug034035-prd-promotion-20261119/`;
`FINAL-GIT-DELIVERY.json` records the final primary/mirror/clean Ubuntu SHA
checks after this documentation commit. Documentation finalization does not
rebuild or reactivate any service.
