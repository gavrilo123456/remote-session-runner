# Execution handbook: Dev, QAS, and PRD lanes

**Status:** complete execution breakdown and first-rollout decisions. No unit
has run. This handbook is a mandatory companion to the
[design](010-dev-qas-prd-three-machine-lanes-detailed-design.md) and
[L0–L7 plan](015-dev-qas-prd-three-machine-lanes-phased-implementation-plan.md).
It removes the need for another milestone-breakdown or requirements discussion.
It does not claim that host access, capacity, bug fixes, or future exact-body
approvals already exist.

## 1. Fixed rollout decisions

The following profile selects among the design's permitted alternatives. A
later expansion is separate work; it is not a question to resolve during this
rollout.

| Subject | Selected decision |
| --- | --- |
| Cohort/order | Mac `mac-control-plane`, then Ubuntu `linux-host`, `sandbox-host`, `factory-sandbox`; all three remote profiles in every lane |
| Source/build | Mac `dev` is authoritative; Gitea primary and verified automatic GitHub mirror; clean Ubuntu `dev` checkouts; Makefile Go `1.27.1`, `GOTOOLCHAIN=local`, recorded toolchain/input digests; no cross-host artifact copy |
| PRD compatibility | Keep all unsuffixed roots, mailboxes, labels and ports; Mac storage mode `host_free_space_budget`; retain existing Linux PRD host scope and port-22 bridge. No optional PRD volume migration or new PRD cgroup controls in this rollout |
| Dev/QAS execution | Remote submitted jobs only; Mac control-plane compute reservation remains `none`; no extra OS account, VM, container or local-job feature |
| Initial Dev/QAS inboxes | One fresh `default` inbox at each lane's derived Mac `<root>/mailbox`, repository alias `remote-session-runner`; default context `ubuntu-current`; allow `ubuntu-current`, `ubuntu-sandbox`, `ubuntu-factory-sandbox`, each bound to that lane's corresponding remote profile and existing environment name. No `mac-local` context. Materialize complete independent V3 documents, not YAML inheritance from V2 |
| External Dev/QAS producers | Disabled for this first rollout. The three remote routes are all tested through the internal acceptance inbox. Preserve every existing PRD external root/producer from fresh inventory. Adding an external Dev/QAS producer is a later configuration transition with its own storage handoff |
| Linux enforcement | cgroup-v2 lane slices, per-device `io.max`, and project byte/inode quotas for lane/cache/staging scopes. Use the filesystem's supported project-quota implementation; lack of required enforceable controls stops readiness, with no weaker substitute |
| Mac storage | Quota-bound APFS Dev/QAS service-root volumes at the design's fixed mountpoints. Keep the documented shared Go-cache paths; separately bound caches, snapshots, staging and backups; no cleanup of shared or retained data |
| SSH listener defaults | Fixed Dev/QAS ports and wrappers from design §5; `MaxSessions 1`, `MaxStartups 3:30:10`, `LoginGraceTime 30`, `MaxAuthTries 3`; key-only, no PAM/PTY/forwarding/subsystem and complete descendant slice membership |
| Release/security | Exact design §7 matrices, JCS/SHA-256/Ed25519 V1 records, signer roles, key/ledger/time checks, same-host verified cache and separate recovery-volume backup. No alternate signing, SSH release transport, trust-on-first-use or implicit approval path |
| Accountable/approval owner | Tomasz Walczuk for all project decisions and change approvals; he is also the default storage, host, network/provider and producer coordinator. The implementer executes scoped approved work using verified access. A delegation is optional and must name its actual authorized operator; no unnamed administrator is a required planning input |
| Scope boundaries | Event-driven wake-up, hostile-code isolation and a Mac compute guarantee remain excluded. Required timeout, explicit-limit, managed-session and retained PRD local teardown behavior cannot be removed to pass |

Concrete capacities are execution data, not new product choices. The
implementer derives a complete proposed per-host envelope from L0 inventory,
existing PRD ceilings/observed use and design §6; then proves the selected
Dev/QAS/management caps and budgets in the L2 native pressure fixtures before
any installation. Selecting enforceable numerical settings within those fixed
contracts is delegated engineering work and needs no planner clarification.
Record every value, calculation, device and proof. No feasible envelope, a
failed pressure test, or required unsupported quota/I/O control is a real
capacity failure: stop rather than invent evidence, silently change hardware,
weaken limits or omit a host. Approval of an exact signed policy still happens
at its declared checkpoint.

## 2. One readiness packet; no late information requests

