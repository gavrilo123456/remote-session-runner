# Phased implementation plan: Dev, QAS, and PRD release lanes

**Status:** reviewed implementation plan. It is deliberately serial. A failed
or incomplete gate stops the next phase and leaves the existing unsuffixed
Runner unchanged.

## Operating rule for every phase

All tracked changes originate in the authoritative Mac `dev` checkout. For a
completed phase, inspect the intended diff; commit it on `dev`; push to local
Gitea; verify the automatic GitHub mirror has the same SHA; and fast-forward
each clean Ubuntu `dev` checkout before host validation. Record the Mac,
Gitea, GitHub, `linux-host`, `sandbox-host`, and `factory-sandbox` SHAs, source
tests, installed tests, live evidence, and rollback state.

Never edit tracked source in an Ubuntu checkout. Never use a lane installer to
repair active or uncertain existing work. Preserve mailbox evidence and use a
supported terminal disposition before any relevant service refresh.

The phrase “three-machine” is historical. The current lane cohort is the Mac
plus all three accepted Ubuntu profiles. Every live phase covers all of them.
Canonical release-record host IDs are `mac-control-plane`, `linux-host`,
`sandbox-host`, and `factory-sandbox`.

## L0 — read-only capacity, route, and ownership baseline

**Deliverable:** a signed-off read-only inventory for the Mac and all three
Ubuntu hosts.

1. Record bytes/inodes, memory, CPU, existing listeners, cgroup-v2 hierarchy
   and per-device I/O capability, host-management build/release scope, service
   roots, workspaces, output/log retention, global-journal consumption, build
   staging, release-cache budget, normal Ubuntu `go env GOCACHE GOMODCACHE`
   paths/use, backups, existing PRD public-direct reachability, planned
   release-transport and Dev/QAS endpoint firewall/NAT/listener prerequisites,
   queued-bridge/listener identities, and current health/build revision. Record
   each not-yet-provisioned `:9443`, Dev, or QAS route as deliberately absent;
   L0 does not require it to connect.
2. Record all active, queued, uncertain, and unacknowledged work. Do not ACK,
   retry, recover, remove, or restart it as part of this phase.
3. Record every active mailbox root and producer, including external roots,
   together with its current owner, normalized path, descriptor identity,
   mount/volume identity, retained-state condition, and every declared
   retained-ancillary top-level entry's path/type/descriptor/owner/mode.
4. Derive, but do not sign or publish, the two policy-scope types per host: one
   proposed `host_management` policy and one proposed `lane_runtime` policy for
   each contemplated lane. The `host_management` policy names only measured
   management scope, PRD safety floor, cache/staging/log budget, and headroom; it
   can support the L3 bootstrap before lane storage exists. Each `lane_runtime`
   policy names one lane plus the complete PRD/Dev/QAS/management envelope. On
   Linux, Dev/QAS policies name their lane-slice caps, while a PRD policy records
   its measured unsuffixed host-baseline scope/floor and any separately
   configured PRD controls; all name measured capacity, bounded
   build/release/anchor/authority scope, per-device I/O limits, disk/inode
   reservations,
   normal-Go-cache budget, and a 30-percent free byte/inode floor. Prove the
   full envelope preserves PRD when a Dev or QAS slice reaches its hard cap. On
   the Mac each lane-runtime policy additionally
   names verified Dev/QAS APFS volume UUIDs/mountpoints/quotas, byte/log/cache
   budgets, snapshots, host headroom, and shared-cache budget. A PRD policy
   records either the existing-root `host_free_space_budget` or a separately
   approved `apfs_quota` mode. Prove that
   non-purgeable usage plus snapshot use plus configured maximum Dev/QAS quotas
   and shared cache budget leaves `max(20 GiB, 15% of Data-volume capacity)`
   unallocated, and that Runner-managed snapshots are within their 10 GiB
   aggregate budget. Record `compute_reservation: none` rather than claiming a
   LaunchAgent CPU/RAM/PID reservation.
5. Record the separately approved storage-preparation handoff manifest for every
   future Dev/QAS service-root and enabled external mailbox volume, including
   the exact five-child mailbox layout and `initialized_empty` state, or record
   it as absent. Absence may pass a pre-L3 bootstrap inventory but prevents a
   lane-runtime policy and therefore L4/L5. L0 does not create, repair, resize,
   or mount a volume.
6. Stop if an existing PRD route is degraded, a planned route prerequisite is
   infeasible or ambiguous, a host has insufficient capacity, listener ownership
   is ambiguous, a quota/cgroup/cache/log bound cannot be enforced, a snapshot
   cannot be accounted for, the Go-cache budget is absent, a declared storage
   handoff is invalid, or retained work would be touched. L3 validates real
   `:9443` mTLS after bootstrap; L4/L5 validate the new lane routes after their
   listeners exist.

**Exit gate:** all four machines have a green inventory, with planned routes
either feasible or recorded deliberately absent. This phase creates no lane
service, key, certificate, mailbox, network rule, or deployment.

