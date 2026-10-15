# Detailed design: Dev, QAS, and PRD release lanes

**Status:** reviewed implementation design. It defines the source work needed
before any lane is installed. It does not authorize a service, mailbox,
credential, firewall, or runtime-configuration change. Every live change still
needs the fresh capacity, quiescence, route, and approval gates in the phased
plan.

## 1. Scope, promise, and current cohort

Runner needs three concurrently usable release lanes:

- **Dev** receives source changes and proves new Runner behavior.
- **QAS** receives the exact accepted Dev release and tests it independently.
- **PRD** remains the stable Runner used by existing mailbox producers while
  Dev and QAS change.

`lane` is a release/deployment identity. It is separate from historical
execution-policy names such as `linux-dev`, `sandbox-dev`, and
`factory-sandbox-dev`. Those names describe an execution environment in the
current V2 configuration; none means that the current service is a Dev release
lane.

The directory name for this improvement predates the Factory host. The current
cohort is **four physical machines**: the Mac control plane and three accepted
Ubuntu profiles. Every lane covers all three remote profiles; an omission is a
failed lane deployment, not an implicit PRD-only exception.

| Remote profile | Host role | Architecture | Current PRD-compatible route |
| --- | --- | --- | --- |
| `linux-host` | current Ubuntu | AMD64 | `129.151.232.40:8443` |
| `sandbox-host` | sandbox Ubuntu | ARM64 | `132.226.205.205:8443` |
| `factory-sandbox` | Factory Ubuntu | AMD64 | `145.241.255.66:8443` |

Release records use the fixed canonical host IDs `mac-control-plane`,
`linux-host`, `sandbox-host`, and `factory-sandbox`. `remote_profile` means one
of the three Ubuntu profiles only; records for the Mac use
`host_id: mac-control-plane` and omit `remote_profile`. A hostname, SSH alias,
or mutable endpoint name is never an identity in a release record.

The existing unsuffixed installation is a **PRD compatibility baseline**, not
an already-migrated PRD lane. It remains unchanged until the scheduled PRD
migration. Creating Dev or QAS must not rename, stop, repoint, migrate, share
state with, or otherwise alter it.

The lanes prevent accidental cross-lane service, configuration, transport,
mailbox, and deployment collisions. They are not hostile-code containment
while lane processes and submitted scripts use the same Mac and Ubuntu accounts:
a script running as that account can still consume shared host resources or
access another same-user directory. The root-owned host-management and authority
signing principals described below are deliberately outside those accounts. A
future requirement for fault containment against Dev scripts still requires
separate operating-system accounts or a VM/sandbox boundary; it is not claimed
by this design. Linux cgroup controls and bounded Mac storage protect the stated
operational resources. A Mac CPU/RAM/PID availability guarantee is outside this
first rollout while the three control-plane pairs share one macOS user; neither
naming nor launchd priority hints turn same-user arbitrary code into a security
boundary.

The file mailbox remains the integration boundary. Marker-last publication,
importer validation, SQLite authority, idempotency, event cursors, ACKs, and
conservative recovery remain unchanged inside each lane.

## 2. Fixed lane namespace

An item in a shared operating-system namespace has a lane suffix. Files that
exist only below a lane-suffixed service root may retain stable leaf names
(`config/mac.yaml`, `state/local.db`, and so on), because their full paths are
already unique. PRD deliberately has no `-prd` suffix.

| Resource | Dev | QAS | PRD compatibility mapping |
| --- | --- | --- | --- |
| Release identity | `dev` | `qas` | `prd` |
| Mac service root | `~/Library/Application Support/RemoteSessionRunner-dev` | `~/Library/Application Support/RemoteSessionRunner-qas` | existing `~/Library/Application Support/RemoteSessionRunner` |
| Mac LaunchAgent labels | `com.remote-session-runner.local-dev`; `com.remote-session-runner.locald-dev` | `com.remote-session-runner.local-qas`; `com.remote-session-runner.locald-qas` | `com.remote-session-runner.local`; `com.remote-session-runner.locald` |
| Mac plist files | matching suffixed label names under `~/Library/LaunchAgents/` | matching suffixed label names under `~/Library/LaunchAgents/` | existing unsuffixed plist names |
| Mac private sockets | `<root>/run/local-api.sock`; `<root>/run/locald.sock` | same leaves below QAS root | same leaves below existing root |
| Linux Runner unit | `runnerd-dev.service` | `runnerd-qas.service` | `runnerd.service` |
| Linux lane slice | `remote-session-runner-dev.slice` | `remote-session-runner-qas.slice` | existing unsuffixed host cgroups and measured baseline scope through and after L6, unless separately approved PRD controls are added |
| Linux queued-SSH unit | `runner-sshd-dev.service` | `runner-sshd-qas.service` | existing host `sshd.service` bridge entry on port 22 |
| Linux service root | `/home/ubuntu/.local/share/remote-session-runner-dev` | `/home/ubuntu/.local/share/remote-session-runner-qas` | existing `/home/ubuntu/.local/share/remote-session-runner` |
| Linux private socket | `<root>/run/runnerd.sock` | `<root>/run/runnerd.sock` | existing `<root>/run/runnerd.sock` |
| Linux queue listener, on every remote host | `2224` | `2225` | existing `22` |
| Linux queued-SSH authority | `/etc/remote-session-runner/ssh/dev/` | `/etc/remote-session-runner/ssh/qas/` | existing `/home/ubuntu/.ssh/authorized_keys` bridge entry; converted in place for V3 PRD at L6 |
| Linux journal identifier | `runnerd-dev`; `runner-sshd-dev` | `runnerd-qas`; `runner-sshd-qas` | `runnerd`; host `sshd` |
| Direct HTTPS port, on every remote host | `8444` | `8445` | `8443` |
| Mac configuration | `<root>/config/mac.yaml` | `<root>/config/mac.yaml` | existing `<root>/config/mac.yaml` after migration |
| Linux configuration | `<root>/config/linux.yaml` | `<root>/config/linux.yaml` | existing `<root>/config/linux.yaml` after migration |

The lane-aware installer derives this table from the selected lane and refuses
an operator-supplied root, label, unit, socket, port, executable path, staging
path, lock, or log path that differs from the table. It installs lane-specific
entrypoints and binaries below the corresponding root. The unit and
LaunchAgent label, not a mutable process argument, is the authoritative
service identity.

The deliberately shared Mac coordination component is an owner-only **lane
registry** at:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-lane-registry/
```

It contains no executable authority, request body, credential, SQLite state,
or mailbox content. It holds the global mailbox-root records, immutable
candidate manifests, promotion attestations, and publication locks needed to
prevent different lanes from claiming the same external root or publishing a
conflicting release record. The same recorded artifact is intentionally
promotable to Dev, QAS, and PRD on one host. Its owner-only files are an
explicit exception to the per-lane root rule and are never writable by a
mailbox request.

The lane registry is a coordination and publication mirror, never an authority
input: it is writable by the shared Mac service UID and therefore cannot grant a
signature, advance an admission state, or authorize a transition. The Mac
root-owned release-authority state defined in section 7 stores the immutable
approved mandate, consumed approval IDs, accepted record IDs, and verified
transition/high-water facts on which signing decisions rely. The authority agent
may read a registry entry only as an untrusted proposed input, then verifies its
canonical bytes, referenced root evidence, and exact approved mandate before it
copies a resulting signed record or state digest back to the registry. A registry
mutation can therefore delay or reject an operation, but cannot make a shared-UID
script obtain a signature. The separate root-owned per-host verified release
cache is a shared read-only installation input, described in section 7. Shared
remote-host files, including `authorized_keys`, are separately governed in
section 5.

## 3. V3 configuration and lane identity

V3 is the first lane-aware configuration schema:

```yaml
version: 3
release_lane: dev # exactly dev, qas, or prd
```

Each lane has one complete owner-only V3 document for its Mac control-plane
pair and one complete owner-only V3 document for each `{lane, remote-profile}`
Linux service. A document contains only one lane. A Mac document contains the
fixed entries for all accepted remote profiles; a Linux document is bound to
exactly its local remote profile. Neither document can register a second lane,
reference a different lane's root, or inherit fields through a symlink or YAML
alias. Secrets remain owner-only files outside Git; configuration contains only
their validated paths and safe fingerprint attestations.

V3 validation derives and checks all of the following from `release_lane`:

1. Mac/Linux service root, configuration path, database, WAL/SHM sidecars,
   sockets, workspace, temporary, backup, log, binary, staging, and lock
   locations.
2. LaunchAgent label/plist or systemd unit/entrypoint/journal identity.
3. The direct listener port and the matching public endpoint for every remote
   profile in a Mac document, and the one local profile in a Linux document.
4. Execution contexts, remote profiles, direct endpoint profiles, mailbox
   definitions, controller identities, and credential fingerprints. Every
   mailbox-allowed `environment` plus `execution_target` pair resolves only to
   a transport and endpoint in the document's lane.
5. The release attestation: `release_lane`, source revision, installed
   artifact digest, configuration digest, and schema version.

`dev/linux-host` in this document is prose for the tuple
`(release_lane=dev, remote_profile=linux-host)`. It is not a legal V3 registry
identifier containing `/`. V3 may retain familiar safe context names within a
separate lane document, but each named profile and endpoint carries the
document's explicit lane binding. Historical `*-dev` environment labels do not
select the Dev release lane.

V2 remains a valid, untouched single-runtime format during Dev and QAS
introduction. It does not gain an implicit `release_lane`, and its V1 mailbox
response schema does not gain an undocumented field. Until L6, V2 health and
mailbox output prove only the current unsuffixed compatibility runtime. The
scheduled PRD migration replaces it with V3 `release_lane: prd` while retaining
all unsuffixed roots and public names.

V3 health and read-only status endpoints expose the safe release attestation.
The retained V1 mailbox response continues to identify its `inbox_id` and
request; it does not expose a filesystem root or an unversioned lane field.
An inbox ID is not globally unique: the response's containing configured root
plus the registry's `(root, inbox_id)` record maps it to a lane. The response
alone never attests a lane; V3 status does. A later versioned response envelope
may add a safe lane attestation only with client and schema migration coverage.

For the first rollout, Dev and QAS V3 documents contain remote execution
targets only: the validator rejects a `kind: local` target in those lanes. The
Mac control-plane services still run; “remote-only” limits where a submitted
script executes, not where mailbox/importer control runs. A later Dev/QAS
local target needs a separate Mac account or VM and its own accepted capacity
and cross-lane tests. PRD retains its existing local-target behavior until a
separately reviewed PRD policy changes it.

## 4. Mailbox, state, and configuration ownership

Each lane owns its complete configuration, SQLite database and sidecars,
sockets, mailboxes, logs, workspaces, temporary files, backups, installed
binary, and release attestation. The Runner configuration and installer must
refuse to open another lane's database, socket, workspace, response tree,
bridge socket, or secret. This is an application invariant, not an operating-
system denial against an arbitrary same-user submitted script; that limitation
is the one described in section 1.

The Mac-wide lane registry makes external mailbox ownership enforceable across
the otherwise independent lane databases. Each durable root record includes its
normalized path, descriptor-derived filesystem identity (`st_dev`, `st_ino`),
and, for a quota-bound Mac volume, the expected volume UUID and mountpoint.
Records have one of five states: `legacy_prd_reserved` for an unmodified V2
root; `prepared` for a candidate root/configuration epoch; `active` only after
its root and configuration are durable and the service has reconciled them;
`superseded` as the retained prior epoch; or `retired` as a retained tombstone.
All five states remain in the collision set. A retry may resume only its own
matching `prepared` record; it cannot claim another lane's record. Service
startup and periodic reconciliation revalidate the active root's path,
filesystem/mount identity, lane, epoch, and configuration digest against the
registry before accepting mailbox work.

The registry also holds one durable **lane operation record** for each of
`dev` and `qas`. It admits at most one unarchived lane transition at a time and
is changed only by compare-and-swap. The record names its UUIDv7 transition,
target candidate/configuration identities, per-host authorization/directive/
attestation progress, and, for a replacement, the exact incumbent identities.
Its closed state graph is:

```text
fresh:       prepare → candidate_closed → reopen_prepared → reopen_authorized → target_opening → reopened → acceptance_pending → accepted → archived
replacement: prepare → incumbent_closure_pending → incumbent_quiescing → incumbent_quiesced → candidate_closed → reopen_prepared → reopen_authorized → target_opening → reopened → acceptance_pending → accepted → archived