The checked-in
[execution ledger](../../040-implementation-evidence/LANES-000-execution-ledger.md)
is already the ordered plan. `LANES-PREP-00` fills its readiness packet by
read-only discovery and prepares all known external change packages. L0 adds
the full read-only inventory. The named bug prerequisite units then establish
the runtime baseline. Before L1 starts, the ledger must seal the readiness
packet with all foreseeable owner inputs, access and resource/change plans
resolved. Do not postpone an owner identity, producer list, policy choice,
fixture provision or provider-access question until L3/L4/L6.

**Authorized observation route for the first two units (2026-10-16).** The
owner authorized direct read-only Mac shell and SSH observations for
`LANES-PREP-00` and `LANES-L0` across `mac-control-plane`, `linux-host`,
`sandbox-host`, and `factory-sandbox`, using configured explicit SSH
identities and existing access. This permits observation only: neither unit
may publish Runner requests or ACKs, run defect probes, or change installed
configuration, services, storage, or firewall rules. Documentation/evidence
edits and the AGENTS.md Git delivery sequence are separately authorized
mutations; record them as source delivery, not as read-only host observations.
After these two units, Runner command execution uses the normal file-mailbox
workflow. The exception waives no failed gate or exact operational approval
checkpoint, and does not grant missing provider or privileged access. If a
required capability or readiness gate fails, record the blocker and stop the
serial sequence; do not seek a different observation route.

| Packet item | Upfront decision and collection/validation rule |
| --- | --- |
| Baseline and source transport | Record actual Mac/Gitea/GitHub/three-host revisions and worktrees using AGENTS identities; inspect installed component build/status and safe artifact/configuration identities independently of checkout HEAD |
| Operator access | Tomasz is the default owner above. Verify actual account/root and necessary provider access through approved read-only routes; record any explicit delegation and capability. An SSH login is not proof of provider API/firewall authority |
| Host capacity/storage plan | Inventory all host/device/filesystem/volume identities, PRD use, free bytes/inodes, cgroups/I/O and normal cache paths. Freeze the complete proposed budget and exact management-backup/Dev/QAS volume creation plan under design §6. Actual new UUIDs are collected after approved creation; they are not guessed beforehand |
| Isolated native fixtures | Name exact disposable storage/device scopes and maximum quotas, unique test namespace and loopback ports, root operator and cleanup manifest for L2/L3/L6/L7 drills. Provision only at the declared approved step. No real PRD root, key, authority, ownership marker or incident request is a destructive fixture |
| Network | Fix design endpoint/bind/port maps and preserve existing PRD access/source policy. Collect actual host/provider rule inventory; prepare exact rule additions needed by that fixed map and their rollback. Source-address restrictions use the verified intended control-plane/client addresses. An unverified NAT/source path blocks readiness, not a prompt to choose a different route |
| Producer and maintenance plan | Inventory all active PRD roots and actual request/ACK publishers/direct clients. Obtain each owner's verified pause/fence procedure and record a maintenance scheduling rule: Tomasz opens the declared window only after all pause controls and zero-work preflight pass. No guessed calendar time or paused producer is required before coding |
| Signing/PKI custody | Use only the design's protected owner/role/path mappings. Record who can authorize/generate/import local root keys and verify peer identities. Private keys remain outside Git and are never packet content; public pins/CSRs/digests are generated at their exact approved bootstrap step |
| Bugs | Resolve the BUG-021–024 prerequisite workflow below for actual serving routes; retain report-specific evidence and the original BUG-024 disposition. A `NEW` label is not a universal failure, and a later successful request does not dispose of original work |
| Approvals | Obtain scope/access/storage/network/producer decisions needed for the fixed rollout. Record the exact future checkpoint types in §3. Future bodies/hashes and short-lived approval receipts are generated just in time from verified preceding evidence; a broad readiness signature is not a signer mandate |
| Tool execution and evidence | Use native workspace edits/reads and the file-mailbox convention in `docs/llm-client-guide.md`, except for a separately recorded, task-scoped observation authorization such as the bounded first-two-unit route above. Verify all required routes before relying on them. Use the plan's S/E commit/delivery contract and Mac shared caches |

The implementer gathers discoverable values; it does not ask the owner to
invent hashes, epochs, quota UUIDs, observed capacity or serving revisions.
Missing existing credentials/access or a genuine failed gate is a blocker
reported with exact evidence and the prepared corrective action. That differs
from an unanswered design question. Once readiness passes, continue the fixed
sequence without asking whether to take the next ordinary implementation step.

