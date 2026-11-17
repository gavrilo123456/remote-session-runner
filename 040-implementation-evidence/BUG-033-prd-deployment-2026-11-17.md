# BUG-033 — PRD deployment

**COMPLETE — PRD accepted, admissions released and source-marker synchronization complete.**
The owner authorized deployment and confirmed PRD clients/publishers stopped.
Operation `820eb31f30304d379403b3ff8b8529a3` is complete without rollback. The initial
supported attempt ended at `2026-11-17T22:34:21.252409+00:00` with exit1 after 282.623
seconds. Sandbox pre-backup capacity was 744,853,504 bytes against 1,295,504,100
required, short by 550,650,596 bytes. The guard blocked the attempt before a cohort
receipt, selector switching or selected-service stops. Its empty stdout, exact stderr
and blocked-operation snapshot remain preserved as failure evidence; completed online
preflight/backup artifacts remain preserved separately. After capacity was restored and
freshly checked, supported resume of this SAME operation completed at
`2026-11-17T23:02:31.957307+00:00` in 1372.243 seconds; the nine historical recoveries
were not repeated. Its preserved outcome was runtime_accepted_branch_sync_pending. A
supported branch-sync-only retry completed at `2026-11-17T23:03:10.351539+00:00` in
2.914 seconds; no runtime switch or service action was repeated for marker
synchronization.
Source qualification, network preparation, historical-work recovery and any
capacity wait are separate from the reported CLI elapsed times. Initial failure
and successful resume times are not combined into a misleading passing run.
Clients were not resumed by this maintenance.

## Immutable promotion and approval

The same release `rr-20261117-bug033-fd20b1e` passed Dev, then QAS, then PRD without rebuilding.
Runtime source is `fd20b1ebb5b551a1e9d143082101ae5d8843dd7b`; attested coordinator
source is `cc0f1d67dc13b17aeeb136b47163b18d86c29a72`.
The coordinator successor changed documentation only. Release SHA-256 is
`38127d62b469cddcb87968d86615f3c212d1e06de42c79862bf07d24ef1425e8`; provenance SHA-256 is
`0d116cc90cbd61fad5f027170c550b278076112a366c238b7695d52856c15e89`.
Build `1391d85143b789f6bceb62e9ab27f5b5` remains the ready four-host artifact.
Full native qualification and original failures are recorded in the
[Dev evidence](BUG-033-dev-deployment-2026-11-17.md); identical-byte QAS acceptance
is recorded in the [QAS evidence](BUG-033-qas-deployment-2026-11-17.md).
Those source gates were not rerun for this unchanged release promotion.

The PRD release-bound approval was prepared after exact historical recovery and
a fresh zero-work plan. Its SHA-256 is
`72b3ff23de832a0135dc751e057e0d771667f58117e0cf453a4462cce3389843`; its recorded
window is `2026-11-17T22:28:56Z` through `2026-11-18T02:29:26Z`.
No approval hashes or receipt history were edited to simulate authorization.

## Separate, explicitly approved historical recovery

The initial plan found nine retained Mac PRD reservations: seven completed-command
teardown pairs and two lost-command pairs, with two command slots held.
These were separate from deploying service bytes. The owner explicitly approved
only the nine named pairs after the concrete package was prepared.

Before recovery, a consistent selected-lane backup and exact read-only API,
original response/event, mailbox and ACK captures were preserved. The baseline
and post-recovery comparison prove eight terminal exchanges remained unACKed;
R183 was already ACKed and retained that acknowledgement. No blanket unACKed
claim applies to all nine. Original completed/failed/lost command outcomes and
event prefixes remain intact. Session GET routes returned session_not_found;
the exact responses were preserved as an API coverage gap, while the supported
inventory and consistent backup retained all nine session/lifecycle records.

Seven guarded scoped completed-teardown recoveries and the accepted sealed
binary's exact two-lost-pair recovery released nine Runner reservations and two
command slots. Every recovery used its supported backup, stopped-service,
ownership and capacity guards. Factory's separate reservations were untouched.
The first scoped recovery had already succeeded (nine reservations to eight)
when its immediate restart readiness observation failed. It was not repeated:
the continuation excluded that exact first pair and processed the remaining
six completed-teardown pairs and two lost pairs only. Early socket refusal and
later short health timeouts remain recorded. Fresh native observations later
proved both real listeners HTTP 200/ready; no inner startup cause is inferred.
The same selected predecessor binaries were restarted and verified before promotion.

The post-recovery exact-exchange comparison is a pre-promotion preservation
observation; it is not a claim of successful historical target execution or
continuous mailbox publication monitoring. Its manifest is
`/private/tmp/rsr-bug033-prd-20261117-t2lutf3l/post-recovery-original-exchange-comparison/COMPARISON.json`.

## Preparation and switching

A fresh supported plan found all four predecessor services active, schema 40,
supported active-work inventories empty and passing capacity. The toolkit
created consistent backups, fenced admissions, stopped only PRD, synchronized
one cohort receipt and switched Linux → Sandbox → Factory → Mac. No database
restore was performed during promotion.