replacement prepare before closure ──(abort)──> aborted → archived
incumbent_closure_pending ──(failure)──> incumbent_closure_pending (retry/renew/safe stop only)
incumbent_quiescing ──(failure)──> incumbent_quiescing (supported terminal disposition or safe stop only)
fresh before target_opening, or replacement after candidate_closed and before target_opening ──(failure)──> failed_closed → aborted → archived
target_opening | reopened | acceptance_pending ──(failure)──> target_closure_pending → target_quiescing → failed_closed
failed_closed ──(recover exact incumbent)──> recovery_candidate_closed → reopen_prepared → reopen_authorized → target_opening → reopened → acceptance_pending → accepted → archived
recovery_candidate_closed through reopen_authorized ──(failure)──> recovery_aborted → archived
recovery target_opening | reopened | acceptance_pending ──(failure)──> target_closure_pending → target_quiescing → recovery_aborted → archived
```

`incumbent_closure_pending` records every incumbent-closure retry and renewal; it may advance only after every
required aggregate closed attestation completes. Until then it cannot abort,
archive, release the one-active record, allocate a recovery, or presume the
incumbent is closed. `incumbent_quiescing` then uses only supported terminal
Runner disposition and reconciliation: it drains or classifies every running,
queued, resumable, and uncertain item, retains any deferred request/ACK evidence,
and requires the complete zero-work postflight before it records
`incumbent_quiesced`. It never deletes, ACKs, replays, or guesses an uncertain
mailbox item.

`target_closure_pending` applies and attests a new `active_runtime_closure` epoch
to the already-open target. It advances to `target_quiescing` only after every
target surface is actually closed; that state performs the same terminal
disposition, retained-evidence, and zero-work gate before `failed_closed` or
`recovery_aborted`. A crash resumes its recorded closure/quiesce state, never a
guessed open or empty state. `target_opening` is recorded before the first target
`open` directive fanout; from that point any failure must complete target closure
and quiescence before it can become failed-closed. `accepted` requires the
immutable lane-specific `lane_acceptance` after the full live suite, including
its zero-work postflight; `target_opening`, `reopened`, and
`acceptance_pending` do not release the operation record.

`failed_closed` records the complete failure digest, every known directive and
high-water tuple, and exactly one disposition. `recover_incumbent` starts the
exact recorded incumbent only by allocating a new `prepared` successor epoch for
that exact candidate/configuration, explicitly superseding the closed target. It
then requires fresh authorization, a strictly higher `candidate_admission`
epoch, the full remote/Mac reopen sequence, and a fresh lane acceptance before
one compare-and-swap makes that new record active. It records one durable
recovery-attempt ID and never reactivates the historical `superseded` row. If
that recovery fails, it does not return to `failed_closed` or select another
disposition: it remains closed, or first completes target closure and
`target_quiescing` if it had reopened, then seals `recovery_aborted → archived`
with its failure evidence.
`abort` keeps every known context closed and seals the record as `aborted →
archived`. No transition restores an old pointer or directive, and no state
releases the one-active record before `accepted → archived`, closed `aborted →
archived`, or closed `recovery_aborted → archived` evidence is durable. Another
Dev or QAS refresh is rejected until then. This is separate from the richer PRD
operation group in section 8, but prevents competing Dev/QAS multi-host fanouts
from becoming ambiguous after a controller restart.

A missing, corrupt, or rollback-suspected lane registry is a **registry-recovery
stop**, not an opportunity to recreate a convenient set of locks. It blocks new
`prepare`, root activation, root release, pointer change, and operation-group
allocation. It preserves the failed bytes and process/root evidence; no backup
is accepted as an authority by itself. Existing active runtimes may continue only
with their already-valid anchor/directive/configuration context and cannot be
restarted, refreshed, or reassigned while recovery is pending. A separately
approved root-maintenance procedure, not a V1 signer mandate or record, may use
the root authority state and per-host anchor facts as verified
transition/candidate/configuration identities; a fresh L0 descriptor inventory
supplies current root and collision evidence. Recovery reconstructs a new
registry generation only when every collision claim is unique and matches those
facts. Each in-flight Dev/QAS transition or PRD operation group receives an
explicit durable closed disposition with preserved evidence; it is never resumed
from an inferred pointer. For PRD it classifies durable per-root fence markers,
paired snapshots, service/anchor state, and commit evidence first: a pre-commit
V2 fence follows only its existing fence-aware abort path, while a committed or
post-commit repair state follows only its existing V3 repair/closure path. It
never releases the PRD group or restarts V2 by inference. Any claim that cannot
be reconciled remains closed and requires a fresh successor transition. Fixtures
cover idle and mid-transition loss, corruption, and rollback, including an L6
fencing/fenced and post-commit-repair loss.

During activation the installer:

1. takes the registry lock;
2. normalizes and checks each configured path against every active or reserved
   root. It may create an internal fixed lane-root child only inside the selected
   new lane service root. Every fresh Dev/QAS external root and its fixed
   five-child mailbox layout must already be supplied by the approved storage
   handoff; a retained PRD root must already have its required mailbox children
   and only its fresh-L0-declared ancillary entries. Neither an external root,
   ancestor, nor child is created or repaired by this operation;
3. opens each resulting root by descriptor, derives its filesystem/mount
   identity, rechecks its path and identity against every active or reserved
   root, and refuses equal, nested, renamed/aliased, or cross-lane roots;
4. creates or resumes only a matching `prepared`
   `(lane, inbox_id, normalized_root, filesystem_identity,
   configuration_digest)` record for every enabled root;
5. atomically changes only its own prepared record to `active` after service
   reconciliation succeeds.

Normalization is not external-root validation. Before a `prepared` record can
be created, the installer must use descriptor/no-follow traversal to verify a
regular, non-symlink external path; safe owner-controlled ancestors; no
group/other-writable ancestor; an owner-only `0700` root; and the owner-only
`0700` `inbox`, `outbox`, `events`, `acks`, and `diagnostics` children. An
`initialized_empty` root contains exactly those five empty children. A
`retained` root contains those children plus only a declared
`retained_ancillary` inventory.
Each ancillary entry has a safe relative top-level name, expected type,
descriptor identity, owner, and non-group/other-writable mode; Runner never
traverses, reads, writes, or deletes it. This permits a retained entry's
external opaque contents to change without permitting replacement of its
directory or file identity. A Mac Dev/QAS external root must additionally be
the expected quota-bound APFS mountpoint with its recorded volume UUID. The
installer never creates or repairs an external ancestor, root, child, or
storage volume. Existing unsafe mode, ownership, symlink, missing required
mailbox child, undeclared/replaced ancillary entry, declared-layout mismatch,
mount identity, or filesystem identity is a hard failure, and these are
negative activation fixtures as well as live preflight checks.

L1 defines a second record form, `legacy_prd_reserved`, containing the
normalized root, descriptor-derived filesystem identity, inbox ID, baseline V2
configuration digest, declared retained-ancillary inventory, and a reservation
for `prd`. It is in the collision set but does not reinterpret or change the
live V2 mailbox. Immediately after the fresh L0 read and before the first Dev
root is created, L4 atomically seeds one such record for every current
unsuffixed root. L6 converts each matching reservation atomically to its active
PRD owner record as part of the fenced PRD cutover. Dev and QAS use fresh roots;
a disabled producer has no lane mailbox and may never fall back to the reserved
or active PRD root. Failed preparation remains a recorded `prepared` state for
supported repair or retirement; it is never silently reused by another lane.

A separate per-host **bridge reservation** has the same lifecycle discipline as
a mailbox root. The legacy record type is `legacy_prd_bridge_reserved`; it is
not overloaded onto a mailbox record. It names canonical host ID, authenticated
key fingerprint, exact authorization-entry comment and digest, forced-wrapper
path and digest, bridge binary/manifest digest, controller-map digest, derived
runnerd socket path and descriptor identity, and the V2 configuration digest.
L4 seeds it from the fresh V2 inventory without rewriting the live bridge. A
V2 maintenance refresh may replace it only with the same host and equivalent
protected identities while retaining history. During L6, the migration lock
atomically converts the matching legacy bridge reservation to the active PRD
bridge record together with the PRD mailbox reservations; a fingerprint,
wrapper, socket, manifest, or key mismatch is a hard stop.

A normal same-lane configuration refresh uses a successor epoch inside its
durable lane operation record. It may create `prepared` only when it names the
same lane, inbox ID, normalized root, descriptor-derived filesystem identity,
and (for Mac Dev/QAS) expected volume UUID/mountpoint as the current active
record, and explicitly `supersedes` that record; a different lane, root,
identity, or mount remains a collision. An active Dev/QAS same-lane refresh is
itself a V3 replacement: it first enters durable
`incumbent_closure_pending`, applies and attests a fresh
`active_runtime_closure` epoch to the current runtime, and remains there while
it retries/renews closure fanout. It may not quiesce, repoint ingress, stage a
successor, claim lane closure, or recover an old pointer until every required
incumbent surface has a complete aggregate closed attestation. It then records
`incumbent_quiescing`, resolves work only through supported terminal
reconciliation, retains any deferred mailbox/ACK evidence, and reaches
`incumbent_quiesced` only with a complete zero-work postflight. Only then does it
verify the successor as a pending candidate under a strictly higher
`candidate_admission` closed epoch. It stages, starts, and reconciles that
successor while its context remains `prepared` and admission-closed. Only after
that start/reconciliation succeeds does one registry compare-and-swap make the
successor `active` and the incumbent `superseded`; only the successor's candidate
epoch may later advance admission. If staging or start fails, the successor
remains `prepared`, all known contexts remain closed, and the lane operation
enters `failed_closed`. If the target had already opened when its acceptance
fails, it instead completes `target_closure_pending → target_quiescing` with a
supported terminal disposition and zero-work evidence before entering
`failed_closed`. Its retained incumbent is a recovery target only through the
explicitly recorded `recover_incumbent` disposition, fresh authorization, and a
still-higher `candidate_admission` epoch for its exact prior identity. It cannot
simply restore an old active pointer or directive. This permits
credential/configuration rotation without allowing a cross-lane takeover or
assuming old ingress was already closed. A V3 PRD replacement instead follows
only the `closure_pending` state graph in section 8.

## 5. Direct network, mTLS, and queued SSH isolation

### Fixed direct endpoint map

Each public endpoint is a fixed `{profile, lane}` mapping. There is no
free-form hostname or port in a request.

| Profile | Dev endpoint / public URL / bind | QAS endpoint / public URL / bind | PRD endpoint / public URL / bind |
| --- | --- | --- | --- |
| `linux-host` | `linux-poc-dev` / `https://129.151.232.40:8444` / `10.0.0.200:8444` | `linux-poc-qas` / `https://129.151.232.40:8445` / `10.0.0.200:8445` | `linux-poc` / `https://129.151.232.40:8443` / `10.0.0.200:8443` |
| `sandbox-host` | `sandbox-poc-dev` / `https://132.226.205.205:8444` / `10.0.0.14:8444` | `sandbox-poc-qas` / `https://132.226.205.205:8445` / `10.0.0.14:8445` | `sandbox-poc` / `https://132.226.205.205:8443` / `10.0.0.14:8443` |
| `factory-sandbox` | `factory-sandbox-poc-dev` / `https://145.241.255.66:8444` / `10.0.0.30:8444` | `factory-sandbox-poc-qas` / `https://145.241.255.66:8445` / `10.0.0.30:8445` | `factory-sandbox-poc` / `https://145.241.255.66:8443` / `10.0.0.30:8443` |

L2 must prove the matching host firewall, cloud security rule/NAT path,
listener bind, endpoint name, server certificate SAN, client trust bundle, and
principal map for every table cell before starting that unit. V3 validates the
derived lane port; it does not merely relax V2's `8443` check.

### mTLS

Every `{lane, remote profile}` has distinct server material, a lane-specific
trusted client bundle, and a lane-specific client leaf. The client leaf for
`{lane, profile}` has exactly this URI SAN:

```text
urn:remote-session-runner:lane:<lane>:controller:<profile>
```

The corresponding Linux principal map contains that exact URI with
`controller_type: direct_mtls` and controller ID
`runner-controller-<lane>-<profile>`. Each server certificate carries the
public IP in the endpoint table as an IP SAN and the distinct server URI
`urn:remote-session-runner:lane:<lane>:server:<profile>`. The URI and principal
map bind the controller to the same lane; the IP SAN binds the client URL to
the server certificate. A server accepts only its own lane's authorized client
identity. Rotation may overlap old and new material within one lane, but a
certificate, CA bundle, principal-map entry, or private key cannot be shared
between lanes.

The configuration parser validates only safe secret paths. The installer and
runtime attestation validate the expected certificate identity and report safe
fingerprints/digests, never secret contents. A Dev/QAS client presented to a
PRD endpoint must fail the TLS/controller check before a direct session,
command, or remote SQLite record is created.

### Queued SSH

The V3 Dev and QAS queued routes do **not** add keys to the host-global
`/home/ubuntu/.ssh/authorized_keys`. On every Ubuntu host each has a dedicated,
root-owned OpenSSH listener:

| Profile | Dev public/bind | QAS public/bind | PRD compatibility route |
| --- | --- | --- | --- |
| `linux-host` | `129.151.232.40:2224` / `10.0.0.200:2224` | `129.151.232.40:2225` / `10.0.0.200:2225` | existing `129.151.232.40:22` |
| `sandbox-host` | `132.226.205.205:2224` / `10.0.0.14:2224` | `132.226.205.205:2225` / `10.0.0.14:2225` | existing `132.226.205.205:22` |
| `factory-sandbox` | `145.241.255.66:2224` / `10.0.0.30:2224` | `145.241.255.66:2225` / `10.0.0.30:2225` | existing `145.241.255.66:22` |

`runner-sshd-dev.service` and `runner-sshd-qas.service` use their corresponding
root-owned configuration directory under
`/etc/remote-session-runner/ssh/<lane>/`, separate host key, dispatcher public
key file, server host-key fingerprint, and forced-command wrapper. The Mac
lane configuration carries the fixed port and the matching lane-specific
`known_hosts` fingerprint; a dispatcher cannot choose a host, port, or socket.
The listener accepts only `ubuntu`, uses `UsePAM no`, `PermitTTY no`,
`AllowTcpForwarding no`, `AllowAgentForwarding no`, `X11Forwarding no`,
`PermitUserEnvironment no`, no SFTP/subsystem, finite `MaxStartups`,
`MaxSessions`, `LoginGraceTime`, and `MaxAuthTries`, and a fixed root-owned
`ForceCommand`. The wrapper accepts only the documented bridge `--stdio`
operation and contains the fixed lane socket. It rejects every other command,
subsystem, forwarding request, or client-supplied socket.

Both the listener and every descendant session/forced-wrapper/bridge process
belong to the relevant `remote-session-runner-<lane>.slice`; `UsePAM no` avoids
PAM/logind moving a successful SSH session into a host-global
`session-*.scope`. The installation and saturation fixtures inspect the cgroup
of the listener, authenticated sshd child, wrapper, and bridge child through
`/proc/<pid>/cgroup` before admitting traffic. A process outside the lane slice
is a failed installation. Thus the Dev/QAS cgroup cap covers the ingress that
can create queued work as well as `runnerd` and its session-agent children.

The global port-22 `sshd` and `/home/ubuntu/.ssh/authorized_keys` remain
untouched for PRD compatibility through L6. L4 records one
`legacy_prd_bridge_reserved` record under the host-global owner-only bridge
lock, without rewriting the entry. Dev/QAS installation neither reads nor
modifies unrelated port-22 keys. At L6 the same lock converts only the recorded
legacy bridge entry to its V3 PRD equivalent; it preserves unrelated entries
and validates its key fingerprint, marker/comment, wrapper digest, bridge
manifest, controller map, and socket identity. The PRD route remains port 22;
it is intentionally outside a new Dev/QAS lane slice, while those slices protect
it from Dev/QAS queued-load exhaustion.

The queued bridge frame has no lane selector. That is intentional: a Dev or QAS
key reaches only its own listener and forced wrapper, then only its own fixed
socket. The cross-lane tests prove that each source key cannot address, create,
read, cancel, or close a resource in either foreign lane. Direct mTLS has the
separate explicit rejection test.

## 6. Shared-host resource protection

Measurement alone does not protect PRD. L2 implements and proves the controls
in a staged fixture. L4 applies the Dev controls before Dev starts, and L5
applies the QAS controls before QAS starts. Neither operation edits or restarts
the unsuffixed PRD unit. Linux has hard Dev/QAS caps plus a measured and
validated PRD headroom/floor; it does not claim a dedicated PRD cgroup
reservation. Mac has a hard storage bound plus remote-only controller
operation, as stated below.

### Versioned capacity policy

The initial pre-helper L0 is an unsigned, read-only privileged bootstrap
preflight and cannot issue a policy. After each helper and its attestation key
are installed, it performs a fresh local re-observation before any
host-management policy or authorization is issued. Each later fresh L0
observation is materialized as a canonical
`capacity_measurement_attestation`, signed by that host's root-protected
attestation key and published only through its fixed outbox. It names the
canonical host, observation time and expiry, measured CPU/memory/task/device
capacity, cgroup and slice facts, filesystem/volume/device identities, free
byte/inode and quota values, cache/staging/backup use and budget, listeners,
active-work inventory, and a canonical `capacity_evaluation_digest` over the
exact values used by the policy equations. The record contains safe measured
facts and evidence digests, never arbitrary paths, secrets, or a caller's
claimed result. The authority verifies it through the host key/ledger chain
before signing a policy.

A host accepts a signed `capacity_policy` only when it names one unexpired
`capacity_measurement_attestation` for that host and its expected
`capacity_evaluation_digest`. The root helper immediately repeats the relevant
measurement and deterministic policy equations: immutable hardware, device,
volume, cgroup, and listener identities must match exactly; dynamic values must
still meet or exceed every free-space, headroom, quota, and low-water bound in
the policy. Any stale, missing, tampered, wrong-host, changed-identity, or
insufficient live measurement rejects the policy before a service changes. It is
a host-specific, versioned release record rather than an installer default. Every policy has exactly one `policy_scope`:

- `host_management` authorizes only the root-owned builder, release transport,
  security anchor, release-transport PKI issuer/installer, and Mac-only
  authority signer/agent/approval tool. It names their bounded scope, host-wide
  cache/staging/backup budget, operating-system headroom, and the PRD safety
  floor; it never names a lane or authorizes a lane installer.
- `lane_runtime` names exactly one `release_lane` and authorizes only that
  lane's promotion. It carries the complete current host envelope: measured PRD
  floor/headroom, Dev/QAS hard caps, host-management budget, disk/inode/cache
  budgets, and every low-water threshold, including the storage identities
  required for the new lane. This makes each lane admission check the capacity
  already accounted for by its peers rather than claiming the host independently.

A `promotion_authorization` must name a current matching-host
`lane_runtime` policy for its `release_lane`; a
`host_management_authorization` must name a `host_management` policy. An
installer and its attestation reject a missing, expired, wrong-scope,
different-host, or measurement-mismatched policy. Changing a cap, disk device,
cache budget, or PRD floor produces a new signed policy and a fresh L0 result;
no service-manager setting is inferred from a previous measurement.

For Linux, a `lane_runtime` policy contains one cgroup-v2 slice hierarchy per
Dev/QAS lane, including exact `CPUQuota`/`cpu.max`, `MemoryMax`, `TasksMax`,
and per-device `io.max` values; the lane slice contains `runnerd`, the
dedicated queued SSH listener, and all bridge/session descendants. It also names
the complete per-host host-management matrix and budget:
`runner-release-builder`, `runner-release-transport`,
`runner-security-anchor-helper`, and `runner-release-transport-pki-installer`.
On the Mac, the corresponding policy additionally accounts for the authority
signer, authority agent, approval tool, and PKI issuer. The policy names the
measured logical CPU capacity, allocatable memory, PID/task capacity, and
block-device limits, and satisfies, for every constrained dimension:

```text
measured PRD floor + Dev hard maximum + QAS hard maximum + host-management budget
    <= measured host capacity
```

