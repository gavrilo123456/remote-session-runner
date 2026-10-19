# Detailed design: Factory-style versioned Runner releases

**Status:** selected design, 2026-10-19. CAT-01 source, fixture and delivery
gates have passed under the owner's implementation assignment. CAT-02 selection
work is next; live migration remains gated by the phased plan and approvals.

## 1. Authority, scope and current compatibility

This is the target design for the separate
[CAT implementation sequence](015-factory-style-runner-releases-phased-implementation-plan.md).
It refines release storage/selection in the
[existing-hosts profile](../040-dev-qas-prd-three-machine-lanes/025-existing-hosts-three-lane-scope.md).
It does not reopen completed SIMPLE phases, activate the superseded advanced
LANES rollout, or block independent BUG-025 source work. Existing supported
installation commands and layouts remain valid until their selected CAT
migration passes. Documentation does not change a running installation.

Reuse Factory's release principles: sealed versioned packages, one independent
selector per environment, unchanged per-environment state, identical bytes
through Dev → QAS → PRD, and a bounded hold/backup/switch/reconcile/verify flow.
Runner adds a four-host cohort: `mac-control-plane`, `linux-host`, `sandbox-host`
and `factory-sandbox`. Use their existing accounts, disks, ports and services.
No new release server, OS account, root authority, cloud resource or filesystem
is required. A catalog on one machine is not a shared network filesystem.

Current Dev/QAS code stores bundles at `<lane-root>/releases/<bundle-sha256>`
and accepts only the existing relative `current` target. Current PRD maintenance
replaces regular files in its unsuffixed `bin` directory and rejects aliases.
The catalog requires explicit installer, launcher, bridge, backup and rollback
changes. Moving directories or adding symlinks under current guards is not a
supported migration.

## 2. Fixed filesystem layout

| Host | Catalog root |
| --- | --- |
| Mac, `tomasz.walczuk` | `/Users/tomasz.walczuk/projects-runtime/runner-releases` |
| Each Ubuntu worker, `ubuntu` | `/home/ubuntu/projects-runtime/runner-releases` |

Each host has this layout; version labels below are examples, not existing files:

```text
~/projects-runtime/runner-releases/
  v1.2.0/
    release.json
    <this-host-id>/
      manifest.json
      bin/
        <this-host's Runner components>
```

`release.json` is the same sealed cohort metadata on every host. Its local
host directory contains only that machine's native bundle; foreign-host binary
directories are absent. A single Mac binary is never deployed to Ubuntu, and
one worker's identity is not substituted for another's. Native builds use the
existing source-delivery process and shared Go caches; no source-file copying
or per-release cache duplication is introduced.

All writable lane state remains under the existing namespaces:

- Mac: `~/Library/Application Support/RemoteSessionRunner[-dev|-qas]`.
- Ubuntu: `~/.local/share/remote-session-runner[-dev|-qas]`.

Those roots keep configuration, DB/WAL/SHM, sockets, mailbox roots, credentials,
workspaces, logs, locks, staging, backups and deployment receipts. Existing
external mailboxes are neither moved nor repointed. Service labels, HTTPS
8443/8444/8445 and the port-22 SSH listener remain unchanged.

## 3. Release identity and publication

A release ID is an immutable human-readable label, for example `v1.2.0`.
Validate it against `[a-z0-9][a-z0-9._-]{0,63}` and reject `dev`, `qas`, `prd`,
`latest`, `current`, traversal, separators and case aliases. A label can never
be rebound to different release metadata or executable bytes.

The sealed `release.json` format has a version, release ID, exact common source
commit, pinned toolchain, and exactly four host entries. Each entry binds host
ID, GOOS/GOARCH, existing bundle digest and component-manifest identity. Include
the supported database read/write schema ranges and declared migration/rollback
compatibility. Actual current-data compatibility still requires tests; a manifest
declaration alone is insufficient. No secret, mutable environment configuration,
future approval or future closeout hash belongs in the sealed release.

Record SHA-256 of the exact sealed `release.json` bytes as the release digest.
Qualification and promotion identify **release ID + release digest + host bundle
digest**. The source and component hashes are verified against the existing
per-host bundle format; metadata cannot make different binaries equivalent.
Changing source, dependencies, binaries or compatibility inputs creates a new
release ID/digest and requires renewed affected qualification.

Build each host's bundle once from the same clean synchronized source revision.
Collect only the non-secret per-host manifest/digest metadata to seal the cohort;
each host keeps its own native binaries. Publication copies that host's verified
bundle into private staging, verifies all hashes, seals the release contents,
and atomically publishes without replacing an existing version. Exact repeat
publication is idempotent; incomplete, foreign, changed or tampered contents
fail closed. Interrupted staging is never a selectable release.