### Required external precondition — Dev/QAS storage preparation

Between L3 and L4, and only after separate explicit host-storage approval, an
administrator pre-provisions every planned Mac Dev/QAS service-root APFS volume
and every enabled external `mailbox-dev`/`mailbox-qas` APFS volume. The handoff
manifest contains lane, canonical mountpoint, volume UUID, quota, owner/mode,
the exact root plus `inbox`, `outbox`, `events`, `acks`, and `diagnostics`
layout, `initialized_empty` state, descriptor snapshot, and creation receipt.
The storage administrator creates only those empty owner-only directories; it
creates no request, ACK, response, event, diagnostic, service, route, key, or
release record. This is not installer work. A fresh L0 immediately before L4
verifies the manifest by descriptor; a missing, resized, wrong-path, unsafe,
wrong-layout, or unexpected-nonempty volume stops deployment.

## L1 — V3 configuration, ownership, migration, and admission contracts

**Deliverable:** hermetically tested `version: 3` configuration, state, and
admission contracts. No installer or live service changes occur in this phase.

1. Add required `release_lane: dev|qas|prd` to V3 Mac and Linux documents.
   Retain V2 as an untouched compatibility format until scheduled PRD migration.
   Reject a Dev/QAS `kind: local` target and prove their submitted scripts run
   only on a remote target.
2. Derive and validate the fixed roots, labels, units, sockets, ports,
   configuration files, logs, staging paths, mailbox roots, target profiles,
   controller identities, endpoint profiles, and credential attestations.
   Reject a source-supplied path, label, port, profile, or secret path that does
   not derive from the selected lane.
3. Add a Mac-wide owner-only lane registry and lock that atomically fences
   mailbox-root ownership across all lane databases, durable one-active
   operation records for Dev and QAS transitions, plus the exclusive PRD
   operation-group lock that serializes initial migration, V3 maintenance, and
   at most one active typed linked recovery child (`v3_restore` or
   `v3_forward_repair`) with durable replacement history. The Dev/QAS record
   must implement the complete graph: fresh `prepare → candidate_closed →
   reopen_prepared → reopen_authorized → target_opening → reopened →
   acceptance_pending → accepted → archived`; replacement `prepare →
   incumbent_closure_pending →` that same sequence; fresh pre-target-opening or
   post-closure replacement failure `failed_closed → aborted → archived`; and a
   failure from `target_opening`, including partial target-open fanout, only
   through `target_closure_pending → failed_closed`, then either exact-incumbent
   higher-epoch recovery or closed abort. A failed `incumbent_closure_pending`
   operation may only retry/renew/safe-stop until complete closure; it cannot
   abort, archive, release, or allocate recovery. Implement
   `legacy_prd_reserved`, `prepared`, `active`, `superseded`, and `retired`
   states; restart/retry/reconciliation; lock-held same-lane configuration
   successor/rollback; and legacy-PRD reservation refresh after reviewed V2
   maintenance. Persist/revalidate descriptor-derived filesystem identity and
   Mac volume UUID/mountpoint. Fixture renamed, bind-aliased, same-path
   replacement, symlink, unsafe ancestor, ownership, non-`0700` mailbox trees,
   allowed retained ancillary entries, and undeclared or replaced ancillary
   entries. Fixture Dev/QAS operation-record CAS, concurrent-refresh refusal,
   incumbent/target-closure crash/retry, target-start failure, partial-target-
   open failure, post-open acceptance failure, higher-epoch exact-incumbent
   recovery and recovery-abort, closed abort, and archival/release only after
   sealed evidence before another transition can allocate.
4. Add safe V3 health/status attestation for lane, source revision, artifact
   digest, configuration digest, schema version, and safe admission status.
   Keep the V1 mailbox response schema unchanged.
5. Add positive fixtures and negative tests for foreign lane paths, profiles,
   ports, keys, certificates, controller maps, endpoint bindings, mailbox
   roots, lane-like request fields, and an allowed environment/target pair that
   resolves to another lane's transport or endpoint.
6. Add the reversible PRD migration-fence controller, distinct from retirement.
   It must durably enter `fencing` and record per-host V2 state **before** the
   first client pause, service stop, disable, or listener proof. Native and
   supported workspace publishers hold a shared lock from fence check through
   either request or ACK JSON, marker-last ready file, and directory fsync;
   the controller takes the exclusive lock for paired request/ACK snapshots and
   marker writes. Implement the descriptor/digest deferred ledger, V2
   stop/disable/absence proof, raw-writer/capability stop gate, and recovery
   behavior for every crash boundary. Fixture V2 cannot mutate after durable
   `fenced`, deferred request and ACK pairs remain intact, and no half-fenced
   restart silently re-enables V2.