A Linux `host_management` policy contains only the corresponding bounded
management scope, PRD safety floor, cache/staging budget, and measured
headroom. It can be issued during the bootstrap before a Dev/QAS root or volume
exists, but it can never be substituted for a lane-runtime policy. Lane-runtime
pressure fixtures saturate each Dev/QAS listener, bridge, service, and lane
log/disk allocation separately; host-management fixtures separately saturate
build/staging and transport/helper scope. Together they prove cgroup membership,
refusal at the limit, and continued PRD liveness. A host without the required
cgroup-v2 and per-device I/O enforcement is ineligible for Dev/QAS installation.

Linux disk and inode fields reserve PRD baseline/floor, Dev and QAS hard
allocations, host-management build/release staging, and the normal Ubuntu Go
caches. The selected account continues to use its normal cache paths discovered
by `go env GOCACHE GOMODCACHE` (currently expected to be
`/home/ubuntu/.cache/go-build` and `/home/ubuntu/go/pkg/mod`), never lane
copies. Their exact paths and a bounded host-wide `ubuntu_go_cache_budget_bytes`
are in the policy. The host enforces that budget with a project quota or
comparable directory-capacity control; the lane installer never runs `go clean`
or deletes shared cache entries. If the normal cache paths differ, lack the
policy's bound, or their measured use plus reserved build growth would violate
the Linux 30-percent low-water, a build/promotion refuses before it starts.

For the Mac, both scopes require `compute_reservation: none` and forbid
CPU, RAM, PID, and aggregate-I/O reservation claims. A `host_management`
policy names only its cache/staging/log budget and the required host headroom.
A Dev or QAS `lane_runtime` policy requires its verified APFS volume UUIDs,
mountpoints, quotas, byte-admission budgets, cache budgets, log caps, snapshot
cap, and the `max(20 GiB, 15% of Data-volume capacity)` unallocated floor. A
PRD `lane_runtime` policy instead names `storage_reservation_mode` as either
`host_free_space_budget` for the existing unsuffixed root (including its
measured baseline and bounded V3 logs) or `apfs_quota` if separately approved
storage maintenance has provisioned a volume at that same unsuffixed path. A
policy can never turn background priority hints into a Mac compute reservation.
The host-management policy deliberately does not require future lane volumes,
so the bootstrap remains possible before the separately approved storage
handoff; a Dev/QAS installer still cannot start without the later scoped
lane-runtime policy and those verified volumes.

The controls are:

- Per-lane Runner limits for sessions, commands, output, workspace, temporary
  storage, retention, and mailbox backlog. Their sum preserves the measured PRD
  headroom/floor; no lane receives the current full host limit independently.
- Linux systemd resource controls for CPU, memory, tasks/processes, and I/O,
  with distinct Dev/QAS **slices** and hard caps whose combined maximum plus
  the separately capped host-management build/transport/helper scope preserves
  measured PRD headroom/floor. The lane slice, rather than `runnerd` alone, contains the
  direct listener, dedicated queued SSH listener, forced wrapper, bridge, and
  session-agent descendants. The future PRD V3 unit may add its own priority or
  limit, but it is not a prerequisite for Dev/QAS coexistence.
- Linux disk/inode low-water admission guard plus quota or equivalent
  reservation for lane roots, logs, backups, workspaces, retained output, build
  staging, the content-addressed release cache, and the normal Ubuntu Go-cache
  budget. If the host cannot enforce the selected disk reservation, Dev/QAS are
  not installed on it. Dev/QAS V3
  systemd units do not use the host-global journal for Runner stdout/stderr:
  they run the candidate-attested `runner-log-capture` wrapper with systemd
  stdout/stderr set to `null`. The wrapper captures the child streams under the
  lane root, rotates before a 64 MiB active file would overflow, retains at most
  seven rotated 64 MiB files and one 64 MiB temporary rotation file (576 MiB
  maximum total), and drops further diagnostic bytes with a durable
  `dropped_log_bytes` status counter. The unsuffixed V2 PRD unit may retain its
  existing journal output until L6; L0 treats that journal usage as PRD baseline
  consumption, never as a Dev/QAS lane reservation.
- On the Mac, Dev/QAS are remote-execution control planes only. macOS has no
  cgroup-equivalent aggregate CPU, memory, PID, or I/O hard reservation for
  independent LaunchAgents running as `tomasz.walczuk`: `ProcessType`, `Nice`,
  `LowPriorityIO`, and launchd/rlimit settings are priority or per-process hints,
  not lane reservations. They may be used as background policy but cannot satisfy
  a PRD compute-reservation gate. A contractual Mac CPU/RAM availability
  guarantee requires a separately sized VM or host; a separate macOS account by
  itself is insufficient.
- Mac Dev/QAS persistent data is hard-bounded instead. Before activation, each
  lane service root and every enabled external Dev/QAS mailbox root must be a
  pre-provisioned, quota-limited APFS volume at its configured mountpoint. The
  installer verifies (and never creates, resizes, or deletes) the expected volume
  UUID, mountpoint, owner, mode, and quota. A mounted mailbox root remains a
  non-symlink directory for descriptor/no-follow validation. A producer whose
  external root is not on its verified quota-bound volume is not enabled.
- Each Mac lane also has durable byte limits, reserved before acceptance, for
  ready-pair count/bytes, response/event/output bytes, SQLite/WAL, logs,
  workspaces, temporary scripts, backups, and staging. It returns a stable
  capacity result before persistence when its allocation is exhausted. Raw
  external writers cannot be claimed admission-bounded; their APFS quota is the
  protection. Dev/QAS LaunchAgents invoke the candidate-attested
  `runner-log-capture` wrapper and direct LaunchAgent stdout/stderr to `null`;
  the wrapper enforces the same 64 MiB active, seven rotated 64 MiB, one 64 MiB
  temporary, 576 MiB total log budget and exposes `dropped_log_bytes`. It owns
  atomic rotation before a write, so an append-only LaunchAgent file cannot
  bypass the quota/cap. V2 PRD append-only logs remain baseline consumption
  until L6 rather than a claimed PRD reservation.
- The shared Mac release cache and the shared Go caches at
  `/private/tmp/remote-session-runner-gocache` and
  `/private/tmp/remote-session-runner-gomodcache` have a separate host-wide
  policy budget and a preflighted quota or equivalent bound. A lane installer,
  rollback, or cache-pressure response never deletes their entries; any cache
  maintenance is a separately approved host operation. APFS quota protects lane
  data, not filesystem metadata, snapshots, or OS headroom; their projected use
  remains part of the host low-water calculation.

Before L6, the V2 PRD root remains on its existing shared volume, so the design
does not claim an APFS PRD disk reservation. Verified Dev/QAS quotas plus a
conservative Mac host-free-space budget limit their storage consumption. At L6,
the V3 PRD units and LaunchAgents must also use `runner-log-capture`, direct
system/launchd stdout and stderr to `null`, and prove the same 576 MiB
rotation cap before `committed`; its policy budget includes that cap. Existing
host `sshd` authentication diagnostics remain host-baseline consumption. L6 may
introduce a pre-provisioned PRD volume mounted at the same unsuffixed path only
through separately approved host storage maintenance; only then may PRD claim a
hard Mac disk reservation.

Mac control-plane CPU, RAM, PID, and aggregate I/O contention is an explicit
accepted non-guarantee in this first rollout. L4/L5 record background policy,
observed controller use, and storage safety, but cannot prove that a faulty
Dev/QAS controller will not affect the shared PRD control plane. If PRD's
reliability requirement includes that compute guarantee, a separately sized VM
or host is a precondition to L4 rather than an optional later improvement.

L0 is read-only: it records the measured bytes, inodes, memory, CPU, listener
ownership, cgroup topology, device I/O capability, host-management build/release
scope, retention, bridge/mTLS readiness, active work, filesystem type, normal
Ubuntu Go-cache paths/use, APFS volume identity/quota, snapshots, shared-cache
use, and the separate release-cache-backup volume identity/quota/use. Before
L3 this remains unsigned read-only preflight; after the helper/key bootstrap, a
fresh local re-observation produces the canonical capacity-measurement
attestation before an authority may issue a policy from it. Before L3, route
readiness means existing PRD-route health
plus the firewall/NAT/listener prerequisites for the deliberately absent `:9443`
and Dev/QAS routes; it does not require a connection to a listener that does not
exist yet. The bootstrap validates actual release-transport mTLS immediately
after it creates that listener, and L4/L5 validate actual lane endpoints. L0
derives a proposed `host_management` policy and, once the storage handoff exists,
a proposed `lane_runtime` policy for each contemplated lane; it does not install
either. A pre-L3 `host_management` L0 passes when the
management scope, PRD safety floor, cache/staging/log budget, and host headroom
are enforceable; it may record future Dev/QAS storage as absent. A lane-runtime
L0 passes only when Linux can enforce every named Dev/QAS/host-management cap
and every required Mac lane storage bound plus projected host headroom is
sufficient. On Linux, projected PRD + Dev + QAS + host-management peak usage
must leave at least 30 percent free bytes and inodes. On the Mac, non-purgeable
usage plus measured APFS snapshot usage plus the configured maximum Dev/QAS
volume quotas, shared-cache budget, and the release-cache-backup allocation when
it shares the Data volume must leave `max(20 GiB, 15% of the Data-volume
capacity)` unallocated. A backup on a distinct device instead names that device
and its independently measured low-water floor. Runner-managed snapshots have a separate
10 GiB aggregate budget and are counted in measured usage; missing snapshot
accounting or a budget overrun is a stop. L2 proves the selected controls in a
staged fixture; L4/L5 prove they are active before their new service starts.
The previously reported near-full ARM64 root filesystem is a hard stop. A
degraded route or retained work that an installation could touch is also a stop
condition.

After coexistence is established, a Dev refresh quiesces and verifies Dev only;
it reads PRD/QAS health and measured capacity/headroom but does not require their
mailboxes to be empty or stop their services. QAS follows the equivalent rule.
PRD uses its own scheduled maintenance window.

## 7. Source delivery, release records, and promotion

The authoritative source checkout remains the Mac `dev` checkout. The current
Gitea-primary → GitHub-mirror → clean Ubuntu `dev` checkout workflow remains
the source-delivery rule. QAS and PRD are **not** source-edit branches or
deployment worktrees: their services run a selected immutable release artifact,
while tracked source updates remain on `dev`.

### Candidate build cohort and component matrix

A candidate is an immutable **build cohort**, identified by its candidate
manifest record ID. A source SHA may produce more than one candidate only when
its pinned build inputs or recorded evidence differ. A build cohort is complete
only when it contains every tuple in this fixed version-1 component matrix:

| `host_id` | Platform | Required lane-runtime components |
| --- | --- | --- |
| `mac-control-plane` | Darwin / host architecture | `runner`, `runner-local`, `runner-locald`, `runner-log-capture` |
| `linux-host` | Linux / AMD64 | `runnerd`, `runner-ssh-bridge`, `runner-session-agent`, `runner-log-capture` |
| `sandbox-host` | Linux / ARM64 | `runnerd`, `runner-ssh-bridge`, `runner-session-agent`, `runner-log-capture` |
| `factory-sandbox` | Linux / AMD64 | `runnerd`, `runner-ssh-bridge`, `runner-session-agent`, `runner-log-capture` |

`release_component_matrix_version: 1` is part of every **lane-runtime**
candidate manifest. Its artifact list must be sorted by `(host_id, component)`,
contain every row in the matrix exactly once, and contain no extra row. A change
to that matrix is a new reviewed matrix version and a new candidate; an
installer cannot silently skip a component because a host or profile name
happens to be absent.

The candidate manifest contains the exact Mac source commit, the matching
Gitea and GitHub commit IDs, and a unique artifact tuple for every matrix row:
`host_id`, `component`, `goos`, `goarch`, pinned toolchain digest,
build-input digest, and artifact digest. It also contains the shared V3
configuration-template/schema digest, test-evidence references, build cohort
timestamp, and the exact required host-management manifest ID defined below. It
contains no lane decision, installed path, endpoint, secret, or resolved lane
configuration. The active repository is SHA-1, so V1 source commit fields are
exactly 40 lowercase hexadecimal characters; moving to a SHA-256 Git repository
requires a new release-record version.

### Host-management control-plane release

`runner-release-builder`, `runner-release-transport`,
`runner-security-anchor-helper`, and the constrained release-transport PKI
installer are host-global control-plane components.
`runner-release-authority-signer`, `runner-release-authority-agent`,
`runner-release-authority-approval`, and the constrained release-transport PKI
issuer are Mac-only host-management components. They never live below a
Dev/QAS/PRD root and a Dev, QAS, or PRD promotion never updates them. They use
this separate fixed host-management matrix:

| `host_id` | Platform | Required host-management components |
| --- | --- | --- |
| `mac-control-plane` | Darwin / host architecture | `runner-release-builder`, `runner-release-transport`, `runner-security-anchor-helper`, `runner-release-authority-signer`, `runner-release-authority-agent`, `runner-release-authority-approval`, `runner-release-transport-pki-issuer`, `runner-release-transport-pki-installer` |
| `linux-host` | Linux / AMD64 | `runner-release-builder`, `runner-release-transport`, `runner-security-anchor-helper`, `runner-release-transport-pki-installer` |
| `sandbox-host` | Linux / ARM64 | `runner-release-builder`, `runner-release-transport`, `runner-security-anchor-helper`, `runner-release-transport-pki-installer` |
| `factory-sandbox` | Linux / AMD64 | `runner-release-builder`, `runner-release-transport`, `runner-security-anchor-helper`, `runner-release-transport-pki-installer` |

A signed `host_management_manifest` names that complete matrix, its exact
host-build attestations, protocol compatibility version, root-owned installation
generation, and source/build inputs. A signed
`host_management_authorization` names one host, exact artifact subset, manifest,
and a `host_management` capacity-policy ID plus validity/ledger constraints; a
host-signed `host_management_attestation` records the verified result and
observed policy. A lane-runtime policy is invalid here, just as a
host-management policy is invalid for a lane promotion.
These records follow the same canonical, key, ledger, time, anchor, cache, and
idempotence rules as other release records but are not lane records. The first
privileged bootstrap has no prior artifact to roll back to: after fresh L0 it
installs and verifies the complete Mac host-management matrix row, including the
anchor, signer, authority agent/approval tool, constrained PKI issuer and
local installer, builder, local transport, and release-transport CA/client trust
material, before each Ubuntu host is bootstrapped one at a time with its
constrained PKI installer. A local-socket or exact mTLS/time-probe gate is
required before advancing. It signs no management manifest until every host
succeeds. A bootstrap failure preserves evidence, removes or disables only newly
installed management artifacts/listener/trust material, and requires a fresh L0
before retry. Later management updates use a scheduled host-management maintenance
window, fresh L0/capacity proof, one-host-at-a-time staged start, health/time
probe, and rollback to the prior management artifact before their management
commit. Before each later host update they require a fresh ledger and every
affected lane directive to have at least 900 seconds remaining; the staged
update plus verified rollback has a 600-second maximum. If that window cannot
be guaranteed, the authority first performs a planned signed closure for the
affected lane rather than allowing an unplanned lease expiry. Under those
conditions it does not change an active lane's root, process, configuration,
mailbox, or ingress state. After its commit, repair follows the verified
management manifest; a lane deployment that requires a different management
manifest waits for that compatible global baseline.

Each lane-runtime candidate names one exact compatible
`required_host_management_manifest_id`. A host accepts its lane authorization
only when its root-owned anchor reports that manifest installed and healthy;
otherwise it rejects before staging lane artifacts. This prevents a Dev/QAS/PRD
release from silently changing shared transport, builder, or anchor code.

The host OpenSSH daemon binary is a protected operating-system dependency, not a
candidate component. Its package/version digest, root-owned lane configuration,
port, host-key fingerprint, and slice attachment are resolved configuration
inputs and are attested with the affected installation.

### Closed release-record protocol

Host-build attestations, lane and host-management manifests/authorizations/
attestations, lane acceptances, ingress directives/applied-state attestations,
capacity policies, host and authority key sets, and revocation ledgers are
immutable signed records. V1 uses this closed JSON
envelope:

```json
{
  "release_record_version": 1,
  "record_type": "candidate_manifest",
  "payload": {},
  "signature_algorithm": "Ed25519",
  "signer_key_id": "release-authority-2026-01",
  "record_id": "lowercase-sha256-hex",
  "signature": "base64-ed25519-signature"
}
```