## 3. Declared approval checkpoints

These are all known normal/incident approval categories. Tomasz reviews the
materialized, concrete package at its checkpoint. Several fully materialized
record bodies may be presented together; each still receives its own exact
mandate where the protocol requires it. This handbook does not preapprove
unknown future bodies, keys or fingerprints, and does not remove interactive
system authorization required by the design.

| ID | Concrete review package | When |
| --- | --- | --- |
| A01 storage/fixtures | Exact host/device/path, operation, quota/budget/retention, current descriptor/capacity inventory, creation/cleanup/rollback plan | Before native fixture provisioning; management backup before first L3 live write; real Dev/QAS volumes only after L3 and before L4/L5 |
| A02 networking | Exact before/after host/provider rules, public/private binds, source policy, access owner and rollback | Before corresponding fixture/public listener route change; revalidate immediately before its health gate |
| A03 bootstrap cohort | Exact contract/generation, four-host order, source/toolchain/input/binary tuples, root paths/roles, public mapping/pins, disable-all failure action and approved window | Before privileged cohort installation. Narrow initial key-generation/import scope is approved first, then its derived public pins are frozen/reviewed before dependent installation |
| A04 bootstrap PKI | Per-host one-use issuance mandate bound to exact contract/generation/installer tuple/pinned key/SANs; public request and receipt/package review | Each host's constrained bootstrap PKI step; disable exception after applicable health proof |
| A05 authority records | Exact closed record type/canonical body/digest, context/predecessor/evidence, host/lane/configuration, ledger/generation/validity | Before each authority signature: initial key/ledger records, policies, management and candidate manifests, authorizations, directives and lane acceptances; no wildcard mandate |
| A06 renewal grants | Exact existing context, predecessor, allowed generation/count cap, expiry and warning threshold | Before grant creation/successor. Renew automatically only within its unchanged finite bounds; no new approval for each allowed renewal |
| A07 normal PKI rotation | One exact issuance mandate; after actual request/receipt exists, a separate final package/peer-set-bound manifest/authorization review | L7 normal rotation or required approved management PKI change |
| A08 PRD/producer maintenance | Actual owners/writers, tested pause controls, retained-work disposition, fresh inventory, exact candidate/continuity/abort-or-repair package and window | Before BUG baseline maintenance, L6 fencing, and later affected PRD refresh |
| A09 registry recovery | Preserved failed evidence, verified authority/anchor facts, fresh descriptor inventory, exact reconstruction proposal | Only upon that incident; separately approved root maintenance, never a V1 mandate |
| A10 security rebootstrap | Outside-authority physical-root incident approval, forensic preservation, ingress disablement, new roots/trust and full reacceptance plan | Actual authority/anchor-loss incident only; routine L7 rehearsal uses isolated fixture identities |

Host attestations are constrained helper outputs, not additional human
signatures. The authenticated host-bound authority-time probe is the design's
approval-free signing exception. A fresh operational approval or a real access
failure can pause execution; no further architectural choice is delegated to
that checkpoint.

## 4. Common execution and test contract

Every row below inherits the main plan's full fresh-read, test, S/E evidence,
primary/mirror/three-host delivery and stop gates. The previous listed unit is
its prerequisite, plus any dependencies named in its row. Rows are the full
breakdown now; do not create another milestone plan before starting them.
If an unforeseen source fix exceeds the bounded slice, split only that active
unit into ordered `.a`/`.b` children with the same deliverable and a safe
checkpoint, record the amendment and keep all downstream IDs/order. Never split
to weaken an exit gate or code a future unit early.

Proof tiers used in the tables:

- **R:** read-only observation on the Mac and all three Ubuntu hosts; no Runner
  request publication or ACK, repair, install or restart. A bounded exception
  applies only to `LANES-PREP-00` and `LANES-L0`: the owner has authorized direct
  read-only Mac shell and SSH observations across `mac-control-plane`,
  `linux-host`, `sandbox-host`, and `factory-sandbox`, using configured explicit
  SSH identities and existing access. These two units must not publish Runner
  requests or ACKs, run defect probes, or change installed configuration,
  services, storage, or firewall rules. Their evidence records this exact
  scope. Documentation/evidence edits and their Git delivery are separately
  authorized mutations under `AGENTS.md`; they are not read-only observations.
  Other command execution uses the normal mailbox workflow, and all declared
  operational approval checkpoints remain in force.
- **H:** hermetic suites with temporary roots on all four host/platform tuples;
  full source suite/vet and relevant race checks accompany the focused gate.