Published release directories and executable files should be owner-read/execute
only, with metadata owner-readable; staging and catalog publication are private
to the existing operator. Runtime/worker workflows receive no catalog mutation
capability. The same OS user can still change its own permissions, so this is
tooling/hash/operating-policy integrity, not hostile-code isolation. Do not add
an OS account or root authority to imply a stronger guarantee.

## 4. Independent lane selectors and path validation

Each existing lane root owns its own `current` selector. Its new supported
target is the canonical absolute path:

```text
<catalog-root>/<release-id>/<this-host-id>
```

Dev, QAS and PRD can select three different releases. They can also select the
same immutable host bundle without sharing writable state. There is no catalog
global `current`, mutable `latest`, shared lane configuration or request-selected
release/path. Existing `release_lane` and authenticated host/profile bindings
remain authoritative for work.

The trusted selector resolver validates fixed host/account/catalog ancestry,
ownership, safe modes, regular manifests/components, exact release and bundle
digests, local platform, and schema/compatibility constraints. Permit only the
single intentional selector link; refuse additional symlinks, alias roots,
hard-linked mutable imports, foreign hosts, path escape and changed manifests.
Revalidate selected identities at the effect boundary, not merely at preview.

Switch under the existing selected-lane deployment lock, with expected previous
selector identity, a durable operation receipt, atomic compare-and-replace and
directory durability. Coordinate publication/selection with catalog pruning
under a fixed lock order: catalog lock, then selected-lane deployment lock.
Acquire additional lane locks only in canonical `prd`, `dev`, `qas` order when
a separately scoped operation actually requires them. No operation can switch
a peer lane or execute from mutable source/staging output.

Generated LaunchAgents, Linux entrypoints, CLI paths and fixed SSH wrappers must
resolve the same selected host/lane release. Preserve component-specific args,
private sockets, credentials and stdout protocol boundaries. PRD's legacy `bin`
checks must be deliberately adapted; do not weaken them to accept arbitrary
symlinks. The ordinary Dev/QAS installer still cannot silently select PRD.
PRD retains its separate guarded maintenance entrypoints.

## 5. Promotion and selected-lane upgrade

1. Publish the sealed release cohort without activating it. Record its source,
   release digest and each host's bundle digest outside secret material.
2. Install/select on Dev across all four hosts and run the bounded route,
   lifecycle, output, timeout, capacity, isolation and fault acceptance.
3. Select the identical release/digests in QAS and repeat acceptance. A changed
   candidate returns to Dev; no promotion-time rebuild or dependency resolution.
4. Prepare the exact PRD destination, release, configuration identity, work
   disposition, backup, migration and recovery package. Use explicit scoped
   maintenance approval and fresh guards before changing unsuffixed PRD.
5. Apply the cohort hold and switch rule below. For each selected host/lane,
   verify a consistent full selected-lane backup, stop only its components,
   switch the selector and required launch definitions, start held, and perform
   supported startup migration/ownership reconciliation and readiness checks.
   Resume external work only after the complete cohort passes its postflight.

### Four-host hold, order and compatibility

The first catalog rollout uses a **whole selected-lane hold**, not rolling
per-host resumption. Identify the Mac and all three workers in one protected
operator cohort receipt, binding the lane, operation ID, old identities, target
release/cohort digest, per-host bundle/configuration identities and backup/
recovery references. Keep matching local receipts in the affected lane roots;
this is operator evidence, not another service or execution-authority database.

First close new Mac lane submissions/queued dispatch and all selected worker
direct admissions through supported controls; verify affected producers are
paused/fenced. Settle or safely fence owned work and reconcile effects across
all four hosts before the first switch. Hold the selected lane throughout the
operation. Stop/switch/start workers in fixed order `linux-host`, `sandbox-host`,
`factory-sandbox`, then the Mac component group. Leave other lanes running.

Intermediate old/new host mixtures are maintenance states, not accepted service
cohorts. Use host-local authorized maintenance for inventory if old/new protocol
readers cannot interoperate; do not dispatch normal work through that mixture.
The final cohort must pass direct API, fixed queued-bridge and Mac-local
compatibility checks with all four target identities. Any later proposal for
rolling availability needs explicit old/new controller/worker and bridge wire-
compatibility qualification in both directions before changing this default.

Keep external publications fenced while running separately authorized bounded
functional probes through a supported, qualified maintenance admission scope.
Such admission must identify only the operator's fresh probes, preserve normal
authentication and refuse ordinary producers. If that scope or a required probe
is unavailable, record NOT RUN and keep the lane held; do not bypass closure or
pretend a readiness GET is a functional test. Respect any no-mailbox instruction.
Open normal admissions only after all four identities, routes, required probes,
capacity and peer-preservation checks pass and cohort completion is recorded.