`record_type` is exactly one of `host_build_attestation`,
`candidate_manifest`, `promotion_authorization`, `promotion_attestation`,
`lane_acceptance`, `ingress_admission_directive`,
`ingress_admission_attestation`, `capacity_measurement_attestation`,
`capacity_policy`, `pki_certificate_request`, `host_management_manifest`,
`host_management_authorization`, `host_management_attestation`,
`host_attestation_key_set`, `release_revocation_ledger`, or `authority_key_set`.
`release_record_version` is the integer `1` and
`signature_algorithm` is
exactly `Ed25519`. The signed body contains only the first five fields in the
order-independent JSON object above. The envelope has no duplicate keys,
unknown fields, non-finite numbers, comments, or version-1 extension fields.
It is stored and transferred as the byte-for-byte RFC 8785 JSON
Canonicalization Scheme (JCS) UTF-8 encoding. `record_id` and `signature` are
derived envelope fields and are not included in the signed body.

For record type `T`, its ID digest and its Ed25519 signature use the identical,
domain-separated bytes:

```text
ASCII("remote-session-runner/release-record/" + T + "/v1") || 0x00 || JCS(signed_body)
```

`record_id` is lowercase hexadecimal SHA-256 of those bytes. `signature` is
standard base64 Ed25519 over the same bytes, verified against the pinned
`signer_key_id`. A parser re-encodes the received envelope, rejects a
non-canonical form, checks the closed typed payload schema, recomputes both the
ID and signature input, and rejects an unknown type, version, key, digest, or
signature. The planned checked-in schemas at
`src/internal/release/schemas/v1/` define every payload field, type, maximum
length, enum, and required/forbidden combination; their exact digest is part of
the V3 template/schema contract. L3 supplies golden body/ID/signature vectors
that must verify unchanged on macOS, Linux AMD64, and Linux ARM64.

All `*_digest` fields use `sha256:` followed by 64 lowercase hexadecimal
characters. Arrays that name artifacts, predecessors, evidence, revoked
records, or revoked keys are sorted and unique. All timestamps are RFC 3339
UTC values with a trailing `Z`. Schema validation occurs before a record can be
cached, used as predecessor evidence, or affect admission.

The V1 payload contracts are:

| Record type | Required payload identity and content |
| --- | --- |
| `host_build_attestation` | Canonical host ID; `release_track` (`lane_runtime` or `host_management`); one corresponding matrix component/platform tuple; the three equal source commit IDs; toolchain and build-input digests; artifact digest; V3 template/schema digest; evidence references; and build timestamp. It is signed by the root-protected host attestation key before a manifest exists. |
| `candidate_manifest` | `release_component_matrix_version`, exact `required_host_management_manifest_id`, the three equal source commit IDs, build cohort timestamp, V3 template/schema digest, evidence references, and the exact lane-runtime matrix of host/component artifact tuples, each naming its verified host-build-attestation record ID. |
| `promotion_authorization` | Candidate manifest ID; canonical UUIDv7 `lane_transition_id`; `release_lane`; one canonical `host_id`; exact signed matching-lane `lane_runtime` `capacity_policy_id`; the exact component/artifact subset for that host; exact resolved configuration digest; installation generation; required predecessor `lane_acceptance` ID (absent only for Dev); `not_before`, `not_after`, and required revocation-ledger generation. |
| `promotion_attestation` | Authorization, candidate, `lane_transition_id`, and capacity-policy ID; lane, host, component/artifact subset, resolved configuration digest, installation generation, accepted/rejected outcome, evidence references, and observed timestamp. Its signer must be the pinned attestation key for its `host_id`. |
| `lane_acceptance` | Candidate manifest ID, lane, `lane_transition_id`, complete sorted accepted promotion-attestation IDs, complete live-acceptance evidence digest (mailbox/ACK, direct, queued, cross-lane denial, resource/postflight), acceptance timestamp, and authority signer. It is immutable historical evidence with no validity interval: its timestamp is audit information, not a lease. It is issued only after the full lane acceptance contract passes. |
| `ingress_admission_directive` | `candidate_manifest_id` (64 lowercase SHA-256 hex) and `lane_transition_id` (canonical lowercase UUIDv7); lane; canonical host ID; exact resolved configuration digest; `directive_role` of `active_runtime_closure` or `candidate_admission`; transition epoch; admission generation (`0` closed, `1` transport-ready, `2` open); lease generation; validity interval; and required revocation-ledger generation. An `active_runtime_closure` directive is only generation `0` and names the currently active runtime. A `candidate_admission` directive names the target candidate and may progress from closed to transport-ready to open. The release authority is the only signer. |
| `ingress_admission_attestation` | Exact directive record ID, candidate, transition, lane, canonical host ID, resolved configuration digest, directive role, epoch, admission generation, lease generation, and the exact sorted required ingress-surface inventory. For each surface it records applied/rejected/expired outcome and safe evidence digest, plus observed time. It is signed by that host's root-protected attestation key; only an exact aggregate `applied` result covering every required surface can satisfy closed, transport-ready, or open transition gates. |
| `capacity_measurement_attestation` | Canonical host ID; observation/expiry timestamps; canonical inventory and safe values for CPU/memory/task/device capacity, cgroup/slice membership, filesystem/volume/device identity, free bytes/inodes, quota, cache/staging/backup use, listeners, and active work; evidence references; and the deterministic `capacity_evaluation_digest`. It is signed by the root-protected attestation key for that host after the helper performs the L0 measurement. |
| `capacity_policy` | `capacity_policy_version: 1`; canonical host ID; `policy_scope` of exactly `host_management` or `lane_runtime`; exact unexpired `capacity_measurement_attestation_id` and expected `capacity_evaluation_digest`; and validity interval. A `host_management` policy has only the bounded management scope, cache/staging/backup/log budget, PRD safety floor, and measured headroom. A `lane_runtime` policy has exactly one `release_lane`. On Linux, Dev/QAS require their cgroup-v2 lane slice/scope and hard CPU, memory, task, and per-device I/O caps plus measured PRD headroom/floor; PRD records the measured unsuffixed host-baseline scope/floor and optional separately configured PRD controls without claiming a dedicated slice. On the Mac, Dev/QAS require APFS volume/quota/byte/cache/log/snapshot/headroom fields; PRD names either its measured `host_free_space_budget` or separately provisioned `apfs_quota` mode. Every lane-runtime policy also names disk/inode and normal-Go-cache budgets and the host-management reserve. It is signed by the release authority after verifying the named measurement. |
| `pki_certificate_request` | One canonical host ID; a one-use `pki_issuance_mandate_id`; exact currently installed management-manifest/installation generation, or during privileged bootstrap the exact bootstrap-contract digest/bootstrap installation generation, installer artifact tuple, and pinned bootstrap host-attestation key ID; leaf role; URI/IP SAN tuple; DER CSR bytes and public-key fingerprint; CA profile and requested bounded validity. It is signed by that host's root-protected attestation key, may be published only by its attested PKI installer through the fixed outbox, and contains no private key. |
| `host_management_manifest` | `host_management_matrix_version`, host-management protocol compatibility version, exact root-owned bootstrap/update contract digest, the three equal source commit IDs, and the complete host-management matrix of artifact tuples/host-build attestation IDs. Its release-transport contract contains the exact CA/trust/leaf inventory, host/URI/IP binding, and signed active-peer certificate fingerprint/SPKI allowlist (including any bounded overlap) whenever those values change. |
| `host_management_authorization` | Host-management manifest ID; one canonical host ID; exact signed `host_management` `capacity_policy_id`; exact host-management artifact subset; root-owned installation generation; `not_before`, `not_after`, and required ledger generation. Its tagged predecessor is mutually exclusive: a normal update names the exact installed management-manifest ID/generation and rollback artifact, while the unique first baseline uses `predecessor_kind: bootstrap` only while the local anchor is bootstrap-only with no installed manifest, and binds the approved bootstrap-contract digest, bootstrap installation generation, and immutable completed-cohort/bootstrap-evidence digest. An absent predecessor or bootstrap predecessor after baseline is rejected. When release-transport PKI changes, it additionally binds the exact `pki_certificate_request_id`, issuer receipt, non-secret certificate/trust install package bytes and digest, active-peer-set digest, and issuer/installer tuple; it never transports a private key. |
| `host_management_attestation` | Host-management authorization/manifest and capacity-policy IDs; canonical host ID; exact component/artifact subset; observed root-owned installation generation, compatibility/probe health, accepted/rejected outcome, and evidence references. |
| `host_attestation_key_set` | Canonical host ID; monotonic per-host attestation-key-set generation; exact predecessor record ID; previous and successor key IDs/public keys; activation/retirement interval; and successor proof of possession. It is signed by a currently trusted release-authority key and binds the only key eligible to sign host-build, promotion, ingress-admission, capacity-measurement, PKI-certificate-request, and host-management attestations for that host. |
| `release_revocation_ledger` | Monotonic ledger generation, exact predecessor-ledger record ID, issue/expiry timestamps, and cumulative sorted revoked record IDs and key IDs. |
| `authority_key_set` | Monotonic key-set generation, exact predecessor-key-set record ID, previous and successor key IDs, successor public key, activation/retirement interval, and a successor proof of possession. It is signed by the previous trusted authority key. |

The V1 signer role is closed by record type. Only a currently trusted
release-authority key may sign `candidate_manifest`,
`promotion_authorization`, `lane_acceptance`,
`ingress_admission_directive`, `capacity_policy`,
`host_management_manifest`, `host_management_authorization`,
`host_attestation_key_set`, and `release_revocation_ledger`. Only the pinned
root-protected attestation key for the exact canonical host may sign
`host_build_attestation`, `promotion_attestation`,
`ingress_admission_attestation`, `capacity_measurement_attestation`,
`pki_certificate_request`, or `host_management_attestation` for that host. `authority_key_set` alone uses its named previous trusted authority key.
The cache, transport, anchor, and registry enforce this type-to-signer mapping
before treating a signature as valid; a cryptographically valid signature from
any other key ID is rejected.

A promotion authorization is consumed only for the exact candidate,
`lane_transition_id`, lane, host, artifact subset, resolved configuration
digest, capacity policy, and installation generation it names. Its record ID is
the authorization identity. A retry may return the durable result for that same
record ID and generation; it cannot perform a second installation or satisfy
another tuple. An `accepted` attestation is installation evidence only when
every field exactly matches its authorization. A failed, rejected, or incomplete
attestation remains evidence but cannot satisfy a predecessor requirement. Only
a release-authority-signed `lane_acceptance`, issued after the complete live
suite, can satisfy the QAS or PRD predecessor requirement. The closed schema
requires no predecessor for Dev; a QAS authorization must name a valid Dev
acceptance and a PRD authorization a valid QAS acceptance. In both cases the
acceptance must have the identical candidate-manifest ID, a complete accepted
matrix attestation set for its own lane, and a valid authority signature. A
wrong-lane, different-candidate, incomplete, revoked, or structurally invalid
acceptance is rejected before staging. A valid acceptance has no independent
expiry: it remains predecessor evidence while its authority signature, complete
referenced evidence, candidate identity, and required ledger/key chain remain
valid and unrevoked. Each new installation still needs fresh capacity policy,
authorization, directive, and time proof; acceptance never extends those leases.

The shared configuration-template/schema digest is deliberately distinct from a
resolved configuration digest: the latter differs by lane, host, root, port,
and credential fingerprint. The installer validates each resolved configuration
against the candidate template/schema and accepts only the exact digest named
by its authorization. It does not require Dev, QAS, and PRD configuration bytes
to be identical.

### Time, revocation, and signing trust

V1 has a fail-closed `release_time_policy_version: 1`: a verified authorization
or directive is valid only while its local UTC clock lies in its signed interval;
the maximum authorization/directive lifetime is 3,600 seconds. Before applying
one, a host requires both a healthy local time-synchronization state and a
fresh nonce-bound authority-time probe. The host never trusts caller-supplied
nonce or timing values. Through the already authenticated fixed `:9443`
release-record transport, the Mac authority calls
`POST /v1/time-probe/challenge` on the target host. The release transport asks
its local security-anchor helper to create and durably correlate a newly random
32-byte base64url nonce with `(wall_t0, monotonic_t0)` and a single-use
challenge ID. The Mac immediately PUTs the signed response to
`/v1/time-probe/<challenge_id>` on that same host. The release transport passes
its bytes to the helper, which alone records `(wall_t1, monotonic_t1)` and
consumes the challenge. A response cannot be accepted through any Runner lane,
mailbox, queued bridge, or arbitrary local caller.

The authority response is a JCS object with `time_probe_version: 1`, that
nonce, `authority_utc_unix_millis`, and `signer_key_id`, plus a base64 Ed25519
signature over:

```text
ASCII("remote-session-runner/release-time-probe/v1") || 0x00 || JCS(probe_body)
```

The helper verifies the nonce and trusted signer, rejects
`monotonic_t1 - monotonic_t0` above two seconds, and calculates
`offset = authority_time - (wall_t0 + wall_t1) / 2`. It accepts the probe only
if `abs(offset) + (monotonic_t1 - monotonic_t0) / 2` is at most five seconds. It
persistently records the highest accepted authority UTC time and rejects a later
local clock rollback greater than one second. There is no grace period for an
expired, unsynchronized, replayed, delayed, or backward-moving clock. L3
fixtures cover expiry, five-second-plus skew, nonce replay/mismatch, delayed
responses, a missing time proof, helper-unavailable timing, and clock rollback.

A host maintains a contiguous, cumulative revocation-ledger chain. Generation
`n` names the exact accepted record ID for generation `n-1` and repeats every
prior revoked record/key ID plus any newly revoked IDs; omission is an invalid
attempt to un-revoke. Generation `1` names the pinned bootstrap-ledger anchor.
A host rejects a skipped predecessor, generation fork, conflicting record ID,
or non-superset revocation list, and never replaces its high-water ledger with
an older record. A V1 ledger has a maximum lifetime of 900 seconds and must be
refreshed at least every 300 seconds while an installed lane can accept
mutation.

Validation first verifies the ledger's predecessor chain and envelope signature
with a currently trusted key **before** applying its new revocations. The
ledger cannot revoke the key that signed that ledger. A ledger signed by a
different currently trusted, unrevoked authority successor may revoke an older
authority key immediately; a self-revoking ledger is invalid. After application,
a candidate, authorization, directive, capacity policy, or attestation is
usable only if neither it nor any record/key required to validate it is
revoked. A ledger that revokes a referenced host-attestation signer or authority
key therefore invalidates that promotion path rather than becoming invalid
itself. This permits emergency key retirement without permitting a record to
validate itself with a revoked key.

If the cached ledger has expired, mutation remains closed, but the transport is
not deadlocked: after a fresh helper-owned authority-time probe it may accept
only a strictly contiguous, correctly signed successor
`release_revocation_ledger` or `authority_key_set` rooted in the anchor's last
verified predecessor. It atomically validates and advances that chain in the
anchor before accepting any other record. This recovery exception never accepts
a candidate, authorization, directive, capacity policy, or attestation while
the ledger is stale, and a successor with a fork, skipped predecessor, expired
or self-revoking signer, or invalid time proof is rejected. Once a fresh ledger
is installed, ordinary record validation resumes.

The initially installed authority public key and key-set anchor are explicit
host trust anchors; there is no trust-on-first-use path. An
`authority_key_set` advances exactly one contiguous key-set generation, names
the preceding accepted key-set record ID, and has `previous_key_id` exactly
equal to the envelope `signer_key_id`. A host rejects a skipped predecessor,
generation fork, conflicting record ID, key-set record signed by another key,
or a successor whose activation interval has no overlap with a still-valid
trusted signer. Its payload's `successor_proof` is a standard-base64 signature
made by the successor key over:

```text
ASCII("remote-session-runner/release-authority-key-possession/v1") || 0x00 || JCS(key_set_payload_without_successor_proof)
```

The host verifies both the old key's record signature and the new key's
possession proof before making the successor eligible at its signed activation
time; it retains the old key until its signed retirement time. A ledger signed
by the other currently trusted key can end either key immediately under the
validation order above.

`host_attestation_key_set` has the equivalent explicit rotation contract. Its
per-host generation `1` names the immutable bootstrap-attestation-anchor ID
installed in the root helper; each later generation names the exact previously
accepted key-set record ID, increments by one, and is signed by a current
release-authority key. Its `successor_proof` is standard-base64 Ed25519 made by
the successor host key over:

```text
ASCII("remote-session-runner/host-attestation-key-possession/v1") || 0x00 || JCS(host_attestation_key_set_payload_without_successor_proof)
```

The helper verifies the authority signature, canonical host ID, bootstrap or
predecessor identity, generation, old/successor IDs, proof, and overlapping
old/new validity interval before allowing the successor. It rejects a skipped
predecessor, generation fork, wrong-host key, conflicting record ID, invalid
proof, or no-overlap activation; ledger revocation ends either host key
immediately. L3 publishes golden vectors for bootstrap generation and successor
rotation on all platforms.

### Security anchor and protected local state

Every host has a host-management-attested, root-owned
`runner-security-anchor-helper`. Its protected state lives outside lane roots,
release caches, and continuity bundles: on the Mac at
`/Library/Application Support/RemoteSessionRunner-security-anchor/` and on
Linux at `/var/lib/remote-session-runner-security-anchor/`. The directory and
its SQLite state are root-owned `0700`; the helper uses a full-sync transaction
and directory fsync before reporting an observation accepted. The state is
**not restored from a Runner continuity bundle**. That is a protocol guarantee,
not a claim that a physical root administrator could never perform an audited
security rebootstrap.

The helper is a root LaunchDaemon on the Mac and a root systemd service on
Linux. It exposes only a local Unix-domain `observe` protocol at
`/var/run/remote-session-runner/security-anchor.sock` (the corresponding Mac
launchd socket has the same pathname). Socket peer credentials must be the
configured Runner service UID (`tomasz.walczuk` on the Mac or `ubuntu` on Linux)
and the socket group is merely a transport permission; the helper verifies the
UID again. It accepts no file path, counter, digest, or trust decision supplied
by the caller. An `observe` request contains canonical record/probe bytes and
the expected local host/lane/configuration context. The helper itself performs
closed-schema, canonical-form, signature, key-set, cumulative-ledger,
revocation, authority-time, host, lane, candidate, configuration, and monotonic
tuple validation before writing state. It returns only the verified high-water
result. An unavailable helper, invalid peer, schema error, or state mismatch
closes admission before any mutable resource is persisted.

The Mac helper also exposes a distinct read-only
`/var/run/remote-session-runner/security-anchor-authority-state.sock` endpoint
to `runner-release-authority-agent` only. It verifies the agent's launchd audit
token, configured launchd label, and code identity; UID `0` by itself is not a
sufficient identity. This endpoint returns the helper-selected, immutable
snapshot of verified key/ledger/high-water/time state needed to authorize a
root authority transaction. It accepts no record body, nonce, digest, path,
key, or trust choice from that agent and cannot mutate anchor state. The normal
`observe` socket remains available only to the configured Runner service UID;
the agent cannot use it as a more privileged generic interface, and a Runner
or shared-UID caller cannot use the authority-state endpoint.

Each host separately exposes
`/var/run/remote-session-runner/security-anchor-release-transport.sock` only to
its installed `runner-release-transport` principal. On the Mac the helper
validates that LaunchDaemon's audit token, label, and code identity. On Linux it
validates the dedicated non-login `runner-release-transport` service UID, the
expected systemd unit/cgroup, and the measured installed executable identity;
an arbitrary `ubuntu` process or root peer is rejected. This transport-only
endpoint accepts only six fixed operations: create and consume the correlated
time-probe challenge/response; verify/cache an addressed release record;
retrieve a verified host outbox record; `attest-capacity-measurement`; and
`attest-pki-certificate-request`. The capacity operation accepts no path, value,
policy, or caller-selected profile: the helper reads only its fixed host inventory
and installed measurement contract, performs descriptor/no-follow and
kernel/service-manager inspection itself, signs the resulting capacity-measurement
attestation with the host key, and writes it to the protected outbox. The PKI
operation accepts only the opaque mandate ID from the authenticated authority
peer (or the bootstrap-contract tuple during first bootstrap). The helper invokes
the measured local PKI installer for the fixed host role, reads its locally held
CSR only through a fixed root-owned descriptor, derives the URI/IP SAN tuple,
checks the expected installer identity and one-use scope, and signs/writes the
fixed outbox request. No caller supplies CSR, SAN, path, or attestation-key
bytes. It accepts no arbitrary `observe`, path, counter, key, or trust-state
mutation. Thus the transport can obtain the helper
checks required by the `:9443` protocol without expanding either the Runner
`observe` socket or the authority-agent endpoint.

The anchor records the accepted ledger and key-set record IDs/generations, the
highest accepted authority UTC millisecond, the highest directive
`(transition_epoch, admission_generation, lease_generation)` tuple for each
`{lane, host}`, and the associated directive role, candidate, configuration,
and record identities. It also records the active-runtime and, while staging,
the root-installed pending-candidate context for that lane. A normal V3 restore
merges only verified records at or above that state; a bundle that lacks the
required high-water record or conflicts with it fails closed. This prevents an
older valid bundle from re-enabling a revoked record, replaying an earlier
directive, or accepting a rolled-back clock.

The helper's initial root-owned binary, pinned bootstrap key-set/ledger anchor,
host-attestation signing key, socket ownership, release transport, Mac-only
release-authority signer, authority agent, approval tool, and fixed bootstrap
`runner-release-builder` are installed only through a separately approved
privileged bootstrap preflight.
That approval records the bootstrap builder's exact source commit,
toolchain/input and binary digests, allowed host/component tuple set, root-owned
pathname, and unprivileged execution UID.
The helper accepts it only at that recorded pathname without a `PATH` lookup;
the attestation private key is never readable by `tomasz.walczuk`, `ubuntu`, or
a submitted script. Its constrained `attest-build` operation accepts no
caller-selected artifact path: it creates a root-owned build job from the
verified clean source SHA, exports that exact commit into sealed staging, runs
the fixed bootstrap builder as the unprivileged build identity with a fixed
toolchain/input set, hashes the protected output, and signs only the matching
canonical host-build attestation. The first `host_management_manifest` may
attest that bootstrap builder as its builder component; after the first verified
management installation, every later build uses the installed builder admitted
by the preceding healthy management manifest. The bootstrap builder cannot be
used as a general untrusted build service or as a lane artifact.
Its distinct constrained `attest-install` operation reads the root-owned unit,
cache, anchor, configuration, capacity policy, and health evidence for the exact
authorization before it signs a promotion or host-management attestation. Its
`attest-ingress-applied` operation reads the exact verified directive, admission
state under the lane lock, and the configuration-derived complete ingress-surface
inventory; it signs and atomically writes an aggregate
`ingress_admission_attestation` to the host outbox only when every surface has
the named outcome. An arbitrary same-user process cannot forge an attestation,
choose its evidence paths, or replace its sealed output. Later helper replacement is allowed only by a verified same-host
host-management artifact and `host_management_authorization`, using the existing
helper to validate the replacement's signed management transition before the
root installer swaps it. L3 fixtures cover unauthorized UID/input/path attempts, malformed or
stale records, forged signer attempts, crash during anchor update, a lower-state
restore, key/ledger fork, clock rollback, and attempted downgrade of the helper
itself.

The Mac release authority signs every release-authority-signed V1 record in the
closed signer map in section 7 — `candidate_manifest`,
`promotion_authorization`, `lane_acceptance`,
`ingress_admission_directive`, `capacity_policy`,
`host_management_manifest`, `host_management_authorization`,
`host_attestation_key_set`, `release_revocation_ledger`, and
`authority_key_set` — only through the root-owned Mac
`runner-release-authority-signer` LaunchDaemon. `authority_key_set` still uses
its named previous trusted key, but that signing operation remains inside the
same root-owned signer. No Runner, lane, build, transport, or mailbox component
has an alternative authority-signing path. Its Ed25519 private key is
root-owned `0600` beneath:

```text
/Library/Application Support/RemoteSessionRunner-release-authority-signer/release-authority.ed25519
```

The signer private key and successor private keys during an approved overlap are
root-owned `0600`; approved mandates, consumed-approval/replay journal,
accepted authority-record IDs, and verified transition/high-water snapshot live
in the root-owned `0700` sibling directory
`/Library/Application Support/RemoteSessionRunner-release-authority-signer/state/`.
That persistent authority state is outside the signer artifact, every lane root,
the release cache, and all continuity bundles. A verified host-management update
may replace signer code, but the root installer must preserve and validate this
state and its public-anchor identity; it cannot reset a replay counter, replace
an authority key, or restore an older authority state. The state is never
imported from a Runner continuity bundle.

Loss or corruption of either root authority state or any host security-anchor
state is an availability outage, not a normal restore path. The sole exceptional
**security rebootstrap** is an audited physical-root incident procedure, approved
outside the potentially lost release authority. It first preserves failed state
and evidence without importing them, blocks public ingress at each host, stops
all Runner lane units, and records the resulting unscheduled closure; it cannot
claim a normal signed closure from damaged state. It then creates new authority
and anchor roots through the fixed serial bootstrap, re-enrols every host and
its release-transport trust, and rebuilds fresh host-management and lane
candidates. Old directives, ledgers, key sets, operation records, and
acceptances remain forensic history only and are never trusted under the new
anchor. No V3 lane may reopen until a new Dev → QAS → PRD acceptance chain is
complete. This procedure never revives V2, restores an old root-state snapshot,
or runs concurrently with normal promotion/repair; L7 rehearses it only in an
isolated recovery fixture.

`runner-release-authority-agent` is a separate root LaunchDaemon. The signer
socket accepts only that agent after validating its launchd audit token, label,
and code identity; it never accepts a Runner service, lane process, the shared
`tomasz.walczuk` UID, or an arbitrary root peer. The separately named
`runner-release-authority-approval` tool has no signer-socket access. After
interactive system authorization it may create one root-owned, fsync-recorded
mandate for an agent-staged pending plan, and it returns only the resulting
receipt. The agent imports a proposed canonical body through descriptor/no-follow
handling into root state; a mandate then binds its exact body digest, record
type, candidate/transition/predecessor context, ledger generation, and short
expiry. The signer consumes that exact approval once, checks the agent identity
and canonical body, and rechecks the root authority state, the read-only anchor
snapshot, required attestations, policy, and transition facts before signing.
The owner-only lane registry is never an authoritative signer input.

A `pki_issuance_mandate` is the sole non-V1 root-maintenance approval. The
approval tool creates it only after interactive authorization and it authorizes
one attested PKI installer to publish one request for one host. For normal
rotation it binds the current management-manifest/installation generation; for
the one-time approved bootstrap preflight only, it instead binds the exact
bootstrap-contract digest, bootstrap installation generation, attested installer
artifact tuple, and pinned bootstrap host-attestation key. The two alternatives
are mutually exclusive. The bootstrap alternative is scoped to its named host
and is disabled for that host after its first applicable local-socket or
mTLS/time-probe health gate; each not-yet-bootstrapped host needs its own
separately approved one-use mandate. Both bind host, leaf
role, exact URI/IP SAN tuple, CA profile, maximum validity/overlap, and expiry;
neither can sign a V1 record, change a lane, or authorize a second request. The
issuer verifies the matching normal or bootstrap context and the host-signed
`pki_certificate_request`, consumes both once, creates and journals only the
non-secret certificate/trust package receipt, and stops. The operator then
reviews that immutable receipt and creates a normal exact signer mandate for the
final `host_management_manifest` and authorization that bind its package
digest. This two-step sequence avoids a certificate-fingerprint circularity and
gives no component a generic CA or signing operation.

A mandate may additionally create a bounded root-owned **directive** renewal
grant for an already approved transition. It names a grant ID, immutable
directive context (role, lane, host, candidate, configuration, transition, and
admission state), starting and maximum directive-lease generations, an absolute
expiry, a finite issuance cap, and a `renewal_warning_at` that leaves at least
two measured lease/publication cycles for an operator to renew or close safely. Until that expiry/cap, the signer may sign
successive contiguous, predecessor-bound phase-preserving directive renewals
within those maxima without another interactive authorization. Each issuance
first fsyncs one append-only grant-journal entry and atomically advances that
grant's high-water/count before it returns a signature; no gap, fork, reuse, or
issue beyond the maximum is accepted.

The revocation ledger has one global chain, so it has at most one active
root-owned **ledger** renewal grant rather than one grant per transition. That
grant names the current predecessor record ID, starting and maximum ledger
generations, unchanged sorted revoked-record/revoked-key sets, absolute expiry,
a finite issuance cap, and a `renewal_warning_at` with the same two-cycle safe
lead time. A root-authority-state compare-and-swap issuer lock
must atomically confirm the current global predecessor, append one issuance
entry, and advance the global high-water before it signs the only next
generation. It publishes that signed successor to every host before another
generation is eligible. Concurrent transition activity can reuse the same
published ledger but cannot fork it. Neither kind of renewal grant can add a
revocation, candidate, lane, host, key, policy, role, phase, configuration, or
transition. A new authority meaning — including a changed directive phase,
authorization, acceptance, capacity/host-management record, key change, or
ledger revocation — needs a new exact operator mandate. A shared-UID script can
neither read or use a private key, create/alter a root mandate, nor replay a
consumed approval or renewal.

The authority agent durably monitors every active directive grant and the one
global ledger grant by grant ID/context, predecessor and current generation,
issued/max count, expiry, and `renewal_warning_at`. It emits a durable operator
alert at the warning threshold or when one issuance remains. The operator either
uses a new exact interactive mandate to create a successor grant with the same
permitted context, or starts a planned signed closure while current directives
remain valid; grants never self-extend. An exhausted or expired grant blocks the
next renewal and therefore fails closed. The global ledger alert also blocks new
transitions until its one successor grant is approved or affected lanes are
closed. L7 includes warning, cap, expiry, and operator-renewal drills.

Authority-time probes are the only deliberately approval-free signing operation:
the authority agent first obtains a single-use challenge through the authenticated
authority-agent peer route (remote mTLS for Ubuntu or the validated Mac local
socket), records its exact host-bound challenge ID/nonce and receipt time in root
authority state, and gives the signer no caller-selected response body. The
signer may sign exactly that canonical probe response once, only while the
challenge is fresh enough for the two-second host exchange, its authenticated
peer and canonical host ID agree, and the current anchor/key state is valid. It then
consumes the challenge. A replayed, stale, wrong-host, wrong-nonce, or
unrecorded challenge is rejected. This operation cannot sign a V1 record or
advance a lane.

An approved authority-key rotation is itself an exact `authority_key_set`
mandate. The root installer imports the successor key only into the same
root-owned state, derives and checks its public key, key ID, fingerprint, and
proof of possession against that exact record, preserves the old key for the
approved overlap, and never exposes either key to the Runner/shared UID. The
successor cannot become usable merely because a management update or state
restore supplied different key material.

The same privileged bootstrap creates or imports that key only on the Mac before
the first policy or release record, derives its public key, key ID, and
fingerprint, and proves they exactly match the pinned authority public anchor in
every host's root security-anchor bootstrap. It never distributes the private
key to Ubuntu. Its initially pinned public verification key, and only a verified
signed successor, is accepted in every host's owner-only release cache. Each
canonical host has a distinct root-protected release-attestation Ed25519 key.
The bootstrap mapping from canonical `host_id` to initial `signer_key_id` and
public key is installed with the root security-anchor bootstrap; it is not
inferred from a hostname, mTLS certificate, or first received attestation.
Thereafter the signed `host_attestation_key_set` forms a contiguous per-host
chain: its predecessor record, generation, old key ID, successor proof, validity
interval, and release-authority signature must verify before the successor is
eligible. A ledger may revoke an old host key immediately. The Mac root
release-authority state retains every host's accepted chain and verifies a
host-build or promotion attestation only against the key set valid for its
canonical host and observed timestamp; the authority agent obtains those records
through verified transport and the anchor snapshot. It may mirror only the
verified record IDs and safe outcomes to the owner-only lane registry. That host key signs only host-build, promotion, ingress-admission,
capacity-measurement, PKI-certificate-request, and host-management attestations
for its canonical host; it is never a service mTLS or queued-SSH credential.