7. Define the V3 ingress-admission state interface for **every** lane and make
   every mutation path consult it under the same admission lock/SQLite
   transaction that persists mutable state: direct HTTPS, queued
   forced-wrapper/private API, Mac local API/local-intent, mailbox request
   import, and ACK import. Implement the provisional fail-closed local state,
   per-host epoch allocation, `active_runtime_closure` versus
   `candidate_admission` identity contexts, pending-candidate pointer switch,
   and durable migration-fence interface, but do not call it signed enforcement
   or distribute directives yet. Fixture the state-machine shape,
   closure-before-candidate ordering, same-epoch identity-switch rejection,
   fence-versus-submit, local/queued/importer races, and restart fail-closed
   behavior. L3 supplies the release-record schema, keys, anchor, transport,
   signed directive validation, renewal, and fanout tests.
8. Implement the durable PRD operation-group and V3 transition contracts before
   any live phase: `v3_maintenance` closure-pending/closed-verified/quiesce/
   stage/commit/reopen states with incumbent-closure then higher
   candidate-admission epochs;
   `restore_required`; typed `v3_restore` and `v3_forward_repair` children;
   `post_commit_repair_required` with repair-closure-pending/repair-closed;
   fresh UUID/epoch allocation above host anchor
   high-water; child failure evidence/archive and sequential replacement; V2
   abort release only after fence-release evidence; and release of the group
   only after the final child and parent, or sealed post-commit repair handoff,
   archives. Fixture every CAS transition, restart, crash, partial directive
   fanout, closure-pending retry/no-child gate, child replacement, V2 abort
   retry, post-commit repair handoff, and
   rejection of a concurrent or parentless PRD recovery operation.

**Exit gate:** schema, root-registry, migration, deferred-pair, provisional
admission-state, status, and isolation fixtures pass. No installer or live
service changes occur.

## L2 — lane-aware installation, bridge, network, logging, and resource controls

**Deliverable:** fixed lane-aware installers and templates, tested without a
live lane installation.

1. Generate only the documented Mac LaunchAgent labels/plists and Linux
   `runnerd`, lane-slice, dedicated queued-SSH listener, host-management,
   release-transport, security-anchor, root, socket, lock, staging, and log
   templates. Include the Mac-only root `runner-release-authority-signer`,
   `runner-release-authority-agent`, and
   `runner-release-authority-approval` packages, their distinct audit-token/code
   identity requirements, signer/anchor authority-state socket modes, and the
   persistent root authority-state preservation rule for management update or
   rollback. A lane invocation refuses a mismatched or existing different-lane
   resource. It does not create or repair any external mailbox root or APFS
   volume.
2. Make Dev/QAS queued bridges lane-specific with root-owned
   `/etc/remote-session-runner/ssh/dev|qas/` configurations, independent host
   keys, `AuthorizedKeysFile`, fixed `ForceCommand`, fixed known-host
   fingerprints, and public/private ports `2224`/`2225`. The listener uses the
   restricted bridge-only OpenSSH settings in the detailed design, attaches to
   its lane slice, and a fixture proves listener, session child, forced wrapper,
   and bridge child remain in that slice despite login/session handling. Keep
   global port 22 and `/home/ubuntu/.ssh/authorized_keys` untouched except for
   recording and later converting its one protected `legacy-prd` bridge entry
   under the host-global lock. Fixture exact legacy identity preservation before
   and after L6 conversion.
3. Add the fixed direct `{lane, remote-profile}` endpoint/bind table and the
   separate host-specific release-record `:9443` table, firewall/security-rule
   preflight, direct per-lane server/client mTLS material, and release-transport
   CA/trust-bundle/leaf material. The root-owned CA private key stays outside
   Git and every lane root; the Mac client leaf is readable only by the authority
   agent and each Ubuntu server private-key file is mode `0600`, installed by
   root, and owned only by its own dedicated non-login release-transport service
   UID. Define URI/IP
   SANs, principal maps, safe fingerprint attestation, rotation, and revocation.
   Fixture ordinary TLS verification against every literal public-IP URL; no
   name-verification bypass is acceptable.
4. Add `runner-log-capture` as a candidate-attested component on every host.
   Dev/QAS Linux units use the wrapper and set systemd stdout/stderr to `null`;
   Dev/QAS LaunchAgents use it and direct launchd stdout/stderr to `null`. The
   wrapper enforces a 64 MiB active file, seven 64 MiB rotations, one 64 MiB
   temporary file, 576 MiB total, and a durable `dropped_log_bytes` counter.
   Fixture a log flood, crash during rotation, and a failed wrapper start. V2
   PRD global-journal/append-only logging remains untouched until L6, when the
   unsuffixed V3 PRD units must pass the same wrapper/null/cap fixture before
   `committed`.