- **N:** actual native task-owned processes/filesystem/TLS operations, using
  isolated roots and no installed Runner service changes.
- **F:** separately approved disposable privileged storage/cgroup/service/PKI
  fixture on each named platform; mocks cannot prove native enforcement.
- **B:** explicitly approved existing-baseline repair/verification; preserve
  original retained work and use supported exact-identity disposition only.
- **D:** real lane deployment and complete installed/live acceptance over the
  four-host cohort, including all three remote profiles.

L1-01 introduces fail-on-missing test dispatch and a coverage manifest keyed by
these unit IDs. Required additions, not commands claimed to exist today:

| Command contract | Meaning |
| --- | --- |
| `make test-lanes UNIT=<exact-unit-id>` | Focused hermetic assertions for the named unit; reject an unknown ID, missing assertions, skip-only result or wrong proof tier |
| `make test-lanes-process UNIT=<id> HOST_ID=<canonical-host>` | N-tier native process/filesystem proof on that actual host; isolated roots only |
| `make test-lanes-controls UNIT=<id> HOST_ID=<host> FIXTURE_MANIFEST=<approved-path>` | F-tier controls proof; validate exact fixture identities/approval before mutation; no production path override |
| `make accept-lanes LANE=<dev-or-qas-or-prd> CANDIDATE_ID=<verified-record-id>` | Full D-tier design §9 acceptance for the derived lane and verified candidate; exact active configuration/route identities, terminal/event/ACK/zero-work and defect gates; no foreign root supplied by caller |

Required existing `make test`, `make vet`, focused package tests and race suites
remain binding. Tests must report actual assertions/machines and reject an
unavailable prerequisite rather than succeed by skipping. The coverage manifest
maps every design §9 item and every plan deliverable to a row below. Native
fixtures have separately compiled test-only identities and loopback ports;
production binaries/installers retain the fixed namespace and candidate matrix.
Never ship test namespace overrides in release artifacts.

A live transaction is one continuous operational unit. No Git/evidence
delivery pause is inserted between its fence, install, commit, reopen and
acceptance steps, or between members of its bootstrap cohort. Serial host
steps below are internal checkpoints, not separate S/E unit boundaries.
Maintain valid already-approved renewal grants throughout; if renewal or a
gate fails, use the design's exact closed/abort/repair path. Source S/E commits
cannot silently change a candidate already being promoted. Stage all source
and fixtures before the live unit starts.

## 5. Preparation and runtime-bug prerequisites

These units are planned work, not claims that the bugs are fixed. They are
included in the implementer's assignment when execution of this whole plan is
authorized; this documentation change does not execute them. Source fixes and
report-specific verification use their own bug evidence and delivery. A
verified non-reproduction can close a prerequisite with exact serving/capability
proof while leaving an unexplained historical report open. No needed positive
capability is waived; original retained work still requires its own disposition.

| Unit | Deliverable | Required proof / safe completion |
| --- | --- | --- |
| `LANES-PREP-00` | Collect readiness owner/access/source/fixture/network/producer plans using §2; retain all exact approval checkpoints | R; complete discovery and concrete external packages, no real provision or lane code; use only the bounded direct read-only observation exception above; pending required access is an explicit stop |
| `LANES-L0` | Full unsigned read-only capacity, routes, mailbox/bridge/producer ownership, serving identities and bug applicability inventory | R; all four hosts, design §6 equations/proposed budgets, existing PRD health and retained-work evidence; use only the bounded direct read-only observation exception above; no later approval or runtime PASS inferred |
| `LANES-B024-01` | Trace exact original Mac close/ownership state; teardown/reconciliation source regressions and proven correction | R+H; preserve original held ACK and cursor; exactly-once terminal teardown, truthful loss and no replay |
| `LANES-B024-02` | Supported original owner disposition and fresh installed Mac one-off teardown/capacity acceptance | B with A08; original exact retained pair has explicit proof-based disposition before an affected refresh; fresh success has closed teardown, ACK and zero-capacity proof; later jobs cannot dispose of old work |
| `LANES-B021-01` | Timeout normalization/dispatch source regression; repair proven cause under existing contract | H; alias/explicit conflict, canonical idempotency and policy clamp preserve deadline; no inferred installed fix |
| `LANES-B021-02` | Verify actual baseline deadline after any separately approved required install | B; fresh five-second controlled finite hold stops before natural success within documented grace; truthful terminal/ownership, exact ACK and zero-work |
| `LANES-B022-01` | Diagnose and correct proven explicit-limit protocol/configuration/diagnostic incompatibility, preserving positive capability | H; permitted matching nested/alias limits admitted; conflict/forbidden requests precisely rejected before delivery |
| `LANES-B022-02` | Actual baseline permitted explicit-limit and policy-negative acceptance | B; independently bounded probe, enforced deadline and supported release; safe rejection alone is not positive PASS |
| `LANES-B023-01` | Managed-session capability/dispatch/lifecycle regressions and proven correction | H; create/submit/read/cancel/close contract and policy rejection; do not infer behavior from a one-off lookup |
| `LANES-B023-02` | Actual managed create → submit → observed hold → exact cancel → terminal → close/release | B; fresh identities on every affected serving remote route; complete output/ACK and zero-work; unknown capability stops |
| `LANES-READY` | Seal complete readiness packet and prerequisite evidence for all L1–L7 work | R/document review; all predictable owner/access/change-plan inputs resolved, no unresolved applicable baseline prerequisite, complete ordered ledger and scheduled approval packages; actual later generated receipts remain checkpoint outputs |