Hosts verify the canonical manifest, authorization/directive, capacity policy,
attestation-key mapping, revocation ledger, time policy, and authority
signature before trusting a received record. The Mac verifies complete
host-attestation key/predecessor chains, outcomes, and evidence before it
issues the next authorization. L3 provisions the four bootstrap mappings and
fixtures unknown host/key, wrong-host key, skipped/forked key set, expired key,
revoked key, and successor rotation.

### Authenticated release-record transport

Release records use a dedicated transport; they never use a mailbox request,
queued SSH bridge, Runner direct API, source checkout, `scp`, `rsync`, or an
arbitrary shell command. Each Ubuntu host runs the host-management-attested
`runner-release-transport` listener at its fixed public `:9443` route bound to
its private address on `:9443`. The Mac release authority is the only client:

| Host | Release-record public URL | Private bind |
| --- | --- | --- |
| `linux-host` | `https://129.151.232.40:9443` | `10.0.0.200:9443` |
| `sandbox-host` | `https://132.226.205.205:9443` | `10.0.0.14:9443` |
| `factory-sandbox` | `https://145.241.255.66:9443` | `10.0.0.30:9443` |

`mac-control-plane` uses the same closed protocol through the root-owned local
Unix socket `/var/run/remote-session-runner/release-transport.sock`; it exposes
no public release-record listener. Only `runner-release-authority-agent` may
use that socket after its launchd audit token, label, and code identity validate.
The signer uses only its dedicated signer socket and the anchor uses only its
own protected interface; the release builder never receives a transport peer
credential. A Mac build or installation attestation is first written by the
root helper to a protected host outbox, then the authority agent retrieves it
through the closed transport after anchor validation. Mac host-build/promotion
attestations, management records, and its local nonce/time-probe exchange thus
pass through the same closed transport and anchor checks before becoming
coordination evidence, so the Mac cannot bypass all-host validation merely
because it is the authority host.

This is a second mTLS trust domain, separate from lane direct-control mTLS. The
Mac client URI is
`urn:remote-session-runner:release-authority:mac-control-plane`; server URIs
are `urn:remote-session-runner:release-transport:host:<host_id>` and their
literal public IPs are IP SANs. Firewall/security rules permit only the
required authority-to-host route. The listener runs in the policy's bounded
host-management scope and accepts no lane controller credential.

The Mac local socket is not TLS. For every Mac-local operation,
**authenticated authority-agent peer** means a transport-validated launchd audit
token, fixed label, and measured code identity that maps in the transport's
compiled policy to `urn:remote-session-runner:release-authority:mac-control-plane`.
The local transport derives that principal after the kernel peer check; it never
accepts a caller-supplied URI or identity field. For an Ubuntu `:9443` operation,
the same term means the validated mTLS client certificate/URI. Every ordinary
record and time-probe rule below uses this single term: it does not imply mTLS on
the Mac socket or allow a local peer to bypass its equivalent identity check.

For a later PKI rotation, after creating a one-use issuance mandate the authority
agent may invoke fixed `POST /v1/pki-certificate-request/<mandate_id>` on that
canonical host. It supplies no CSR, SAN, path, certificate, or key bytes. The
transport validates the authenticated authority-agent peer and current
ledger/time state, then hands only that opaque ID to its measured PKI installer;
the installer derives its fixed local role/endpoint, generates its local key and
CSR, and publishes the signed request to the outbox. The issuer matches the ID
and every derived tuple to root authority state before it issues anything. This
is the sole non-secret mandate handoff; no generic file or shell channel exists.

The first `:9443` contact never bootstraps its own trust. The separately
approved privileged bootstrap preflight creates or installs a dedicated
release-transport CA, root-owned CA trust bundles, and a root-owned CA private
key outside Git and every lane root. Only the Mac
`runner-release-transport-pki-issuer` has normal-process access to that CA
private key. It has a measured code identity and accepts only one host-signed
`pki_certificate_request` retrieved through the fixed outbox after the root
approval tool created the matching one-use `pki_issuance_mandate`. It verifies
host ID, leaf role, URI/IP SANs, DER CSR/public key, CA profile,
validity/overlap, and mandate expiry, plus either the current management
generation or the one-time exact bootstrap-contract/install-generation,
installer-artifact tuple, and bootstrap host-attestation key, before consuming the request and journaling a
non-secret package receipt. It cannot accept a
caller-selected CSR, subject, path, or host. A separate normal exact signer
mandate is required before that receipt can become a final signed
host-management manifest/authorization. During initial bootstrap, before a
`:9443` listener has a certificate, the approved preflight uses a one-time
root-console handoff instead of normal transport. Before the first CSR, it
verifies the installer against the exact bootstrap contract and recorded
source/toolchain/input/binary tuple, with the host's local root-protected
bootstrap build attestation. The installed helper then signs the public-only CSR
request; a root verifier carries only that envelope and its host-key/fingerprint
readback to the Mac issuer, and it returns only the non-secret certificate/trust
package plus receipt to the same verifier. The preflight pins canonical host,
role, URI/IP SANs, CSR digest, issuer fingerprint, and package digest at both
ends; it never carries a private key, source, lane
artifact, shell command, or reusable credential. For this scoped preflight only,
the approved bootstrap tuple replaces ordinary installed-manifest, ledger, and
transport-time validation; the root verifier checks that tuple and the pinned
bootstrap key instead. Once that host passes its first applicable local-socket
or mTLS/time-probe health gate, its console exception is permanently disabled
and later issuance uses the fixed route above.

Each Mac or Ubuntu `runner-release-transport-pki-installer` is likewise a
distinct measured root service. It generates the local leaf key and DER CSR
without exporting the private key, and causes exactly one host-signed
`pki_certificate_request` to be published through the fixed outbox only when the
security-anchor helper invokes its constrained attestation operation, and after
bootstrap accepts only the issuer's non-secret certificate/trust package embedded
in the matching final signed host-management authorization. During initial
bootstrap it accepts only the exact root-console preflight receipt/package whose
bootstrap-contract, generation, and attested installer artifact tuple match its
signed request; it records that evidence before first start. It revalidates the
manifest/authorization/request/receipt tuple (or
that bootstrap tuple) through the anchor and atomically installs the named
trust/leaf files before requesting the transport reload. The Mac client leaf/private key is readable
only by `runner-release-authority-agent`. The installer sets each Ubuntu server
leaf/private-key file to mode `0600` owned only by that host's dedicated
non-login `runner-release-transport` service UID; no Runner or `ubuntu` process
can read it. A generic root peer is not admitted to the issuer or installer IPC;
physical-root access is the separately audited incident boundary. The bootstrap
records CA and leaf fingerprints, validates exact URI and literal-IP SANs against
the generated Ubuntu certificates, and installs only the required firewall/NAT
route before the first listener starts. The Mac has local-socket peer
authorization rather than a public listener or server leaf. Immediately after
each Ubuntu listener starts, a normal mTLS handshake and chain/identity
validation must succeed to its fixed URL before the baseline is accepted. This
material is distinct from direct-control mTLS; a missing, mismatched, expired,
or unreachable bootstrap route is a stop rather than a fallback to SSH or a lane
transport.

A verified host-management manifest carries the release-transport
`active_peer_set`: per endpoint, the exact active client and server certificate
fingerprints/SPKIs, URI/IP binding, and a bounded old/new overlap set. The Mac
agent and every listener enforce this set after normal chain and URI validation.
The release ledger records trust decisions but does not itself make a TLS stack
reject a still-chain-valid leaf. Therefore a leaf removed from `active_peer_set`
is rejected immediately, even if its CA chain remains otherwise valid.

Release-transport PKI rotation is a root-only host-management operation, never
a lane deployment action. An exact operator mandate and signed
`host_management_manifest`/authorization bind the old and new CA/trust/leaf
fingerprints, active-peer sets, affected host matrix, overlap window, issuer and
installer artifact tuples, and rollback artifact. It first creates the one-use
PKI issuance mandate; the issuer then journals the host-request-derived package
receipt; only a reviewed normal exact signer mandate may bind that receipt in the
final manifest/authorization. The constrained installers then add dual trust and
the bounded old/new active-peer set while preserving the working material. They then install new leaf keys and reload one transport at a time,
proving a normal fixed-URL mTLS handshake and fresh time probe to every host
using the new chain. Only after all hosts and the Mac agent accept the new chain
may a final manifest remove old fingerprints. Every endpoint must then prove
that the retired old leaf is rejected before its material is removed and its
retirement/revocation is recorded in the ledger. A failed overlap retains the
old working chain and its active-peer set and uses the host-management
update-or-verified-rollback guard; it never falls back to SSH, a lane credential,
or trust-on-first-use.

The closed API has no path-to-filesystem mapping. Authenticated Mac authority
may fetch `/v1/outbox-index` and `/v1/outbox/<record_id>` for that host's
canonical `host_build_attestation`, `promotion_attestation`,
`ingress_admission_attestation`, `capacity_measurement_attestation`,
`pki_certificate_request`, and `host_management_attestation` records. It may PUT the exact canonical record to `/v1/records/<record_id>` only for
`candidate_manifest`, `promotion_authorization`, `lane_acceptance`,
`ingress_admission_directive`, `capacity_policy`,
`host_management_manifest`, `host_management_authorization`,
`host_attestation_key_set`, `release_revocation_ledger`, or
`authority_key_set` addressed to that host. It alone may also invoke the single-use
`POST /v1/time-probe/challenge` and matching
`PUT /v1/time-probe/<challenge_id>` exchange, fixed
`POST /v1/capacity-measurement` with no request body, and fixed
`POST /v1/pki-certificate-request/<mandate_id>` with no body or key material. The security-anchor helper,
not the transport caller, owns the nonce and both timing observations; it also
owns every measured capacity value and the signed outbox result. Ordinary
record fetch/cache/PUT validates the authenticated authority-agent peer, URL
record ID, canonical body, schema, signature/key chain, ledger, time proof,
host/lane/configuration scope, and anchor state before accepting it. The two
time-probe endpoints are the narrow exception needed to establish that proof:
`POST` requires only the authenticated authority-agent peer and its fixed
canonical-host binding, then asks the helper to create the challenge; `PUT`
requires that same authenticated authority-agent peer, the helper-correlated
unconsumed challenge, its exact nonce,
the current anchored authority key, and the probe signature/timing checks. It
does not require a pre-existing fresh time proof or ledger. If the ledger is
stale, a successful probe permits only the contiguous ledger/key-set recovery
path in section 7; it cannot admit any other record or mutation. The transport
rejects source, binary, artifact, command, request, arbitrary JSON, arbitrary
path, and any other record type. It does not expose SQLite, lane roots, or an
execution endpoint.

A received record is handed to the helper's transport-only verify/cache
operation, which alone writes
`<release-cache>/incoming/<record_id>.json.tmp`, fsyncs it with its directory,
validates it, and atomically renames it into the verified record cache. An
existing identical record is an idempotent success; the same ID with different
bytes is an integrity failure. The helper likewise writes outbound host
attestations after it independently verifies the sealed build/install evidence.
The authority polls only the fixed outbox API and verifies the host signature;
hosts need no route to write the Mac registry.

The first transport and security-anchor helper on each host are a deliberately
small bootstrap exception. After separately approved privileged preflight,
source delivery builds the exact bootstrap artifacts locally, verifies their
recorded digests and pinned trust anchors, and installs no lane service or
mailbox change. Once present, the old verified transport can receive the signed
`host_management_authorization` for its own next host-management artifact and
the root installer can atomically replace it. All subsequent release records use this transport and
all later artifacts still build locally from the mirrored source SHA; the
transport never copies source or artifacts between hosts.

### Artifact cache and sequential promotion

Each physical host builds only its required matrix artifacts from the exact
clean mirrored `dev` SHA in root-created sealed staging. The unprivileged
builder may write only that staging output. After the helper reopens the exact
output by descriptor/no-follow traversal, hashes and attests it, the helper
alone copies it into the root-owned, content-addressed **verified** release
cache:

```text
# Mac
/Library/Application Support/RemoteSessionRunner-release-cache/

# each Ubuntu host
/var/lib/remote-session-runner-release-cache/
```

The cache root is root-owned `0700`; artifacts and canonical records are
root-owned regular files addressed by their digest. No `tomasz.walczuk`,
`ubuntu`, Runner, submitted script, or unprivileged builder can create, replace,
or write below it. The root helper is the only cache writer; a transport submits
bytes only through its constrained verify/cache endpoint. The authority agent
accesses Mac records only through its protected transport/API, not the
filesystem.

Each host also has a separately budgeted, root-owned `0700` immutable cache
backup store on a distinct local recovery volume:

```text
# Mac
/Library/Application Support/RemoteSessionRunner-release-cache-backup/

# each Ubuntu host
/var/lib/remote-session-runner-release-cache-backup/
```

After accepting a verified artifact, only the helper writes its digest-addressed
backup copy plus the verified manifest/attestation references. The backup never
travels between hosts, is not a Runner continuity bundle, and does not grant an
untrusted caller cache-write access. To rehydrate an empty replacement cache,
the helper opens backup bytes by descriptor/no-follow, hashes them, revalidates
the complete recorded candidate/host-build chain and host/platform tuple, then
atomically writes them into a new root-owned cache and records recovery
evidence. If both the verified cache and its same-host backup are missing or
mismatched, that candidate is unavailable on that host: installation, rollback,
and recovery stay closed. It may only be replaced by a fresh complete candidate
build and acceptance chain; no checkout rebuild reconstructs an old candidate.
L3 and L7 test both verified rehydration and this closed-stop outcome. Before a
candidate exists, each host signs one
`host_build_attestation` for every local matrix tuple with its pinned host key.
The Mac root authority state verifies those exact tuple records and creates a
candidate manifest only after all required attestations verify and match the
matrix. The release-publish operation transfers only the signed canonical
manifest and relevant signed authorization/directive through the authenticated
deployment channel; it never copies source into an Ubuntu checkout. The helper
validates every incoming canonical byte, ID, signature, schema, revocation, and
time proof before its atomic verified-cache write and before activation.

A lane installer copies the exact verified cache bytes into the selected lane
root. It never rebuilds a candidate while it moves from Dev to QAS or PRD on
that host. A binary digest is comparable only for the same component, host,
platform, and source commit; Darwin, Linux AMD64, and Linux ARM64 artifacts
are intentionally different entries.

Promotion is linear and complete-set based:

```text
clean authoritative dev SHA
        │ primary Gitea push + exact GitHub mirror verification
        ▼
all host/component artifacts + immutable candidate manifest
        │
        ▼
Dev authorizations → complete Dev installation attestations → signed Dev lane acceptance
        ▼
QAS authorizations → complete QAS installation attestations → signed QAS lane acceptance
        ▼
PRD authorizations → complete PRD installation attestations → signed PRD lane acceptance
```

A Dev authorization has no predecessor acceptance. Every QAS authorization
names the one complete authority-signed Dev `lane_acceptance`; every PRD
authorization names the one complete authority-signed QAS `lane_acceptance`.
Each acceptance itself names the complete accepted host/component installation
attestation set and live evidence. A partial host/component result or a passing
install without the live suite cannot advance a lane. A failed Dev or QAS
candidate never changes PRD; correction is a new `dev` commit and candidate
manifest. The installer rejects a branch name, unrecorded binary,
missing/incorrect authorization or predecessor acceptance, source mismatch,
altered artifact digest, missing matrix tuple, or artifact copied from another
host or platform. After startup it records the observed lane, source revision,
artifact digest, configuration digest, and installation generation.

Every V3 installation also has a durable **lane deployment transition**. For
Dev or QAS, the lane registry first creates one immutable canonical lowercase
UUIDv7 `lane_transition_id` naming the target candidate manifest, lane,
complete host matrix, exact target resolved configuration digest per host, and
the capacity-policy ID for each host. An active V3 replacement additionally
names the incumbent candidate manifest and resolved configuration digest for
each host; a closure directive must exactly match those recorded incumbent
identities, while a candidate directive must exactly match the target
identities. A fresh launch has no incumbent. Only then may the authority sign the host-specific promotion
authorizations bound to that transition. The registry atomically appends the
resulting authorization IDs before any host stages artifacts; each later
attestation and directive binds the same ID. A fresh Dev/QAS launch has only a
`candidate_admission` epoch: its `closed` directives name the target candidate
and each component starts admission-closed. After the full remote cohort has
healthy closed attestations, it issues `transport_ready`, then atomically
records `target_opening` before the first `open` directive in that candidate
epoch. Only after every remote `open` attestation does the Mac control-plane
ingress open. Any failed partial fanout from `target_opening` enters
`target_closure_pending` and cannot become failed-closed until it has fresh
complete closure attestations. On the successful Mac ingress attestation the
record becomes `reopened → acceptance_pending`; it runs the full live suite,
then records the signed transition-bound lane acceptance, seals its evidence,
and compare-and-swaps `accepted → archived`. A later Dev/QAS V3 replacement
first uses an `active_runtime_closure` epoch for its incumbent and then a
strictly higher `candidate_admission` epoch for the replacement, under the
Dev/QAS state graph in section 3. This Dev/QAS lifecycle has no V2 fence or
old-binary rollback: a failed installation remains closed and is removed
through the normal installer rollback. PRD uses the richer migration transition
in section 8, but all V3 lanes use the same directive, epoch, expiry, and
applied-state rules.