5. Implement the scoped capacity-policy validator and staged controls.
   A `host_management` policy can authorize only its bounded build/release/anchor/
   authority-signer/agent/approval scope and host cache/staging/headroom; a
   `lane_runtime` policy is required for every lane installer and carries the
   full PRD/Dev/QAS/management envelope.
   Linux uses cgroup-v2 hard caps for the full Dev/QAS lane slice plus a
   separately bounded host-management scope, lane disk and inode reservations,
   and the selected account's normal bounded Go-cache paths; no installer cleans
   those shared caches. Mac validates pre-provisioned quota-limited Dev/QAS
   volumes and, for PRD, either the existing-root `host_free_space_budget` or
   separately approved `apfs_quota` mode; it also validates byte admission,
   release/Go-cache budgets, 10 GiB snapshot budget, the documented
   `max(20 GiB, 15%)` floor, bounded logging, and background priority policy
   while recording `compute_reservation: none`. Deliberately
   fill each isolated lane volume/cache/log allocation and each Linux
   listener/bridge and host-management build/transport/authority scope; prove new
   admission refuses safely while PRD remains usable. Missing or wrong scope,
   policy, volume, quota, cgroup/slice membership, I/O device cap, Go-cache
   bound, external mailbox handoff, cache/snapshot accounting, or log-capture
   boundary fails closed.
6. Add dry-run/preflight commands proving Dev/QAS installers and configured
   controllers cannot select a PRD path, claim its mailbox root, alter port-22
   legacy SSH authority, use its certificate, escape their slice, or exceed the
   signed policy. These checks do not claim containment from an arbitrary
   same-user submitted script; that requires the VM/host boundary stated in the
   design.

**Exit gate:** installer/resource tests prove correct derivation, no cross-lane
bridge or certificate mutation, literal-IP network/mTLS validation, Linux
slice/disk/cache refusal, Mac quota/headroom/log admission refusal, and rollback
after a staged-start failure. The unsuffixed service is not restarted.

## L3 — release records, host-management baseline, and immutable artifacts

**Deliverable:** a hermetically verified closed V1 protocol followed by a
compatible root-owned host-management baseline on every physical host. This
phase installs no Dev/QAS/PRD lane service or mailbox.

1. Implement and hermetically test `runner-security-anchor-helper`,
   `runner-release-builder`, `runner-release-transport`, and the Mac-only
   `runner-release-authority-signer`, `runner-release-authority-agent`, and
   `runner-release-authority-approval`, their root bootstrap packages,
   root-owned sockets/state, sealed unprivileged build staging, constrained
   build/install/ingress-attestation operations, and no-bypass peer-credential
   checks. The signer must be the only authority-key operation for every
   release-authority-signed closed-table record type; its private keys,
   mandates, consumed-approval journal, accepted-record IDs, and verified
   transition/high-water state remain root-owned outside artifacts, lane roots,
   caches, and continuity bundles. The authority agent is the only audit-token/
   label/code-identity-validated signer peer; the approval tool has no signer
   socket and creates an exact, replay-safe root mandate only after interactive
   authorization. Implement finite root-state renewal grants with exact context,
   generation maxima, issuance cap, expiry, fsync high-water update, and no
   added revocation/key/lane/host/phase authority. Implement the approval-free,
   one-shot authority-time-probe operation only for a fresh authenticated
   host-bound challenge. Fixture shared-UID and arbitrary-root socket attempts,
   registry mutation, unapproved/wrong-body/wrong-type/expired/replayed
   approvals, grant gap/fork/past-cap/expiry, two successive allowed renewals,
   wrong-host/nonce/stale/replayed time probes, authority-state rollback,
   private-key read/replacement, and authority-key rotation. At this point they
   are source/test artifacts only; no host installation is attempted.
2. Implement the closed V1 envelope, JCS UTF-8 body, domain-separated SHA-256
   ID/Ed25519 signature, closed signer-role map, canonical-form rejection, and
   golden macOS/Linux AMD64/Linux ARM64 vectors for
   `host_build_attestation`, `candidate_manifest`, `promotion_authorization`,
   `promotion_attestation`, `lane_acceptance`,
   `ingress_admission_directive`, `ingress_admission_attestation`,
   `capacity_policy`, `host_management_manifest`,
   `host_management_authorization`, `host_management_attestation`,
   `host_attestation_key_set`, `release_revocation_ledger`, and
   `authority_key_set`. Fixture an unknown/wrong host signer, wrong record-type
   signer, canonical-form mismatch, and every host-key bootstrap/rotation/
   revocation rejection.
3. Implement the root anchor's full-sync high-water state, constrained
   root-protected attestation key, authority/host-key chains, cumulative ledger
   validation order and expired-ledger recovery, and helper-owned single-use
   nonce/time-probe correlation. On the Mac add the read-only authority-state
   endpoint for the authority agent only; on every host add the separate
   release-transport endpoint limited to record verification/cache/outbox and
   time-probe create/consume operations. Validate audit-token/label/code identity
   on macOS and dedicated transport UID/systemd unit/cgroup/executable identity
   on Linux. Fixture an unprivileged `ubuntu`/Mac service user, arbitrary root
   peer, or transport peer cannot read an attestation or authority private key,
   choose an evidence path, forge a build/install/ingress attestation or
   authority record, mutate anchor state, or downgrade the helper/signer.
   Fixture expiry, skew, replay/delay, helper unavailable, stale/replaced cache,
   ledger/key fork, transport endpoint misuse, initial/stale-ledger time-probe
   bootstrap with no prior time proof, clock rollback, and fail-closed recovery.