Failure or interruption after any host switches keeps the whole selected lane
held. Reconcile each actual service/selector/authority and receipt before retry.
Use a qualified forward completion or compatible rollback for every already
switched host; the old cohort must also be fully verified before resuming it.
No automatic half-cohort resume, old-DB restore or replay is permitted.

Keep unknown work/ownership held; timeout is not stop proof. Never replay bodies,
release capacity manually, ACK historical records, restart peer services or
pause unrelated projects for convenience. Future mailbox acceptance must respect
the owner's actual publication authorization; while the no-mailbox instruction
stands, record that gate NOT RUN rather than inventing acceptance.

Every operation receipt records tool/host/lane, previous and target identities,
configuration identity, applicable checks/approval/backup, stage and outcome.
Interrupted operations reconcile recorded and actual selector/service/authority
state before another effect. These are protected operator records, not a new
Runner execution entity or extra central management service.

## 6. Existing installations and rollback

Catalog migration is separate from moving data or changing database semantics.
No DB migration is required solely to relocate executable packages. A candidate
with an unrelated schema change still follows its own qualified migration and
rollback rules; the catalog move cannot authorize that change implicitly.

Inventory and preserve the exact existing binaries, manifests, selector targets,
service definitions, fixed bridge wrappers and current authority/configuration.
Dev/QAS legacy bundles may be imported by copy-and-verify when the full release
identity can be established. Never hard-link to mutable old installation files
or invent an absent source/cohort identity. An unqualified legacy installation
remains protected rollback material in its old layout; it is not relabelled as a
qualified catalog release.

Migrate Dev first, QAS second and PRD only through its separately approved guarded
procedure. Keep the former layout and service definitions until selected-lane
rollback and postflight have passed. Do not fall back to rebuilding from a newer
checkout, rewriting an active config or manually linking a running PRD binary.

Align future release compatibility with Factory: initially allow ordinary
promotion only for backward-compatible migrations. QAS must prove previous code
can read/use the current schema and records/configuration written after upgrade.
Binary rollback repeats hold/reconcile/stop/switch/start checks against current
authority. Never automatically restore an old DB merely to start old code.
Incompatible changes require a separately selected maintenance/forward-fix or
protected restore procedure. Existing legacy snapshot-recovery tooling remains
available within its exact guards, not as an automatic catalog rollback shortcut.

## 7. Release retention and failure handling

The first catalog implementation has **no automatic release deletion**. Status
and bounded read-only retirement preview may explain references and eligibility.
Keep every release needed by any lane selector, compatible predecessor, backup,
active reader, incomplete deployment or recovery receipt. Unknown references,
missing evidence and unreadable peer selectors prevent retirement.

Any later explicit retirement needs catalog-wide coordination, a fresh complete
reference inventory and revalidation before removal. Never apply one lane's age
policy to shared release bytes. This is distinct from DB/event/log retention;
coordinate with the [retention proposal](../050-retention-policy-improvements/005-initial-idea.md)
without making its full implementation a prerequisite. No shared Go cache,
rollback backup, retained work or old layout is purged to make space for a move.

Insufficient existing disk space, failed route/authentication, unresolved work
or a changed manifest blocks its dependent live action. It does not authorize
new infrastructure or stop unrelated hermetic source/design work. Failures keep
the affected lane held for bounded recovery; they do not silently activate a
different version, foreign lane, old DB or source-checkout executable.

## 8. Implementation coverage and acceptance

Adapt the existing bundle installer/resolver, simple plan/service generation,
queued SSH wrapper, PRD preflight/backup/install/rollback and legacy deployment
scripts together. A legacy script must either honor the selected catalog or
explicitly refuse to overwrite a catalog-managed installation. No entrypoint
may silently undo selection by writing an old `bin` path.

Tests must cover sealed/cohort identities, changed-label/overwrite refusal,
foreign host/platform, path/alias/permission and TOCTOU failures, concurrent
publication and lane switches, interrupted publication/switch/recovery,
legacy adoption and rollback, new-data schema and old/new transport compatibility,
failure after each host switch with a lane-wide hold, and prevention of
retirement while another lane/operation needs a release. Use temporary real
filesystem/process fixtures and bounded deterministic synchronization.

Dev and QAS live acceptance cover the Mac and all three workers, direct mTLS,
fixed queued bridges, configured Mac-local execution, timeouts/output/teardown,
lane isolation and unchanged peer configuration/state. Production migration,
normal-load observation and source tests are separate evidence claims.
The [CAT plan](015-factory-style-runner-releases-phased-implementation-plan.md)
defines completion order. Source implementation is active; live operations
remain individually gated and CAT-06 PRD migration requires separate approval.