## 8. PRD migration and rollback

Dev and QAS are fresh roots. Existing PRD mailboxes, databases, service labels,
and external producer paths are not moved. PRD migration occurs only after QAS
acceptance and a scheduled maintenance window.

### Cohort transition state

The lane registry holds one owner-only, compare-and-swap **PRD operation
group** keyed by `prd`. It serializes every `v2_migration`, `v3_maintenance`,
`v3_restore`, and `v3_forward_repair` transition that can touch the unsuffixed
roots, listeners, mailboxes, or directives. A group is acquired before any PRD
transition ID,
authorization, or directive is allocated. While held, every other PRD
transition allocation is rejected. A `restore_required` maintenance parent may
allocate at most one **active** linked recovery child: `v3_restore` when its
prior candidate lineage remains valid, or `v3_forward_repair` when it does not.
No sibling maintenance, restore, or repair is permitted. If a recovery child
fails before its commit, it must persist `aborted` with its evidence and archive
while remaining admission-closed; only then may the parent allocate one explicit
higher-epoch replacement child. The group is released only when a normal
primary transition has archived; when a pre-commit `v2_migration` has restored
V2, sealed its fence-release evidence, and archived `aborted`; when a
post-commit repair state has sealed its all-host closure, quiescence, and repair
handoff and archived; or when the final linked recovery child has completed and archived
and its parent has atomically become `aborted` and archived. The group record
names the primary transition, recovery-child history and active child, current
owner state, and all affected host high-water tuples, so a restart cannot infer
that a new PRD operation is safe.

One active durable `v2_migration` PRD cohort-transition record lives in the
lane registry; completed transitions are retained immutably as history. The
following graph applies only to that initial V2→V3 migration. Before the
authority signs any PRD authorization, it allocates the immutable canonical
lowercase UUIDv7 `lane_transition_id`; the authorizations, later attestations,
and every directive bind that same ID. The new transition initially names the
exact target candidate manifest, all four canonical host IDs, exact target PRD
configuration digests, and capacity-policy IDs. It has no incumbent V3 identity
because V2 has no V3 directive consumer. It receives the short-lived PRD authorization
set only at the end of `staged`, immediately before candidate installation; the
registry atomically records those exact authorization IDs before fanout. A
renewal uses a new authorization record for the same transition only while it
is still pre-`committed`; it never changes candidate, configuration, capacity
policy, or installation generation. The transition records a compare-and-swap
state plus per-host progress; no host may independently reopen PRD intake. Its
only states are:

```text
prepare → fencing → fenced → staged → candidate_closed → committed → reopen_prepared → reopen_authorized → reopened → acceptance_pending → accepted → archived
                                               ↘
                                                aborted → archived

committed | reopen_prepared | reopen_authorized | reopened | acceptance_pending ──(irrecoverable V3 fault)──> post_commit_repair_required → repair_closure_pending → repair_quiescing → repair_closed → archived
```

Later `v3_maintenance`, `v3_restore`, and `v3_forward_repair` transitions
use the separate state machines defined in the later-V3 section. They never add
a state to this initial migration graph.

`fencing` is intentionally durable **before** the first V2 client pause,
service stop, disable, or listener check. It records the initial per-host V2
service-manager, PID, socket, listener, work, inbox, and ACK-tree observation.
Each subsequent pause, stop/disable, and proof updates that record durably.
On a controller or host restart while the state is `fencing`, a V2 member
already recorded stopped/disabled stays that way and no member is implicitly
re-enabled. A member still recorded active is stopped only by resumed fencing
or is handled by the explicit pre-commit abort procedure; recovery never
infers that the whole cohort should resume. This closes the otherwise ambiguous
crash gap between disabling the first V2 member and persisting an empty fence
snapshot.

`committed` is an atomic registry transition only after every V3 PRD member is
installed, healthy, lane-attested, and admission-closed. It is the V2 rollback
cutoff: after it, an operational failure is repaired or restored only as V3.
`reopen_prepared`, `reopen_authorized`, and `reopened` are forward-only V3
states. The transition record, its host progress, and every directive named
below are part of the PRD continuity bundle. Any irrecoverable V3 fault after
`committed` and before archival follows the common V3-only
`post_commit_repair_required` handoff defined below; it never re-enables V2.

### Reversible mailbox migration fence

The required fence is a new **reversible migration fence**, not the existing
retirement fence. It must never create `.runner-mailbox-retired`, freeze
retirement permissions, or use a retirement tombstone. For every PRD mailbox
root it persists a registry fence record and a regular owner-only
`.runner-mailbox-migration-fence` marker. They carry the same `lane_transition_id`,
root and filesystem identity, configuration digest, state, and the digest of a
marker-last snapshot of both request and ACK ready-pair trees. A mismatch,
incomplete pair, replaced descriptor, or missing side stops the transition.
They survive a restart and make V3 admission remain closed until reconciled.

A fence-aware native or workspace publisher holds the migration shared lock
from `CheckMigrationFence` through the complete durable publication of either:

- request JSON, marker-last request ready file, and parent-directory fsync; or
- ACK JSON, marker-last ACK ready file, and parent-directory fsync.

The transition takes the same lock exclusively while it writes its fence
intent/marker and captures the request-and-ACK snapshot. This prevents a
check-then-publish race. The snapshot and later deferred ledger identify a pair
by kind (`request` or `ack`), inbox, logical ID, descriptor-derived relative
JSON/ready paths, descriptor identities, file digests, and observation
generation. On restart the controller reopens those descriptors without
following replacements and validates the recorded identity and digest before
it changes state. It never reconstructs a pair from a filename alone.

A raw file writer has no safe migration guarantee and must be stopped for L6.
Configured publishers must declare a tested migration-fence capability and
version. Native clients gain the versioned request-and-ACK fence check; direct
workspace publishers use the supported fence-aware wrapper/API. L6 cannot
leave `prepare` until every configured publisher is proven capable or stopped.
Fixtures cover a crash before/after the request or ACK JSON, ready file, and
directory fsync, as well as the exclusive-lock race with a publisher.

In `prepare`, validate every configured mailbox publisher and direct client,
its supported pause control, and the current work inventory, but do not alter
V2 state. The controller then persists `fencing` **before** it pauses the first
publisher/client or disables any V2 member. In `fencing`, it pauses the
configured publishers and direct clients, then drains every mailbox exchange,
active session, active/queued command, and resumable job to a supported
terminal zero-work state on every host. An uncontrolled writer/client,
uncertain record, or remaining work is a stop condition.

Current V2 importers do not understand the new marker. Therefore `fenced` is
not durable until every V2 direct listener, Mac local ingress, mailbox importer,
and queued-bridge backend has been stopped **and disabled at its service
manager**, no PID/socket/listener remains, and zero work is proved again. Only
then, under the exclusive lock, does the controller persist the per-root marker
and empty request/ACK snapshot and advance to `fenced`. If a nonempty pair
appears, it returns to the appropriate durable `fencing` progress step for a
supported terminal disposition; it never silently treats the snapshot as empty.

After `fenced`, a dedicated V3/offline migration scanner records complete pairs
that appear in their original inbox as deferred work and leaves both files in
place. The scanner does not execute, ACK, delete, or manually replay them.
The ledger supports the same outcome after a crash because it retains the
original pair identity and digest. No V2 direct or queued create/submit can
cross this durable boundary: V2 ingress is disabled, V3 is closed, and an
uncontrolled raw writer was a pre-fence stop condition.

### Signed ingress admission

All V3 mutation ingress is governed by the signed
`ingress_admission_directive` defined in section 7 and by a separate
host-applied durable admission-state row at:

```text
<release-cache>/transitions/<lane_transition_id>/ingress-admission/<directive_role>-<transition_epoch>.json
```

An epoch represents exactly one runtime identity, rather than an entire logical
transition. The lane registry allocates it per `{release_lane, host_id}`
monotonically and never resets it across cohorts. A `directive_role` makes that
identity explicit:

- `active_runtime_closure` is a fresh epoch strictly above the anchor high-water
  tuple. It names the currently active candidate and resolved configuration,
  permits only `admission_generation: 0` (`closed`), and must have a complete
  aggregate closed attestation before that runtime can be quiesced.
- `candidate_admission` is a fresh epoch strictly higher than every preceding
  closure or candidate epoch observed for that host. It names the target
  candidate and resolved configuration. It starts at generation `0` (`closed`),
  then may advance to `1` (`transport_ready`) and `2` (`open`). A fresh V3
  launch, V2→V3 migration, restore, or forward-repair has only this role; a
  V3-to-V3 replacement has both roles in that order.

Every directive also has a nonnegative `lease_generation` that starts at `0`
for that phase and increases by exactly one for each phase-preserving renewal.
A renewal keeps the same `lane_transition_id`, `directive_role`,
`candidate_manifest_id`, lane, host, configuration identity, and admission
state while it supplies a new signed validity interval. A host keeps the
highest accepted `(transition_epoch, admission_generation, lease_generation)`
tuple. It accepts an exact idempotent copy, rejects an older tuple, and rejects
a different record with the same tuple. This permits a long closed or
transport-ready barrier to renew safely, while preventing a directive from an
older cohort becoming valid merely because a local counter was reset.

If a directive in any phase expires, is revoked, loses its required fresh
ledger, or cannot be renewed before its validity end, the host atomically
records `closed_due_to_lease_expiry` under the admission lock and rejects new
mutation. It retains the verified high-water tuple for replay protection. It
can resume the same phase only after it verifies the next valid lease generation
for that phase, or move under a newer epoch. L1/L3 fixtures cover closed,
transport-ready, and open renewal; skipped/stale renewal; expiry during restart;
expiry before the remote cohort completes; and a concurrent mutation at lease
expiry.

The release authority alone signs the directive. Each host verifies its record,
revocation ledger, time validity, lane, host ID, `candidate_manifest_id`/
configuration digests, role, and epoch before atomically applying it to its
local state. The running runtime may apply a directive only when the role and
identity exactly match its active candidate/configuration. The root installer
may cache and register a `candidate_admission` **closed** directive as a
pending-candidate context only after its exact promotion authorization and
target configuration have been verified; it cannot make that context active,
or accept transport-ready/open, until that candidate has been installed. It
switches the service admission pointer from the already closed incumbent to the
pending candidate only while both contexts are closed. An incumbent never
applies a directive for a different candidate, and a closure directive can
never be reused for a replacement.

The root-protected attestation helper then emits one aggregate
`ingress_admission_attestation` only after independently checking every required
surface under that directive: on an Ubuntu host, `direct_https` and
`queued_ssh_bridge`; on the Mac, `local_api`, `local_intent`,
`mailbox_request_importer`, and `mailbox_ack_importer`. The V3 configuration
contains the sorted exact inventory for that `{host, lane}`, and the attestation
names the same inventory and a safe evidence digest for each surface. A host may
attest `transport_ready` only for that exact candidate-admission directive and
complete surface inventory; an `open` attestation requires the matching `open`
directive and inventory. A stale, revoked, cross-host, cross-lane, replayed,
incomplete, identity-switched, or surface-mismatched directive/attestation
fails closed.

Direct HTTPS create/submit, queued forced-wrapper/private-API mutation, the Mac
local API/local-intent dispatcher, and the Mac mailbox importer all read the
verified applied state under the same lane admission lock and SQLite transaction
that would create a session, command, intent, job, ACK-consumption record, or
other mutable resource. The importer checks it before every startup scan and
before it accepts either a request or ACK ready pair. In `closed` or
`transport_ready`, direct, queued, and local mutation returns the safe
`migration_fenced` maintenance result; the importer leaves a pair untouched and
adds or revalidates its deferred-ledger entry. Read-only status exposes only
safe lane, `candidate_manifest_id`, `lane_transition_id`, and admission information. A restart starts
admission-closed unless it revalidates the directive and durable state row.

### Cutover, abort, and reopening

1. **Prepare.** Validate the QAS-accepted candidate and the prerequisites to
   issue a fresh PRD authorization set, Dev/QAS health,
   `legacy_prd_reserved` mailbox and bridge records, source and artifact
   attestations, capacity-policy freshness, the candidate's exact required
   healthy host-management manifest, maintenance window, client pause controls,
   and work inventory. The preallocated transition has no stale PRD
   authorization set yet. Do not alter V2 state.
2. **Fencing and fenced.** Persist `fencing` before the first client pause;
   then pause clients, drain to proven zero work, and durably record every
   stop/disable and absence proof. Finally persist the per-root migration
   markers, empty paired snapshot, and the target candidate identity/configuration
   intent, but no V3 directive record: V2 has no V3 directive consumer and no
   PRD authorization exists yet. A crash at any point remains in `fencing` or
   `fenced` and follows only the recorded recovery/abort path.
3. **Staged.** Take and prove an isolated restore of the PRD-only continuity
   bundle. It includes the binary; configuration; SQLite database with
   WAL/SHM; mailbox enrollment, tombstones, markers, snapshots, and deferred
   ledger; directives, applied state, and attestations; bridge artifacts;
   principal maps; credentials; and PRD registry records. It never rolls back
   concurrent Dev/QAS state. Only after that proof and immediately before
   installation, the authority signs fresh short-lived PRD authorizations bound
   to the preallocated transition and the current capacity policies; the registry
   appends their IDs atomically before transport fanout. It then allocates and
   distributes the target `candidate_admission` closed directive for each host;
   only that authorized candidate may cache it as a pending context.
4. **Candidate closed.** Install the exact PRD artifacts and migrate the
   unsuffixed configuration to V3 `release_lane: prd` without changing its
   root, service labels, public port, mailbox roots, or durable state paths.
   Atomically convert matching legacy mailbox/bridge reservations to active PRD
   records. Start all cohort members admission-closed and require health, lane,
   source/artifact/configuration, mTLS, bridge, capacity, and recovery
   attestations. No mutation is admitted.
5. **Committed.** After every candidate member is healthy and closed, atomically
   write `committed` and the final V2 rollback cutoff. The old V2 bundle remains
   preserved as evidence but is never enabled again. A later fault remains
   V3-only.
6. **Reopen prepared and authorized.** Each V3 member reconciles its fence,
   paired snapshot, deferred ledger, directive high-water state, and recovery
   health while closed, then records `reopen_prepared`. When the full set is
   present, the registry compare-and-swaps from `committed` to
   `reopen_prepared`, naming those attestation IDs, then compare-and-swaps to
   `reopen_authorized`, naming the exact signed
   `transport_ready` directive record ID for every remote host. Every remote
   server and bridge applies that immutable directive and appends its
   applied-state attestation to the same durable transition record. Only after
   the full remote-ready set exists does the authority issue the signed `open`
   directives for that same target candidate-admission epoch; their record IDs are atomically
   added to the still-`reopen_authorized` transition record before fanout.
7. **Reopened.** Every remote member applies and attests its named `open`
   directive. Only after the complete remote open-attestation set exists does
   the Mac apply its named `open` directive to its own ingress and permit
   configured direct clients to submit. The registry then compare-and-swaps to
   `reopened`, naming every final directive and attestation ID. It next records
   `acceptance_pending`, runs the full PRD live suite and zero-work postflight,
   obtains the transition-bound signed PRD `lane_acceptance`, seals that evidence,
   and only then compare-and-swaps `accepted → archived`. A restart or a partial
   fanout stays `reopen_authorized`: it retransmits only those named
   directives, reuses idempotent attestations, or records a higher `lease_generation` before it can resume. It never invents a new transition or falls
   back to V2. The selected V3 importer then handles deferred work through its
   normal durable recovery rules.