4. Implement the fixed authenticated release-record transport: authority-agent-
   only Mac local Unix socket plus Ubuntu public/private `:9443` mTLS routes,
   closed outbox/record and time-probe API, idempotent fsync/rename cache,
   host-signed build, promotion, ingress, and host-management attestation
   retrieval, and no source/artifact/shell/SQLite path. The Mac builder uses a
   protected helper outbox, never a transport peer; the signer uses only its
   dedicated socket. Fixture the Mac client leaf readable only by the authority
   agent and every Ubuntu server private-key file mode `0600`, root-installed,
   and owned only by its own dedicated non-login release-transport service UID.
   No component is installed yet.
5. Implement and fixture both capacity-policy scopes and signed ingress control.
   `host_management` can authorize only bounded management and Mac authority
   component scope; a
   `lane_runtime` policy must match the promoted lane and full resource
   envelope. Implement authority signing/distribution, host validation, and the
   root helper's aggregate `attest-ingress-applied` operation over the complete
   ingress-surface inventory. Implement directives on L1's fail-closed
   interface: each binds candidate, UUIDv7 transition, host, lane,
   configuration, `directive_role`, epoch, admission generation (`0` closed,
   `1` transport-ready, `2` open), and independently monotonic lease
   generation. Permit `active_runtime_closure` only as a current-runtime closed
   epoch; permit `candidate_admission` as an exact authorized target context,
   with its pending-to-active switch only while both contexts are closed.
   Require exact host-signed `ingress_admission_attestation` records for every
   closed, transport-ready, and open gate. Fixture wrong scope/host, expired
   measurement, wrong cgroup/device, missing Go-cache bound, false Mac
   compute-reservation claim, finite directive-grant renewal, two successive
   allowed renewals, skipped/stale lease, grant cap/expiry, a single global
   no-new-revocations ledger-grant CAS under concurrent lane activity,
   expiry before a remote cohort completes, incumbent closure followed by a
   higher candidate epoch, same-epoch identity/configuration rejection,
   closure-pending no-quiesce/no-child behavior, fence-versus-submit,
   local/queued/importer races, cache loss/restore, abort/retry,
   repair-closure-pending then sealed post-commit repair handoff, and every
   directive-fanout crash.
6. After steps 1–5 pass, commit/push/mirror/pull through the normal source
   delivery chain. Then, only after a separately approved privileged bootstrap
   preflight, take a fresh L0 measurement and bootstrap in the fixed order
   `mac-control-plane`, `linux-host`, `sandbox-host`, `factory-sandbox`. First
   install and verify on the Mac the complete root-owned management row:
   `runner-security-anchor-helper`, `runner-release-authority-signer`,
   `runner-release-authority-agent`, `runner-release-authority-approval`, fixed
   bootstrap `runner-release-builder`, and local `runner-release-transport`.
   Create/import the authority key only into the root signer, create its root
   authority state and audit-token/code-identity socket rules, derive the public
   key/key ID/fingerprint, and prove it exactly matches the pinned public anchor
   before any remote policy, certificate, time probe, or host-management record
   is signed; never distribute that private key. Create the root-owned
   release-transport CA/private key and trust bundles outside Git/lane roots,
   then install the Mac client leaf/private key only through the authority agent.
   On each Ubuntu host in order install its helper, fixed bootstrap builder,
   dedicated non-login release transport, root-installed mode-`0600` server
   leaf/private-key file owned only by that service UID, CA trust bundle,
   literal-IP/URI SANs, and firewall/NAT route.
   Before advancing to the next host, verify every recorded source/toolchain/
   input/binary digest, bootstrap builder tuple allow-list/path/UID, CA/leaf
   fingerprints and trust/SAN chain, pinned bootstrap key/ledger anchors,
   immutable canonical `host_id` → initial `signer_key_id`/public-key mapping
   for all four hosts, root-owned cache/socket/state ownership, constrained
   signer/agent/approval/transport peer checks, host-attestation key protection,
   and the exact local socket or Ubuntu mTLS/time-probe health gate. This is the
   one signed-host-management-authorization exception. A failed initial bootstrap
   signs no host-management manifest: preserve evidence, disable/remove only the
   newly installed management artifacts/listener/trust material, and take a
   fresh L0 before retrying. The fixed bootstrap builder may produce only the
   first management matrix through sealed staging; after the first verified
   management installation, the preceding healthy management manifest admits
   every later builder. Only after all hosts pass may the authority sign the
   per-host `host_management` policies. It creates no lane root, mailbox, direct
   endpoint, queued listener, or lane configuration.
7. Through that baseline, produce the complete **host-management** matrix on all
   four hosts, including the Mac signer, agent, and approval components; collect
   root-protected build attestations, publish a signed host-management manifest/
   authorization set bound to the `host_management` policies, stage one host at
   a time, collect host-management attestations, and establish the compatible
   installed manifest. Test rollback before a management commit and prove no
   active lane root, process, configuration, mailbox, or ingress state changes.
   A signer/agent/approval update must preserve/validate the root authority state
   and public-anchor identity, not replace or restore it. Where a later
   management update affects active V3 lanes, fixture the 900-second
   directive-lease margin and 600-second update-or-verified-rollback guard.