BUG-024 comes first because later dispatcher/limit fixes may need an affected
Mac service refresh. Do not wait for those installs to investigate or dispose
of the original retained work. Its own approved guarded recovery/install must
prove the required ownership boundary; missing proof stops all affected refreshes.

No defect probe runs in L0. Safe probes have an independent finite natural
bound, private workspace and bounded output. If a baseline repair invalidates
an earlier relevant bug result, rerun that earlier gate before `LANES-READY`;
record the dependency loop without pretending old evidence applies. No lane
candidate exists yet. Later candidate regressions follow the normal correction
and fresh Dev → QAS → PRD chain.

## 6. Complete L1–L3 implementation sequence

### L1 — contracts (design §§2–4, 8–9)

| Unit | Deliverable | Additional focused proof |
| --- | --- | --- |
| `LANES-L1-01` | Unit-test dispatcher/coverage manifest and V3 schema/namespace derivation | H; missing/skip-only gate fails; V2 unchanged, exact roots/labels/ports, foreign lane/local Dev/QAS rejection |
| `LANES-L1-02` | Profiles, contexts, endpoints, controllers and credential binding | H; all three Mac profiles/one Linux profile; every permitted environment/target resolves within its lane |
| `LANES-L1-03` | Descriptor-based root/storage handoff validator | H+N; five-child layout/ancillary entries, ownership/mode, alias/symlink/nesting and volume identity refusal |
| `LANES-L1-04` | Durable mailbox registry, five states, epochs and legacy reservations | H; collision set, CAS, fsync/restart, same-lane successor history, no second SQLite runtime authority |
| `LANES-L1-05` | Successful fresh/replacement Dev/QAS transition graph | H; closure before quiescence, zero work before staging, higher candidate epoch, remote-first reopen and acceptance/archive |
| `LANES-L1-06` | Failed-open, exact-incumbent recovery and abort graph | H; close/quiesce before recovery/archive, fresh authorization/epoch, no pointer restore or uncertain-work deletion |
| `LANES-L1-07` | Registry-loss/corruption/rollback stop and reconstruction validation | H; verified root facts/descriptors only, collision uniqueness, explicit in-flight disposition and no inferred V2 restart |
| `LANES-L1-08` | Native/workspace publisher fencing for request and ACK pairs | H; shared lock through JSON/marker/fsync, exclusive snapshots, race/crash and raw-writer stop gates |
| `LANES-L1-09` | Reversible PRD fence and descriptor/digest deferred ledger | H; durable fencing before client/service action, V2 absence proof and fence-aware abort preserve pairs |
| `LANES-L1-10` | Provisional admission contexts and direct/private/queued mutation hooks | H; exact identity versus epoch, persistence lock/transaction, concurrent submit/close fail closed |
| `LANES-L1-11` | Mac/local-intent/importer/ACK admission and safe status | H; all remaining surfaces, restart closed, safe V3 attestation, V1 response unchanged |
| `LANES-L1-12` | Exclusive PRD group, maintenance and typed linked recovery | H; closure/quiescence gates, one active child, monotonic epoch and fresh child acceptance |
| `LANES-L1-13` | Post-commit repair and complete contract integration | H; repair closure/quiescence/zero-work/handoff, sequential child replacement, V2 abort fence release; full L1 coverage |

### L2 — installation and controls (design §§2, 5–6)