| Host | Selected bundle SHA-256 | Switch intent → verified | Selector journal |
| --- | --- | ---: | --- |
| linux-host | `d9042be80f966fb1c55aef5c62882b9399f0d791c7eb3af2a77d8c80fdd0b4be` | 50.584s | `80380995f67f4addf1a1630fdce28a5e` |
| sandbox-host | `28d65c8b623a4a609dc654656e86e8f480518a1895ca1f565f4aef3b644dc590` | 45.721s | `58db97b3c9619c99604789616c967331` |
| factory-sandbox | `979e27c3db919976a78c23a6410f79e8813b2efd0e74d8472053d01a1b2b548c` | 66.651s | `958c5baa157e2027de5a5a164a11c87a` |
| mac-control-plane | `a9fceaf3f1bdf3a5ad851675cce58efd97eb47423bf9f48856bb1ecde507c60d` | 36.544s | `13536c27a50119eec9246eb2dd3e9ab1` |

These host durations cover guarded selection and journal verification, separately
from common preparation, functional acceptance and final admission release.
All selected bundle hashes match the accepted Dev/QAS artifact.

| Host | Consistent backup manifest SHA-256 |
| --- | --- |
| linux-host | `78742241ef8a9b4543a6e69f610830f90f7cbc24c4c1103a8c6f324b3fa790cc` |
| sandbox-host | `12018f4fa3f1ac7065cde4bf006d8efd57aba5a56fec16ff141d88c87cf3a181` |
| factory-sandbox | `0e66b9fb86d35b59de0aa9987ada2932ab64604242adb096544da1cc671e1c7a` |
| mac-control-plane | `aafd542197463049ba22485b7566465750950082424ac7e7e729aa754525cf27` |

## Acceptance and postflight

All eight release-bound catalog-functional-v1 gates passed: capacity,
direct-mTLS, isolation, lifecycle, limits, Mac-local, queued-SSH and teardown.
Acceptance at `2026-11-17T22:58:32.238336Z` has evidence SHA-256
`8318914df6671044ecac96f2f6c91495a87502f6ffa4f02f9fcf1ffa7fead133`. Public status reads
and the canonical Mac receipt
confirmed four identical released receipts, 20 transitions,
SHA-256 `c05361ed9ed61d65d1061943971897bc250d106ece27e57a9f8e33e7f7064674`.

Fresh postflight confirmed four active exact-release selections, schema 40,
empty supported active-work inventories, no admission holds and passing capacity.
Sandbox had 2271576064 free bytes against
567108864 required. Historical intents/results remain preserved;
empty active work does not mean all historical database rows were deleted.
Dev/QAS selector, configuration and service identity evidence matched the fresh
pre-operation baseline. After final release/restart, all three worker routes
passed authenticated TLS 1.3 HTTP 200/ready and rejected clients without certificates;
both unsuffixed Mac owner-only health sockets returned HTTP 200/ready.
Functional commands passed before admission release; the later health checks
were read-only and submitted no commands.

## PRD network scope and legacy trust

Automatic approval review initially rejected the inferred PRD firewall scope;
no repair was applied through that rejected attempt. The owner then explicitly
approved Linux/Factory PRD8443 access from any IPv4 source while retaining
mandatory certificate authentication. Fresh backups and exact one-rule syntax
checks preceded the guarded application. Each host gained one INPUT NEW
TCP8443 ACCEPT rule before its terminal reject. Dev8444 and QAS8445 rules were
preserved; no full live firewall restore was performed.

Actual PRD configuration remains legacy V2. The selected server enforces TLS 1.3,
trusted client certificates and explicit URI-to-controller mapping. Native
probes retain the configured CA bundle, client credentials and hostname
verification, plus separately observed server-leaf fingerprints (verified SSH
public certificates for Linux/Factory; an initial CA/hostname-verified TLS leaf
for Sandbox). These observation pins are not configured V3 certificate/SPKI/URI
pins. No CA, certificate, private key, application configuration or trust check
was weakened. The read-only persistence check at `2026-11-17T22:09:05.521666+00:00` verified
both saved files matched the reviewed candidates, retained root:root mode 0640 with
one link, and had enabled/active successful persistence loaders. No reboot
or reload is claimed as a boot-time test.

## Boundaries and delivery

No historical request was replayed or replaced, no ACK was published, and no
project mailbox assignment changed. Filesystem-mailbox acceptance and live
lost-acceptance fault injection remain NOT RUN. BUG-033's automatic observation
and no-replay behavior are covered by disposable source regressions. The older
GPU exchange remains administratively indeterminate; this rollout does not
establish its historical execution or teardown.

Private captures remain under `/private/tmp/rsr-bug033-prd-20261117-t2lutf3l` and
`/private/tmp/rsr-bug033-prd-network-20261117-omj3t6h4`. This closeout changes
only evidence/documentation, with diff review and git diff --check. Primary
Gitea push, exact automatic GitHub mirror and three clean worker fast-forwards
are recorded in
`/private/tmp/rsr-bug033-prd-20261117-t2lutf3l/prd-closeout-git-delivery.json`. Runtime
source tests
are not rerun for the documentation commit.