8. Build the separate complete **lane-runtime** matrix through the
   root-controlled builder: Mac `runner`, `runner-local`, `runner-locald`,
   `runner-log-capture`; each Ubuntu `runnerd`, `runner-ssh-bridge`,
   `runner-session-agent`, `runner-log-capture`. Produce one root-protected
   host-build attestation per tuple, then let the Mac verify the matrix and
   create a lane candidate that names the exact healthy host-management
   manifest.
9. Do not issue a `lane_runtime` policy or lane authorization in L3. The
   candidate is eligible for Dev only after the separately approved storage
   handoff, a new L0 measurement, verified volumes, and fresh matching
   lane-runtime policies in L4.

**Exit gate:** a compatible host-management baseline, immutable lane candidate
manifest, complete verified host-build matrices, host-management policies,
caches, anchor/transport records, and signed directive/attestation fixtures
prove the exact artifact eligible for later Dev preflight. No PRD lane
installation occurs in this phase.

## L4 — deploy and accept Dev

**Deliverable:** an isolated Dev control plane and one Dev service on each
accepted Ubuntu profile.

1. Repeat L0 immediately before live change. Verify the approved storage
   handoff manifest by descriptor and the exact healthy host-management manifest
   required by the candidate, then sign and fan out fresh per-host `lane_runtime`
   capacity-policy records. Under the registry lock, seed
   `legacy_prd_reserved` mailbox records, including each retained-ancillary
   inventory, and protected legacy port-22 bridge records from the fresh V2
   inventory. Do not create or repair an external root/volume.
2. Allocate the immutable Dev `lane_transition_id` with the candidate, complete
   host matrix, resolved configuration digests, and current capacity-policy IDs.
   Only then sign and transport the Dev authorization set, target
   `candidate_admission` closed directives, candidate, policy, ledger, key, and
   predecessor records. Register only the
   verified pre-provisioned Dev roots, credentials, dedicated `:2224` queued
   listeners, direct endpoints, and controller authorization on the Mac and all
   three Ubuntu profiles.
3. Apply and verify Dev Linux lane-slice/disk/log controls and Mac verified
   volume/quota, byte admission, cache/snapshot/log budgets, host headroom, and
   background priority policy before Dev starts. Start every Dev component
   admission-closed without stopping or changing PRD; prove the authenticated
   SSH listener, forced-wrapper, and bridge children are in the Dev slice.
4. Require the healthy closed remote cohort, issue `transport_ready`, collect
   attestations, then durably enter `target_opening` **before** the first
   renewable target `open` directive. After every remote open attestation, apply
   the Mac's named target open directive and require its complete aggregate
   ingress attestation before permitting Mac ingress. Any target-open fanout
   failure enters `target_closure_pending` and reaches `failed_closed` only after
   fresh aggregate closure attestations. Run source/static/installed tests and a
   fresh native mailbox acceptance for every Dev route: terminal response,
   complete event prefix, exact ACK, and zero-work postflight.
5. Prove one positive dedicated queued route and one separate positive direct
   mTLS route for every Dev remote profile. Prove both configured directions
   Dev→legacy-PRD and legacy-PRD→Dev reject before resource creation, with the
   equivalent queued key/wrapper-to-foreign-socket attempts. Exercise a bounded
   Dev listener/bridge saturation case and prove PRD remains usable.
6. Attest that PRD process IDs, sockets, listeners, databases, mailboxes,
   credentials, Linux hard Dev/QAS caps and measured PRD headroom/floor, Mac
   storage/layout/headroom, revision, and workload are unchanged. Record
   observed Mac controller compute use and its explicit non-guarantee. Enter
   `acceptance_pending`; only after the whole Dev live-acceptance contract and
   complete installation attestations verify does the authority sign the immutable
   Dev `lane_acceptance` record. Atomically record that acceptance, seal the live
   evidence, and compare-and-swap `accepted → archived` to release the one-active
   record. A live-suite or acceptance failure first closes any target that has
   entered `target_opening`, then follows the recorded failed-closed recovery or
   abort path.

**Exit gate:** Dev is live and lane-attested across the whole cohort, has one
signed Dev `lane_acceptance`, and PRD is unchanged.

## L5 — promote, deploy, and accept QAS

**Deliverable:** an independently isolated QAS lane running the exact
Dev-accepted candidate manifest, Dev `lane_acceptance`, QAS authorization, and
host artifacts.

1. Repeat L0 after Dev has been active. Verify the signed Dev `lane_acceptance`,
   the candidate's required healthy host-management manifest, and the approved
   QAS storage handoff, then sign and fan out fresh per-host `lane_runtime` capacity-policy
   records. Stop if Dev consumption would make the Linux measured PRD
   headroom/floor
   or Mac quota/headroom/log/snapshot budget unsafe; Mac compute contention
   remains the explicit first-rollout non-guarantee.