| Unit | Deliverable | Additional focused proof |
| --- | --- | --- |
| `LANES-L2-01` | Fixed lane/management installers, templates and role packages | H+N; native plist/systemd validation; root/peer/socket/key identity, preserved authority state, no external-root creation/PRD mutation |
| `LANES-L2-02` | Dedicated queued SSH listeners and forced wrappers | H+N; native sshd validation; exact defaults/ports/socket, command/subsystem/forward refusal; preserve unrelated port-22 keys |
| `LANES-L2-03` | Direct and management endpoint/TLS material validation | H+N; literal-IP SAN/URI/controller maps, lane trust separation and real local TLS negatives; public reachability waits for actual listeners |
| `LANES-L2-04` | PKI local-key/CSR/package mechanics and issuance interfaces | H+N; wrong host/SAN/CSR/package, bootstrap-after-health and retired-leaf refusal; protected signed enforcement follows in L3 |
| `LANES-L2-05` | Candidate-attested log capture wrapper and stream wiring | H+N all hosts; flood/crash/rotation/failed-start, 64 MiB files/576 MiB total, durable drop counter and null direct output |
| `LANES-L2-06` | Capacity scopes, envelope validator and durable byte admission | H; PRD floor, normal caches/backup/snapshots, Linux 30% and Mac max(20 GiB,15%) floors, changed identity and false compute claim refusal |
| `LANES-L2-07` | Real Linux slice/descendant, disk/inode/cache controls | F on each Ubuntu; actual listener/session/wrapper/bridge cgroups; bounded CPU/memory/tasks/I/O and quota/log/cache saturation; PRD unchanged |
| `LANES-L2-08` | Real Mac APFS/quota/cache/snapshot/headroom controls | F on Mac; approved disposable volumes, quota fills/byte refusal/wrong mount, bounded logs/backups, unchanged PRD and no compute guarantee |
| `LANES-L2-09` | Preflight/dry-run and staged-start rollback integration | H+F; foreign resource/credential selection refused, fixture-only rollback, complete L2 enforcement coverage and original services unchanged |

Fixture provisioning for L2-07/08 is the A01/A02 package settled in readiness,
not early creation of real Dev/QAS roots. Use isolated native devices/volumes,
test units and loopback listeners. L2 implements validators/templates/crypto
mechanics; L3 supplies canonical signed records, protected key/peer enforcement
and authenticated distribution. No normal signed policy or production
management baseline is issued in L2.

### L3 — release and host management (design §7)