An abort is allowed only before `committed`. Candidate services remain
admission-closed and V2 services remain disabled while a fence-aware V3/offline
verifier reconciles every marker, paired snapshot, deferred ledger entry, and
bridge/registry record. A deferred pair does not make abort fail; it must remain
intact and descriptor-valid. The controller then takes the exclusive ingress
lock, disables candidate services, revokes or lets expire every transition
record, removes only verified migration markers/directives/applied-state records,
restores the old PRD continuity bundle and legacy records, atomically marks
`aborted`, and only then enables and starts V2.

This is a distinct fenced **offline V2 restoration**, not a normal V3
continuity-bundle restore: it preserves the root security anchor exactly as it
stands and never restores, deletes, or merges the anchor from the V2 bundle.
V2 has no V3 directive consumer, so its legacy runtime can resume only after
the verified fence release. After V2 is restored, the fence-release evidence is
sealed, and `aborted` archives, the PRD operation group may release; only then
may a later retry allocate a new transition and a strictly higher directive
epoch than the preserved anchor high-water. It cannot reuse an aborted
authorization or directive. Preserved post-fence pairs become ordinary V2 work
after reopening; they are never deleted, ACKed, or manually replayed.

After `committed`, a V3 forward repair or restore is executed as a new
scheduled `v3_maintenance` primary transition in a newly acquired operation
group after the completed transition archives; it never treats the linked-child
`v3_restore` or `v3_forward_repair` forms as parentless operations. A rollback
or repair never discards work accepted after its snapshot. The full abort path
and every crash boundary
(before/after client pause, each V2 stop/disable/proof, marker/snapshot write,
V3 stage/start, commit, each directive, and each applied-state attestation)
must be proven first against an isolated copy. The fresh harmless PRD mailbox
exchange occurs only after `reopened`, so its durable result is preserved by
the V3 repair path rather than erased by a V2 rollback. Once its final evidence is sealed, only `accepted` after the signed
transition-bound PRD `lane_acceptance`, or pre-commit `aborted`, moves to
`archived`; it can never be reused for another candidate.

### Later V3-to-V3 PRD promotion

A `v3_maintenance` transition has this independent durable state graph:

```text
prepare → closure_pending → closed_verified → quiescing → quiesced → staged → candidate_closed → committed → reopen_prepared → reopen_authorized → reopened → acceptance_pending → accepted → archived
                                                                  ↘
                                                                   restore_required ──(referenced recovery child completes)──> aborted → archived

committed | reopen_prepared | reopen_authorized | reopened | acceptance_pending ──(irrecoverable V3 fault)──> post_commit_repair_required → repair_closure_pending → repair_quiescing → repair_closed → archived
```

`closure_pending` durably records the incumbent candidate/configuration,
per-host directive fanout and aggregate-attestation progress, and each
closure-lease renewal. Before the complete incumbent closure set exists, it may
retry or renew only that closure operation under the held PRD group. It cannot
quiesce, stage the target, claim global closure, enter `restore_required`, or
allocate a recovery child. A broken route or expired/failed closure remains a
safe stop in this state until repaired and re-attested; it is not permission to
assume that the unconfirmed ingress is closed.

For every host, the transition durably records two identity-bound directive
contexts when it replaces a running V3 PRD candidate: an
`active_runtime_closure` epoch/directive/aggregate-attestation set for the
incumbent, and a strictly higher `candidate_admission`
epoch/directive/aggregate-attestation set for the target. `closed_verified` is
legal only after the complete incumbent closure set proves every required
surface closed. `candidate_closed` is legal only after the target's pending
candidate-admission closed context has been activated and attested. The record
retains both contexts, their epoch high-water tuples, and the atomic pointer
switch; later `transport_ready` and `open` directives belong only to the target
candidate-admission context.

After `closed_verified`, the transition enters durable `quiescing`. It obtains
a supported terminal disposition for every running, queued, resumable, and
uncertain item, retains deferred request/ACK evidence, and retries only this
reconciliation after a crash. It reaches `quiesced` only with a complete
zero-work postflight. It cannot stage a target, enter `restore_required`, or
allocate a recovery child while quiescing.

`restore_required` is permitted only after `quiesced` and before
`committed`. Its compare-and-swap
record contains the failed state, failure evidence digest, full per-host
progress, all issued directive/attestation IDs, preserved continuity-bundle
identity, last accepted PRD candidate and acceptance IDs, and each host's
anchor high-water tuple. It remains admission-closed and has no transition back
to `open`, `reopened`, or `staged`. Under the PRD operation-group lock, its
only legal child is the one named recovery child: `v3_restore` when the prior
lineage verifies, or `v3_forward_repair` after explicit authority approval when
it does not. Its only legal parent successor is `aborted`, and that
compare-and-swap is valid only after the named child has reached `accepted` with
its complete evidence or sealed its own post-commit repair handoff. In the
latter case the child and parent archive together and no replacement child is
permitted; a later scheduled primary handles the sealed V3 context. The
operation group remains held until both records archive.

`post_commit_repair_required` is the only terminal repair handoff for any V3
PRD transition type (`v2_migration` after its V3 commit,
`v3_maintenance`, `v3_restore`, or `v3_forward_repair`) after `committed` and
before archival. It records the failed state and evidence, all
directive/attestation IDs, current V3 candidate/configuration and
continuity-bundle identity, last accepted candidate/acceptance lineage, parent
transition when present, and every anchor high-water tuple. It remains V3 and
cannot restore V2 or allocate a linked recovery child. It first enters durable
`repair_closure_pending`, which records fresh higher
`active_runtime_closure` fanout for the currently installed candidate/
configuration and its per-host progress. It does **not** claim global admission
closure in that state. Only after complete aggregate closed attestations exist
on every host, including any endpoint that had already opened during partial
fanout, may it enter `repair_quiescing`. That durable state obtains a supported
terminal disposition for all running, queued, resumable, and uncertain work,
retains deferred request/ACK evidence, and retries after a crash until a
complete zero-work postflight exists. Only then may it enter `repair_closed`,
seal a repair-handoff record naming the closure and quiescence evidence and the
next required QAS-accepted candidate criteria, and archive. For a recovery child, the failed child and its
`restore_required` parent atomically become `aborted` and archive after the
handoff; otherwise the primary archives. Only then may the operation group
release; a later scheduled `v3_maintenance` primary acquires a new group and
starts from that sealed, closed V3 context.

A `v3_restore` transition is separately durable:

```text
prepare → closed_verified → staged → candidate_closed → committed → reopen_prepared → reopen_authorized → reopened → acceptance_pending → accepted → archived
               ↘ any pre-committed failure → aborted → archived

committed | reopen_prepared | reopen_authorized | reopened | acceptance_pending ──(irrecoverable V3 fault)──> post_commit_repair_required → repair_closure_pending → repair_quiescing → repair_closed → archived
```

It records the failed maintenance transition ID, the prior accepted candidate
and acceptance lineage, fresh policy/authorization/directive IDs, one target
`candidate_admission` epoch per host strictly above every parent/anchor
high-water tuple, per-host progress, high-water tuples, and the sealed parent
`quiesced` evidence. Its `reopen_authorized` → `reopened`
compare-and-swap requires the complete open directive/attestation set. It then
records `acceptance_pending`, runs the complete restore live-acceptance suite,
and may enter `accepted → archived` only with a **new** authority-signed PRD
`lane_acceptance` naming this restore transition and its complete restore
promotion-attestation set. The old PRD acceptance is predecessor provenance
only; it cannot attest the restored transition. A failure before its own
`committed` remains closed while it
persists failure evidence, then transitions to `aborted` and archives; it cannot
reopen the superseded runtime or lower its directive epoch. The parent remains
`restore_required` and may allocate only a fresh, explicitly authorized,
higher-epoch replacement child after that archive.

A `v3_forward_repair` is the only alternative recovery child. It uses that same
durable graph and requires the sealed parent `quiesced` evidence, but receives a
new UUIDv7 `lane_transition_id` and one target
`candidate_admission` epoch per host strictly higher than both the failed
transition and every host anchor high-water tuple. It names a newly selected
QAS-accepted candidate rather than the invalid prior candidate, its exact
required healthy host-management manifest, plus fresh matching policies,
authorizations, closed/transport-ready/open directives, aggregate attestations,
and a new PRD `lane_acceptance`. It remains under the
parent operation group, starts and validates closed before reopening, and cannot
reuse the failed transition's candidate, authorization, directive, or
acceptance. Its pre-commit failure uses the same closed `aborted`/archive path
before any replacement recovery child is allowed.

A later QAS-accepted candidate uses a new `v3_maintenance` PRD transition and
a new UUIDv7 `lane_transition_id`; it never reuses the V2 migration state or an
archived transition. During a scheduled window it verifies the current V3
provenance, a fresh matching `lane_runtime` capacity policy, and the exact
healthy host-management manifest ID required by that candidate. It first issues one fresh
`active_runtime_closure` epoch per host, strictly above that host's anchor
high-water tuple, for the current V3 candidate/configuration. Only after every
complete aggregate closure attestation exists may it enter `quiescing` and
obtain supported terminal dispositions for running, queued, resumable, and
uncertain V3 work, retain deferred request/ACK evidence, and preserve a tested
current-V3 continuity bundle. It reaches `quiesced` only with a complete
zero-work postflight. Immediately before installation it appends fresh
short-lived PRD authorizations to that transition,
then registers a pending target context and issues one strictly higher
`candidate_admission` closed epoch per host for the QAS-accepted candidate.
It stages the exact candidate under the existing unsuffixed roots, atomically
switches the closed admission pointer, validates all cohort attestations,
commits, and follows the target candidate's transport-ready/open/reopened
sequence. It then records `acceptance_pending`, runs the full live suite, and
may enter `accepted → archived` only with a fresh transition-bound PRD
`lane_acceptance`. The runtime remains V3 throughout.

A pre-`committed` maintenance failure after `quiesced` never directly starts
the prior bundle. A closure failure before `closed_verified` stays in
`closure_pending` and follows only its closure retry/renewal path; a failure
while `quiescing` remains there until its supported terminal reconciliation and
zero-work postflight complete. The failed transition records `restore_required`,
remains admission-closed, and
retains its complete per-host progress, both directive contexts, attestations,
and the last complete V3 bundle as evidence. The authority then allocates a
separate `v3_restore` transition with a new UUIDv7 and one target
`candidate_admission` epoch per host strictly higher than the failed transition
and every affected host's anchor high-water tuple. It names the prior accepted
PRD candidate, its still-valid acceptance lineage, the failed transition ID,
fresh matching capacity-policy IDs, and the exact healthy host-management
manifest ID required by that prior candidate. It must issue fresh short-lived promotion authorizations for that
prior candidate, then fresh `closed`, `transport_ready`, and `open` directives
and collect their exact aggregate ingress attestations. The restore uses the
verified bundle only as its installation/continuity input; it still reinstalls
through the candidate installer, starts closed, validates the full cohort,
reaches a restore commit, and follows the same remote-first sequence. It enters
`reopened` only after the exact open attestations, then remains in
`acceptance_pending` while the restored route, mailbox, cross-lane, resource,
and zero-work live suite runs. Only a fresh PRD `lane_acceptance` for the
restore transition permits `accepted → archived`.

The failed maintenance transition becomes `aborted` and archives only after the
separate restore transition has committed and accepted, or after that child has
sealed its post-commit repair handoff. A crash or partial fanout before a child
commits stays closed and resumes only the durable failed or restore state; after
a child commits it follows `post_commit_repair_required`. It cannot reuse a
lower-epoch open directive, invent a transition, or revive V2. A restore is
eligible only when the prior candidate
and acceptance lineage and required host-management manifest remain valid and
fresh matching current `lane_runtime` policies can be issued; expired or
superseded original policies are historical evidence only. If that fresh
eligibility cannot be obtained, the parent remains closed in `restore_required`
and the authority may allocate its one linked `v3_forward_repair` child with a
newly QAS-accepted candidate. After a
maintenance commit, use V3 forward repair or a later complete V3 restore only
as a new scheduled `v3_maintenance` primary after the completed transition
archives. L7 exercises the maintenance failure, higher-epoch
restore/forward-repair, and partial-fanout/crash fixtures.

## 9. Acceptance contract

Every lane requires source, static, installed, and live evidence for the Mac
and all three remote profiles. Evidence records the source SHA, Gitea SHA,
GitHub SHA, artifact/configuration digests, runtime lane attestation, and
rollback state.

The live proof requires all of the following:

1. Mac and every host health/status endpoint identifies the expected lane,
   source revision, artifact digest, configuration digest, and schema version.
2. A fresh same-lane native mailbox exchange proves marker-last publication,
   terminal response, complete event prefix, exact ACK consumption, and a
   zero-work postflight on each configured route.
3. A fresh positive queued route and a separate fresh positive direct-mTLS
   route succeed for every `{lane, remote profile}` pair.
4. Cross-lane denial expands only as lanes exist. Dev acceptance proves both
   directed configured-controller pairs Dev→legacy-PRD and legacy-PRD→Dev on
   every remote profile; QAS acceptance then proves all six directed pairs
   Dev→QAS, Dev→legacy-PRD, QAS→Dev, QAS→legacy-PRD, legacy-PRD→Dev, and
   legacy-PRD→QAS; PRD acceptance repeats those six pairs with V3 PRD. Every
   rejection occurs before resource creation. The equivalent queued
   key/wrapper matrix proves each source key is pinned to only its own wrapper
   and socket and cannot create, read, cancel, or close a resource in every
   foreign lane that exists in that phase. These are configured-controller
   tests, not a claim that an arbitrary same-user script cannot bypass
   operating-system permissions.
5. Negative configuration/installer fixtures reject cross-lane roots, ports,
   profiles, controller identities, mailbox ownership, certificates, bridge
   sockets, artifact digests, and service labels.
6. Dev/QAS refreshes leave PRD process IDs, sockets, listener, database,
   mailbox trees, credentials, measured Linux PRD floor/headroom and baseline
   controls, Mac storage layout/headroom, revision, and workload unchanged. Mac
   compute contention is the
   explicitly recorded first-rollout non-guarantee in section 6. PRD promotion
   proves the exact Dev → QAS → PRD candidate-manifest ID and immutable
   promotion-attestation chain.
7. The complete lane-runtime and host-management matrices have verified
   host-build attestations, their exact required healthy host-management
   manifests, correctly scoped capacity
   policies, valid key/ledger/time evidence, and complete cross-platform record
   test vectors. Expiry, revocation, clock-probe replay/delay, key successor,
   stale ledger, and an open-lease renewal/expiry all fail closed.
8. The PRD migration fixture proves crash/restart recovery before and after
   every durable `fencing`, paired request/ACK snapshot, deferred-ledger,
   candidate-closed, commit, directive-fanout, and reopen state change. It
   proves a pre-commit abort preserves deferred pairs and a post-commit failure
   remains V3-only. The later-maintenance fixture proves incumbent closure uses
   an exact current-runtime closed epoch and the replacement uses a strictly
   higher candidate-admission epoch; it rejects a same-epoch identity/configuration
   switch. It proves a partial incumbent-closure fanout remains durably
   `closure_pending` and cannot quiesce, stage, or allocate a recovery child.
   It proves a failed pre-commit transition after full closure uses one linked
   recovery child: a separate higher-epoch restore, or when its predecessor is
   ineligible, an explicitly authorized forward repair. Both require fresh
   authorizations/directives/attestations and a fresh transition-bound PRD
   `lane_acceptance`; neither reopens an old bundle with stale directives. It
   also proves a post-commit partial fanout remains
   `repair_closure_pending` until a fresh all-host closure reaches
   `repair_closed`, then `post_commit_repair_required` archives and a new
   scheduled primary begins.
9. A staged resource fixture fills each Dev/QAS cgroup/quota/cache/log budget
   and the separate host-management build/transport scope, exercises log
   rotation and failure, and proves that Linux PRD measured headroom/floor and Mac
   documented volume/headroom/snapshot bounds remain usable. It records the
   shared-Mac compute limitation rather than treating a priority hint as a
   reservation.

Event-driven mailbox wake-up remains separate work. It may be considered only
after the lanes are live and measured ingress latency requires it; it remains a
per-lane best-effort hint with periodic reconciliation, never the delivery
path.