2. Select the Dev-accepted candidate, allocate the QAS `lane_transition_id` and
   resolved QAS configuration/capacity-policy tuple, then sign the QAS
   authorization set bound to that transition and the Dev acceptance record.
   Register only pre-provisioned QAS roots, credentials, dedicated `:2225`
   listeners, endpoints, and controller authorization. Apply and verify QAS
   Linux slice/disk/log controls and Mac volume/quota/cache/snapshot/log/
   headroom/background controls before QAS starts.
3. Repeat the target candidate-admission closed → transport-ready sequence,
   durably enter `target_opening` before the first QAS `open` directive, then
   apply the remote and Mac named open directives with their complete aggregate
   ingress attestations. A partial open failure first completes
   `target_closure_pending`. Repeat L4 positive route tests for QAS. Prove all
   six pre-L6 foreign mTLS directions
   per profile (Dev↔QAS, each of Dev/QAS to legacy PRD, and legacy PRD to each
   new lane) and the equivalent queued key/wrapper-to-foreign-socket attempts
   remain pinned to their source lanes. Verify listener/session/wrapper/bridge
   cgroup membership and bounded saturation for QAS.
4. Prove a QAS restart, failed candidate, dedicated-listener refresh, mailbox
   activation, directive expiry, log-cap event, or capacity-policy refusal
   leaves Dev and PRD unchanged. Enter `acceptance_pending`; after the complete
   QAS live-acceptance contract and complete installation attestations verify,
   the authority signs the immutable QAS `lane_acceptance` record. Atomically
   record it, seal the live evidence, and compare-and-swap `accepted → archived`.
   An open/live-acceptance failure closes the target before failed-closed recovery
   or abort.

**Exit gate:** QAS and Dev run concurrently, both have complete signed lane
acceptance records and are isolated, and PRD remains on its prior accepted
revision.

## L6 — scheduled PRD migration and promotion

**Deliverable:** the unsuffixed stable installation explicitly identifies as
PRD and receives only a QAS-accepted candidate manifest, QAS `lane_acceptance`,
fresh PRD authorization, and complete promotion/live-acceptance chain.

1. Before entering `prepare`, repeat L0, validate the fresh capacity/storage
   measurements, and sign/fan out the per-host `lane_runtime` capacity-policy records. Stop on
   a legacy reservation, configuration, bridge, policy, storage, or
   host-management mismatch. In `prepare`, acquire the atomic PRD
   operation-group lock and allocate the durable PRD `lane_transition_id` with
   the QAS-accepted candidate, QAS
   `lane_acceptance`, all already-verified resolved PRD
   configuration/capacity-policy IDs, and the candidate's exact required healthy
   host-management manifest ID. Confirm Dev/QAS health, `legacy_prd_reserved` mailbox/bridge
   entries, maintenance window, publisher/direct-client pause controls, and
   current work inventory. Do not issue PRD authorizations or alter V2 state in
   `prepare`.
2. Atomically enter `fencing` **before** the first V2 service-manager or client
   action and record per-host V2 status. Then pause every configured
   publisher/direct client, drain all mailbox work, sessions, commands, and
   resumable jobs to proven zero work, and record every V2 stop/disable,
   PID/socket/listener absence proof. An uncontrolled writer/client, uncertain
   record, or remaining work stops the transition. Under the exclusive ingress
   lock persist each migration marker, empty paired request/ACK snapshot,
   descriptor/digest deferred ledger, and target candidate-admission intent;
   V2 has no directive consumer, so no directive is issued yet. Only then enter
   `fenced`. A crash stays `fencing` or `fenced` and follows durable
   recovery/abort; it never silently restarts V2.
3. In `staged`, take the PRD-only continuity bundle and prove isolated restore.
   Include database/WAL/SHM, configuration, mailbox markers/snapshots/deferred
   ledger, directives/applied states/attestations, bridge material, principal
   maps, credentials, PRD registry records, and reference to the preserved
   security anchor. Immediately before candidate installation, sign fresh
   short-lived PRD authorizations bound to the preallocated transition, current
   policy, and the candidate's exact required healthy host-management manifest;
   atomically append their IDs to the
   transition, then allocate and fan out target `candidate_admission` closed
   directives through the release-record transport.
4. In `candidate_closed`, migrate the unsuffixed configuration to V3
   `release_lane: prd` without changing its root, labels, public port, mailbox
   roots, or durable paths. Convert matching legacy mailbox/port-22 bridge
   reservations to active PRD, activate `runner-log-capture` with null direct
   stdout/stderr and its cap, activate the authorized target closed context,
   start all members admission-closed, and require health, lane, source,
   artifact/configuration, mTLS, bridge, capacity, exact required
   host-management manifest, logging, and recovery attestations. When
   every member is healthy and closed, atomically record `committed`; V2 rollback
   is now forbidden.