| Unit | Deliverable | Additional focused proof |
| --- | --- | --- |
| `LANES-L3-01` | Canonical V1 envelope, JCS, domain-separated ID/signature and role map | H four platforms; exact golden vectors, unknown role/type/noncanonical byte refusal |
| `LANES-L3-02` | Build/candidate/management closed schemas and complete matrices | H; all tuples exactly once, source/toolchain/input/digest binding, no missing/extra component |
| `LANES-L3-03` | Promotion, admission, acceptance, capacity and PKI payload schemas | H; closed field combinations, scope/host/lane/transition/predecessor/type restrictions |
| `LANES-L3-04` | Authority/host key sets, revocation ledger and time contract | H; skipped/forked/revoked/self-revoking/invalid-overlap records and bootstrap key mapping |
| `LANES-L3-05` | Root anchor sync/high-water state and stale-ledger recovery | H+N; fsync crash/rollback, key/ledger chain and expired-ledger fail-closed behavior |
| `LANES-L3-06` | Protected peer identities and constrained observe/transport endpoints | H+N; Mac audit token/label/code and Linux UID/unit/cgroup/executable checks; arbitrary-root/shared-UID refusal |
| `LANES-L3-07` | Sealed fixed builder, independent build attestation and outbox | H+N; caller cannot choose path/input/evidence, alter sealed output or access keys |
| `LANES-L3-08` | Root verified cache and distinct same-host backup recovery | H+F; descriptor/hash/chain checks, saturation, rehydration and missing-cache-plus-backup closed stop |
| `LANES-L3-09` | Constrained install/capacity/aggregate-ingress attestations | H+N; independent observed inventory/evidence, no caller values, complete ingress surfaces |
| `LANES-L3-10` | Root signer/agent/approval exact mandates and replay journal | H+F; exact body/context, one use, preserved root state, peer/key denial and all authority record roles |
| `LANES-L3-11` | Finite directive/global-ledger grants and host nonce-time probe | H+N; two renewals, concurrent CAS, gap/fork/cap/expiry/warning, no authority expansion |
| `LANES-L3-12` | Issuer/installers normal and bootstrap issuance chains | H+F; local key-only custody, exact host/tuple/CSR/request/receipt, no replay/key export/post-health bootstrap |
| `LANES-L3-13` | Fixed Mac local and Ubuntu mTLS release-record API | H+N; exact closed API, peer/URI/IP checks, atomic idempotent record cache; no generic path/source/artifact/shell/SQLite access |
| `LANES-L3-14` | Authority and host capacity-policy integration | H+F; both scopes, same evaluation/live remeasurement, quota/cgroup/cache/backup identity and low-water refusal |
| `LANES-L3-15` | Signed admission and full L1 transition integration | H+N; fanout crashes, closure/quiescence/zero-work, higher epochs and exact post-commit handoff |
| `LANES-L3-16` | Management update/rollback and PKI rotation implementation | H+F; preserve root state, serial 900/600-second guard, dual trust, old-leaf denial and verified rollback |
| `LANES-L3-17` | Constrained bootstrap packages and abandon/retry protocol | H+F; exact contract/pins/mapping, per-host exception disable, whole-cohort abandonment and no generation reuse |
| `LANES-L3-18` | Complete platform/no-bypass rehearsal and live package preparation | H+F; all prior L3 assertions/platforms and source gates; fresh unsigned L0 and exact currently materializable operation packages, plus fixed A01–A06 schemas/order for later-generated bodies; no real bootstrap yet |
| `LANES-L3-19` | One continuous privileged bootstrap cohort | Approved live management operation, no lane services: Mac complete row/local-socket gate → linux-host → sandbox-host → factory-sandbox mTLS/time gates; per-host PKI mandates/receipts and fresh signed measurements; verify all anchors/mappings, initialize approved key/ledger contexts, then four management policies. Any partial failure abandons/disables the entire cohort as design requires |
| `LANES-L3-20` | One continuous first host-management baseline operation | Build/attest complete management matrix; exact manifest and tagged-bootstrap authorizations; install/health/attest Mac then each Ubuntu in fixed order; preserve state and establish complete compatible baseline, with design rollback/failure action |
| `LANES-L3-21` | Complete immutable lane-runtime candidate and L3 closeout | All host/component builds/attestations/verified caches/backups; signed candidate names exact healthy management manifest; no lane-runtime policy, lane authorization, lane root or service yet |

L3-19/20 are coordinated operational transactions with per-host internal
checkpoints, not four independently delivered phases. All their source and
rehearsal work is already complete. Exact A03–A06 bodies/keys/policies must be
materialized and approved in the design's required order; the operator is not
asked to design a new bootstrap flow. Record safe final state or the exact
failed-closed disposition before a unit's S/E closeout.
L3-18 cannot supply public pins, CSR receipts, measurements or record/grant
predecessors that only arise in L3-19/20. Generate, freeze and approve those
values immediately before their dependent actions; the predefined checkpoint
schema is not a substitute for the actual receipt or mandate.

## 7. Complete L4–L7 rollout and operation sequence

| Unit | Deliverable | Required installed/native proof and completion |
| --- | --- | --- |
| `LANES-L4-01` | Provision approved real Dev/QAS service volumes after L3; fresh Dev package and isolated complete Dev rehearsal | A01/A02; descriptor/layout/UUID/quota handoff, fresh full-envelope L0 and compatible management baseline; F rehearsal proves install/open/accept or closure/quiescence failure branches without touching PRD |
| `LANES-L4-02` | One continuous Dev install → remote-first open → full acceptance/archive | D all hosts/profiles; target closed/transport-ready/open; positive mailbox/queued/direct/BUG gates, local rejection, both directed legacy-PRD denial pairs, real caps/saturation, unchanged PRD and signed Dev acceptance |
| `LANES-L5-01` | Fresh QAS readiness, exact Dev artifact/provenance selection and isolated QAS rehearsal | Verify QAS storage and current Dev+PRD use; A01/A02 as applicable; F failure/restart/expiry/refusal tests, no change to accepted candidate or other lanes |
| `LANES-L5-02` | One continuous QAS install → open → full acceptance/archive | D all hosts/profiles; same candidate bytes, complete positive/BUG/ACK/zero-work gates, six directed foreign-lane denial pairs, slice/log/resource proof, unchanged Dev/PRD and signed QAS acceptance |
| `LANES-L6-01` | Isolated full PRD migration and recovery rehearsal; final exact maintenance package | F; every fencing/deferred snapshot/install/commit/fanout/reopen crash point, V2 pre-commit abort versus V3-only repair, original-work disposition and tested continuity restore; A08 package prepared from real read-only inventory |
| `LANES-L6-02` | One continuous approved PRD migration → reopen → full acceptance/archive | D with A08: fresh pause/drain/fence/zero work, tested PRD-only bundle, immediate fresh authorizations, candidate-closed/log-cap proof, commit cutoff, remote-first open; remote BUG gates plus retained Mac-local teardown/capacity proof, all routes/ACK/denials, signed fresh PRD acceptance or exact abort/repair-closed disposition |
| `LANES-L7-01` | Capacity/log/drop telemetry and directive/global-ledger warning operations | N+F; warning threshold, last issuance, same-context successor, cap/expiry and planned close; fresh measured envelopes and no evidence/cache reclamation |
| `LANES-L7-02` | Verified cache rehydration and absent-cache/backup drill | F; full candidate/build chain and same-host backup validation, wrong/missing bytes closed; no checkout rebuild of an old candidate |
| `LANES-L7-03` | Serial host-management update/rollback drill and operating procedure | F all hosts; correct policy/predecessor, 900-second lease margin/600-second update-or-rollback, root-state preservation and no lane mutation |
| `LANES-L7-04` | Normal PKI issuance/overlap/rotation/rollback drill and procedure | F; two-stage exact receipt approval, new-chain mTLS/time proof, old chain-valid leaf denied before retirement, no peer-set expansion or key export |
| `LANES-L7-05` | Dev/QAS refresh and failed replacement/exact-incumbent recovery drill | F; one active operation, incumbent closure/quiescence, higher target epoch, partial-open closure, fresh acceptance and no pointer restore; other lanes unaffected |
| `LANES-L7-06` | Later PRD maintenance, linked restore/forward-repair and post-commit handoff drill | F; all group/parent/child/epoch/zero-work rules, fresh child acceptance, retry/archive and V3-only post-commit recovery |
| `LANES-L7-07` | Registry idle/mid-transition/fencing/repair loss/reconstruction drill | F; verified root facts and fresh descriptors, retained evidence, no inferred pointer/group release/V2 restart; A09 procedure staged as a fixture |
| `LANES-L7-08` | Isolated authority/anchor-loss security rebootstrap drill and final handoff | F; A10 fixture, new roots/trust and full acceptance chain, no old state import/V2 revival; complete runbook, alerts, per-host current identity, unit/coverage/evidence reconciliation and all group closeouts |

L7 failure drills use isolated roots/authority keys/processes. They do not
damage accepted live lanes just to demonstrate recovery. Production monitoring
is read-only; any later real maintenance uses a new exact scoped operation,
fresh preflight and the same already-defined procedure. L7 closes with the
runbook/drill handoff, not an impossible claim that future operation is finished.

## 8. Deterministic failure handling and final acceptance

| Condition | Required action; no new design discussion |
| --- | --- |
| Missing access, external handoff, unsupported enforcement or insufficient capacity | Stop the dependent live path, preserve facts, report the exact failed prerequisite and prepared corrective package; do not weaken the four-host/resource contract |
| Source test failure or candidate regression | Correct the proven defect in the active scoped unit; retest and deliver a new S/E. If a candidate already exists, create a new complete candidate and repeat the Dev → QAS → PRD chain |
| Unknown/retained runtime work | Preserve identities, output and held ACKs; use only the supported owner-disposition path. No replay, speculative ACK, DB edit or manual capacity release |
| Dev/QAS stage/open/acceptance failure | Exact design closure/quiescence/failed-closed recovery or abort; other lanes remain untouched |
| Initial bootstrap partial failure | Disable/abandon the entire cohort; a fresh exact-approved contract/generation/key/receipt chain and fresh unsigned L0 are required before retry |
| Initial PRD failure before commit | Fence-aware V2 abort only, evidence and security anchor preserved, group released only after verified restoration/fence release/archive |
| PRD failure after commit, or later V3 recovery | V3-only closure/quiescence/repair or typed linked restore/forward-repair as appropriate; no V2 revival or parentless/concurrent recovery |
| Lost registry versus lost root trust | Use A09 verified registry reconstruction or A10 isolated security rebootstrap respectively; never conflate the two |

Final delivery requires the checked-in ledger to link every completed unit and
all L0–L7 aggregate evidence, current exact per-host source/installed identities,
the complete immutable promotion chain and design §9 proof, supported retained
work disposition, and the operating/recovery handoff. No skipped gate is PASS.
Planning can be complete while execution is still blocked by a real measured
condition or pending exact approval; report that condition precisely rather
than claiming either a missing specification or a successful implementation.