5. Every V3 member reconciles closed state and records `reopen_prepared`. The
   registry compare-and-swaps to `reopen_authorized` with exact
   `transport_ready` directive IDs, collects remote readiness, atomically adds
   final open-directive IDs, collects remote open attestations, then applies the
   Mac named open directive and requires its complete aggregate ingress
   attestation before permitting Mac ingress. It compare-and-swaps to `reopened`
   only with the complete directive/attestation set. Run PRD mailbox, queued,
   direct-mTLS, the converted port-22 PRD bridge, log rotation, and full
   six-direction cross-lane-denial gates. Separately prove Dev/QAS dedicated
   listener and lane-slice isolation remains intact.
   After the full PRD live-acceptance contract passes, issue the PRD
   `lane_acceptance` and archive the completed transition. Before `committed`,
   an abort restores V2 only through the fence-aware offline verifier while
   preserving the security anchor and issuing a higher epoch on retry; release
   the operation group only after V2 restoration, fence-release evidence, and
   archival. After `committed`, an irrecoverable partial fanout or
   live-acceptance failure enters `post_commit_repair_required`, remains
   repair-closure-pending until a fresh all-host closure reaches repair-closed,
   seals its repair handoff, then archives before a new scheduled
   `v3_maintenance` primary may acquire the group. It never uses a parentless
   recovery child or V2 revival.

**Exit gate:** PRD has exact Dev → QAS → PRD candidate, installation, and signed
lane-acceptance provenance; all routes are accepted at the new PRD revision, and
the tested cohort-abort/forward-repair evidence is recorded.

## L7 — operate and improve

Operate per-lane `lane_runtime` capacity-policy thresholds and
`host_management` policy thresholds, Linux lane and host-management slice use,
normal Go-cache budget without automatic cleanup, Mac quota use, APFS snapshots
and host headroom, bounded cache maintenance, log-capture rotation and
`dropped_log_bytes`, backup/restore drills, credential/key rotation and
revocation, release-record/cache retention, five-minute ledger refresh,
directive lease renewal, host-management maintenance, and Dev/QAS-only refresh
procedures. Never automatically delete retained mailbox or rollback evidence to
reclaim space.

A host-management update uses a fresh `host_management` policy, fresh ledger,
one-host staging, and a verified update-or-rollback within 600 seconds. If any
V3 lane is active, every affected directive must have at least 900 seconds of
lease remaining before the update starts; otherwise perform a planned signed
lane closure first.

For a Dev/QAS V3 refresh, first acquire the lane's durable one-active operation
record, recording both incumbent and target identity and every per-host
directive/attestation result. Close the incumbent under a fresh
`active_runtime_closure` epoch, and remain closure-pending until complete
aggregate closure attestation exists. Only then start the target under a
strictly higher `candidate_admission` closed epoch. A failed target leaves the
lane closed or recovers the verified incumbent only through fresh authorization,
a still-higher target-candidate epoch, the full remote/Mac reopen sequence, and
a fresh lane acceptance. Fixture partial closure retry/no-pointer-restore,
crash resumption, concurrent-refresh rejection, and both recovery paths; never
reopen by restoring an old pointer or directive.

Run each later PRD promotion as the scheduled V3-to-V3 maintenance transition:
new UUIDv7, QAS `lane_acceptance`, fresh `lane_runtime` policies and
authorizations, exact required healthy host-management manifest, and two
ordered epochs per affected host. The `active_runtime_closure` epoch is fresh
above anchor high-water, bound to the recorded incumbent candidate/configuration,
and remains closure-pending until it has complete aggregate closed attestations
before quiescence. The target
`candidate_admission` closed epoch is strictly higher, is bound to the
QAS-accepted candidate, and alone proceeds through transport-ready/open. Then
run tested current-V3 restore, candidate-closed validation, commit, full
remote/Mac reopen and live suite, signed PRD `lane_acceptance`, and archival.
A pre-commit failure keeps the atomic PRD operation-group lock and uses at most
one active linked recovery child. Each receives a new UUIDv7 and a target
candidate-admission epoch strictly higher than the failed transition and every
host anchor high-water: a distinct V3 restore when its predecessor/required
manifest remains valid and fresh matching policies can be issued, or an
explicitly authorized forward repair with a newly QAS-accepted candidate when
it cannot. Both use fresh authorizations, closed/transport-ready/open
directives, aggregate attestations, and a fresh transition-bound PRD
`lane_acceptance`, never stale directives or an old acceptance. If that child
fails before commit, archive it admission-closed with its evidence before
explicitly allocating one higher-epoch replacement child; never run siblings.
After any V3 commit, an irrecoverable reopen or acceptance failure enters
`post_commit_repair_required`, remains repair-closure-pending until a new
all-host closure reaches repair-closed, then seals its repair handoff and
archives (and archives its parent when it is a recovery child) before a new
scheduled primary acquires the group. Reject every other PRD transition while
the group is held. It never revives V2.

Revisit event-driven mailbox wake-up only after measuring per-lane latency and
backlog. It remains a best-effort hint with periodic reconciliation, never the
mailbox delivery path.
