# PROMO-08 first Dev/QAS rollout — controller correction checkpoint

Date: 2026-10-24 (Europe/Warsaw)

## Frozen service release

- Release: `rr-20261024-43a4e675`
- Digest: `093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`
- Source: `43a4e675ccccb89e1ea1c333e975ea028c9600fa`
- Sealed host bundle hashes are recorded in `PROMO-BUILD-43A4.md`; this
  controller-only correction does not change those bundle bytes.

## Initial read-only plan and diagnosis

The first `promote-to dev all --release rr-20261024-43a4e675 --plan --json`
stopped before mutation with `linux-host dev admission receipt inspection:
release workflow record is invalid or corrupted`.

Read-only `catalog-cohort --host HOST --lane dev --action discover` succeeded
on Mac, Linux, Sandbox, and Factory. All four returned the same held,
completed prior Dev cohort receipt, operation
`c02b2c01b3504c067de9d3b77605b87e`, release
`cat-20261023-c01e633`, at receipt stage `complete`. No selector, service,
receipt, mailbox, database, or work inventory was changed by this discovery.

The failure was a controller schema mismatch. `catalog-cohort discover`
returns `host_id`, `lane`, `admission_held`, an optional `operation_id`, and
the held `receipt`. `readAdmission` used strict JSON decoding into a struct
containing only the last two hold fields, so a valid response was rejected as
corrupt. The correction decodes the full response and verifies host, lane,
hold/operation consistency, and receipt identity before accepting it.

## Correction checks

- Added regression coverage for unheld and held discovery records, required
  receipt identity, host/lane mismatch, and inconsistent hold/receipt pairs.
- Mac Go 1.27.1 `go test ./src/internal/releaseflow -count=1`: PASS.
- Mac Go 1.27.1 `go vet ./src/internal/releaseflow ./src/cmd/promote-to ./src/cmd/runner-release`: PASS.
- Mac Go 1.27.1 `go build -o /private/tmp/promote-to-controller ./src/cmd/promote-to`: PASS.
- `git diff --check`: PASS.

The discovery-envelope correction is committed and delivered to all three
workers as `bec5b3b44a5a73658236c72f8f2045a09a939b95`. Live promotion remains
NOT RUN. The prior completed Dev receipt is still holding admissions and must
be handled only by supported workflow controls after the corrected read-only
plan exposes its state.

## PRD peer-baseline correction

After the discovery decoder correction, the read-only Dev plan advanced and
then failed at `linux-host peer lane prd isolation baseline` because
`readLaneSnapshot` called the legacy `prd-preflight` command. That path
deliberately rejects catalog-managed PRD with `legacy PRD installer cannot
modify a catalog-managed service`.

The controller now obtains PRD peer state through the read-only
`catalog-work-inventory --lane prd` path. That path validates the canonical
PRD namespace/configuration, reads the selected catalog identity and schema,
uses the PRD-only status probe, and opens the database with the existing
read-only restart-preflight connection. It does not use the legacy installer,
change configuration, stop PRD, or migrate the database. Dev/QAS mutation
guards remain unchanged.

A local Mac invocation of this read-only command succeeded. It observed the
unsuffixed PRD service active, schema 40, selected release
`cat-20261023-c01e633`, and two active command slots/reservations. This is
retained production work; PRD remains outside this deployment and untouched.

The first focused source run passed `laneinstall` and `releaseflow` but exposed
a test setup mistake: the CLI's current-host guard correctly rejects a
non-local host on Mac. The test was corrected to use the local canonical host.
The rerun passed:

- Mac Go 1.27.1 `go test ./src/internal/laneinstall ./src/cmd/runner-lane ./src/internal/releaseflow -count=1`: PASS.
- Mac Go 1.27.1 `go vet ./src/internal/laneinstall ./src/cmd/runner-lane ./src/internal/releaseflow ./src/cmd/promote-to ./src/cmd/runner-release`: PASS.
- Mac Go 1.27.1 builds of `src/cmd/runner-lane` and `src/cmd/promote-to` to
  `/private/tmp`: PASS.
- `git diff --check`: PASS.

The PRD peer-baseline correction is committed and synchronized as
`1aee2ead07b5e423f5965f159cd5caef22f18064`. The actual Dev plan has not yet
passed; QAS promotion, mailbox probes, and Dev/QAS acceptance remain NOT RUN.

## Cross-host test correction

After delivery at `1aee2ead07b5e423f5965f159cd5caef22f18064`, all three Ubuntu
workers correctly exposed that one new lane-scope test incorrectly used the
host-local `currentCatalogHost` guard as a platform-independent lane predicate.
The worker package test failed on Linux, Sandbox, and Factory for that reason;
the other changed packages passed in those runs. The implementation's
host-local guard remains unchanged. The test now exercises the separate
canonical-lane predicate, while invalid host/lane scopes continue to be
rejected before host state is read. Mac changed-package tests passed after the
test correction. A corrected cross-host rerun is required after its Git
delivery; no lane state was changed.

## Completed-hold replacement correction

The first valid Dev plan exposed the existing completed
`c02b2c01b3504c067de9d3b77605b87e` receipt as an admission hold. The initial
promotion CLI rejected every non-empty hold before the workflow could inspect
its history, so no new operation could be created. After removing that blanket
CLI rejection, fresh preparation also needed to distinguish the exact
validated predecessor hold from an unrelated or conflicting hold.

The controller now permits one common predecessor operation only when all
four hosts report it and its receipt copies are identical, `complete`,
eligible for supersession, selected on every host, and still match the fresh
plan's bundle, configuration, schema and empty-work evidence. It persists the
predecessor ID in the durable promotion operation. Preparation revalidates the
old receipt before backups, allows only that exact hold while collecting fresh
preflights, holds the successor on all four hosts, and then uses the existing
`catalog-cohort supersede-host` / `supersede` operations. It synchronizes each
recorded transition; partial or divergent histories refuse to continue. The
old hold therefore remains effective until the successor hold is recognized
cohort-wide. The sealed Runner release and its four bundle hashes are
unchanged.

Regressions cover public `promote-to` acceptance of one validated completed
hold, persistence of its predecessor identity, refusal of partial/mixed or
unverified holds before effects, the four-host native receipt synchronization
path, interruption after a local supersession write and safe retry, and
admission lookup during the completed-predecessor/successor-held overlap.
Live Dev/QAS rollout is not claimed until the controller source is delivered
and the run finishes its functional gates.

Mac source gates for this controller patch (working tree based on delivered
`3ef2b9df256e6c30e280addc1a6418851ade6e5c`):

- `go test ./... -count=1`: PASS with Go 1.27.1. The first sandboxed run
  failed only because existing `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`
  could not invoke `/bin/ps` for its own test-owned child (`operation not
  permitted`). The isolated package and the full uncached suite were rerun
  with the required process-inspection permission and passed.
- `make vet`: PASS.
- `make build`: PASS.
- `make smoke`: PASS.
- `git diff --check`: PASS.

These are controller/source gates only. They do not qualify or accept the
sealed service release; all four worker fast-forwards and native controller
checks remain pending until delivery.

## Receipt-sync and selected-service ordering correction

The first resume of Dev operation `dea80fa695a606e64985f0b2b53ebeb4`
reconciled the held operation, selected `rr-20261024-43a4e675` on
`linux-host`, and then failed while syncing the receipt because the selected
Dev service was active. The failure was truthful and preserved: the prior
durable operation history still records the failed Linux switch/sync attempt.
The corrected controller does not rewrite that history or the sealed service
release.

The cause was an ordering defect in native promotion: it attempted cohort
receipt synchronization after selector activation had started the lane. The
host-side sync guard correctly requires the selected lane stopped and empty.
The correction stops only the selected lane, and only after verifying the
operation hold and empty, identity-matching inventories across all four hosts.
It then synchronizes the receipt before proceeding. Cohort acceptance starts
services only under the exact hold; final release starts the lane only after
the released receipt has synchronized and verifies all four hosts. Refusal
tests confirm that a host with retained work is never stopped.

Regression coverage in `f31de51548e4007165261a48592bcce1ce0bad4a` exercises
active-empty-lane stop before sync, retained-work refusal, and selector
transition/sync before service start. The sealed release remains
`rr-20261024-43a4e675`, digest
`093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`; no
service bundle was rebuilt.

Source/controller validation for `f31de51548e4007165261a48592bcce1ce0bad4a`:

- Mac Go 1.27.1 `make test`, `make vet`, `make build`, `make smoke`, and
  `git diff --check`: PASS.
- Linux (`linux-host`, Go 1.27.1 linux/amd64): focused
  `releaseflow` receipt-sync/switch/resume regressions, package vet, `make
  build`, and `TMPDIR=/tmp make smoke`: PASS.
- Factory (`factory-sandbox`, Go 1.27.1 linux/amd64): the same focused
  regressions, package vet, `make build`, and `TMPDIR=/tmp make smoke`: PASS.
- Sandbox (`sandbox-host`, Go 1.27.1 linux/arm64): the same focused
  regressions, package vet, `make build`, and `TMPDIR=/tmp make smoke`: PASS.
- Before the Sandbox gates, `/dev/sda1` had 502,272,000 bytes available. The
  gates completed successfully; no task-owned smoke directory remained. The
  shared Go cache was preserved.
- Source commit `f31de51548e4007165261a48592bcce1ce0bad4a` was pushed to
  primary Gitea, verified at the identical GitHub mirror SHA, and fast-forward
  pulled to all three clean Ubuntu `dev` checkouts before their gates.

## Live retry blocked before new effects

The corrected controller binary was built on the Mac from the synchronized
`f31de51548e4007165261a48592bcce1ce0bad4a` source. Its retry of operation
`dea80fa695a606e64985f0b2b53ebeb4` stopped on Sandbox capacity before any new
selected-lane hold, stop, backup, or selector change:

```text
sandbox-host measured free space 491794432 is below the read-only minimum 500000000
```

A subsequent read-only measurement on Sandbox `/dev/sda1` was 491,270,144
bytes available (100% reported utilization), 8,729,856 bytes below the
500,000,000-byte minimum. `/tmp` is on the same filesystem. The shared Go
build cache (3.8 GiB), module cache (348 MiB), release catalog (846 MiB), and
release-operation records (41 MiB) were inspected and preserved; no
task-owned smoke temporary directory was present. No cleanup was performed.

At this checkpoint the operation remains `blocked`, with its prior hold and
receipt history preserved. The earlier Linux selector transition remains to
be reconciled by the supported resume. The latest failed preflight made no
additional lane changes. Dev functional acceptance is NOT RUN; QAS promotion
and acceptance are NOT RUN; filesystem-mailbox acceptance is NOT RUN. QAS,
PRD, and all project-mailbox ownership remain unchanged. Resume this same
operation only after a fresh capacity check passes and the controller's
per-backup capacity guards pass; do not create a successor or rebuild the
sealed release.

## Interrupted active-switch recovery correction

The owner later raised Sandbox capacity. A fresh check before worker source
gates showed 1,195,696,128 bytes available on `/dev/sda1`; after those gates,
1,186,000,896 bytes remained. These exceed the 500,000,000-byte operating
buffer. No cleanup was performed.

Resuming the existing Dev operation `dea80fa695a606e64985f0b2b53ebeb4` with
the `f31de51` controller passed capacity and then recorded a new failed
`observe-host` event (sequence 12): Linux's candidate selector was already
active, while the supported receipt-sync path requires the selected lane to
be stopped. This failure is preserved in the operation record. No new
selector, receipt, backup, mailbox, or peer-lane changes were made by that
failed observation.

Controller revision `d6f0a54b0a5ed708de567303493b2c82916d19d7` adds a narrow
recovery path: an active service may be stopped and its receipt synchronized
only when the exact candidate release/bundle is selected, the selector journal
is complete, the matching switch transition is present, and the operation
still owns the admission hold. The existing all-four-host inventory,
configuration, schema, and work checks run before the stop; all hosts must be
empty and identity-matched. After synchronization, the host is observed again.
An active service with any host retaining work, a missing switch transition,
an incomplete journal, a different selector, or a different hold remains
blocked without a stop or receipt write.

Regressions exercise the actual native observation and receipt-sync path for
an interrupted Linux switch, including synchronization to all four fixture
receipts, and verify refusal before mutation for retained work or missing
switch evidence. The sealed service release `rr-20261024-43a4e675` and its
four bundle hashes are unchanged.

Source validation and delivery for `d6f0a54`:

- Mac Go 1.27.1 `make test`, `make vet`, `make build`, `make smoke`, and
  `git diff --check`: PASS. The standalone smoke/diff rerun also exited 0.
- Linux (`linux-host`, Go 1.27.1 linux/amd64), Sandbox (`sandbox-host`, Go
  1.27.1 linux/arm64), and Factory (`factory-sandbox`, Go 1.27.1 linux/amd64):
  `go test ./src/internal/releaseflow -count=1`, vet of `releaseflow`,
  `promote-to`, and `runner-release`, `make build`, and `TMPDIR=/tmp make
  smoke`: PASS on each host.
- The Mac `promote-to` and `runner-release` controller binaries were built
  from `d6f0a54`; their SHA-256 values are respectively
  `e6507b188ecad163d2ad7071b3d028ddf9bf81f1cbee06a2b54576e849141124` and
  `922130d89e9ec317325d1fe7c89100bec8b657edc6353da9e20735cdd912de81`.
- Commit `d6f0a54b0a5ed708de567303493b2c82916d19d7` was pushed to primary
  Gitea; the automatic GitHub mirror matched exactly; all three clean Ubuntu
  `dev` checkouts fast-forwarded to the same commit before their gates.
- This is controller/source validation only. The supported resume, Dev
  functional acceptance, QAS promotion/acceptance, and filesystem-mailbox
  acceptance remain NOT RUN at this checkpoint. PRD and its project mailboxes
  remain outside scope.

## Acceptance ordering defect exposed by the first live Dev run

The owner-provided space increase was rechecked on Sandbox before continuing.
The latest read-only `df -Pk /` returned 992,544 KiB available on `/dev/sda1`
(1,016,365,056 bytes), above the Dev 500,000,000-byte operating-buffer gate.
No cleanup was performed.

The existing Dev operation `dea80fa695a606e64985f0b2b53ebeb4` resumed under
its synchronized four-hour continuation `825dd659a1116657bd5a9b334ccbb9e7`
(renewal digest `e0fe18d3de17b5b71e8684114868497018cf6b2ba35d49f2fa51d8b7e233af65`,
valid through `2026-10-24T23:30:55.733951Z`). The supported verifier passed on
Linux, Sandbox, Factory and Mac. Linux remained selected and verified; the
controller then completed Sandbox and Factory, and Mac. The durable operation
record contains host-verified events for all four exact bundle hashes:

- Linux `f3628574b1ffd50e0e0f11790039f8b404701de475a34770b7b64b63d9f56c37`
- Sandbox `3da6a23a44ef96bbd8681bc7f8504e6ec114fa9d5049336240b80085e543f0d9`
- Factory `206a1b7e010aadf64139f81b26077716be7a5090de4c4e19e318c975f3309e29`
- Mac `a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062`

At `2026-10-24T19:48:19.577496Z`, whole-cohort acceptance stopped before the
first direct-mTLS command. `catalog-probe-prepare` returned
`catalog maintenance probe admission refused`. Read-only inspection of the
cohort receipt on Linux and Mac showed `stage=switching` with ten transitions;
the probe guard correctly requires the synchronized receipt to be
`verifying`. The controller had deferred `verify-start` and the ordered
per-host receipt verification/synchronization to `ReleaseCohort`, which is
called only after acceptance. This is a controller ordering defect, not a
service-bundle or TLS finding. The operation is `blocked`; it was not marked
accepted, and QAS/PRD remain outside the operation.

Correction: move the supported `verify-start` and ordered per-host
verification/synchronization before functional probe preparation. The
controller retains all admission, empty-work, exact-selector, backup,
configuration/schema and capacity checks; retries recognize an already
`verifying` receipt and do not repeat completed transitions. The regression
drives the public cohort command sequence, verifies all four synchronized
receipts and transition order, and retries without duplicating verification.

Mac Go 1.27.1 gates for this controller correction: `make test`, `make vet`,
`make build`, `make smoke`, and `git diff --check` PASS. Commit
`82ad19e1f3b9e45dfe08b17f41ba03abb88c06a1` was pushed to primary Gitea; the
automatic GitHub `dev` mirror matched exactly. Linux, Sandbox and Factory were
clean on `dev` at `f6e444c` before pulling and each fast-forwarded to
`82ad19e`. At that revision each worker passed
`go test ./src/internal/releaseflow -count=1`, vet of
`./src/internal/releaseflow ./src/cmd/promote-to ./src/cmd/runner-release`,
`make build`, `TMPDIR=/tmp make smoke`, and `git diff --check`.

The corrected Mac controller tools built from `82ad19e` have SHA-256
`0c235af1a288b2bc8c7999451201fc37de31a36365687dd1d87c619bf6aad235`
(`promote-to`) and
`3dd407e38e374d952f730cd2991207c04b6122306fc432a7ab63e6afabf16622`
(`runner-release`). No service bundle was rebuilt. The sealed service
candidate `rr-20261024-43a4e675` and all four bundle hashes remain unchanged.

After source validation, read-only checks on all four Dev instances confirmed
the exact operation hold `dea80fa695a606e64985f0b2b53ebeb4`, the same
`switching` receipt with ten transitions, the candidate selector and bundle,
schema 40, unchanged configuration hashes, active services and empty work
inventories. All active-command, active-session, queued, running, resumable and
recovery counts were zero. The exact Linux direct-mTLS probe scope file was
absent. Sandbox had 859,456 KiB free before worker validation; no cleanup was
performed. The operation remains blocked pending its corrected acceptance
retry. Dev functional acceptance is NOT RUN; QAS promotion/acceptance and
filesystem-mailbox acceptance are NOT RUN. No QAS or PRD configuration,
service, selector, database, or mailbox state was changed.

## Probe result decoding and accepted-scope recovery

The next synchronized-controller retry first read the Linux direct-mTLS
probe's terminal status successfully. The accepted scope is bound to job
`job-705974bc8f474d77898670837be15b61`; the helper returned exit 0. The
promotion controller then rejected its JSON because its strict outcome type
did not include the helper's `accepted_at`, `http_status`, or
`readiness_http_status` fields. This was a controller decoding failure after
one accepted probe, not a Runner command failure. No second request was
submitted. The Dev operation remains held and blocked; QAS and PRD are
unchanged.

The controller correction adds the complete public outcome schema and a
read-only scope recovery path. Before preparing a capability, the controller
asks the selected host to inspect its existing owner-only canonical scope.
The host validates its file ownership/mode, exact host/lane/operation,
transport, authenticated controller, configuration digest, environment,
idempotency key and request digest. It returns no token. An accepted scope is
observed through a bounded status-only GET using the configured mTLS endpoint
(or the Mac Unix socket); the request is never replayed. An active unused
capability or an ambiguous reserved scope fails closed, while only an expired,
unused ready scope can be prepared again. No scope or Runner bundle is deleted
or rewritten by observation.

Regressions cover complete strict decoding of the actual helper result,
unknown-field rejection, exact accepted-scope observation without token
disclosure, configuration/job-identity validation, absent-scope read-only
behavior, and refusal to replace active or replay ambiguous/consumed scopes.

Mac Go 1.27.1 source gates for this correction:

- `make test`: PASS (full `go test ./...`).
- `make vet`: PASS.
- `make build`: PASS.
- `make smoke`: PASS.
- `git diff --check`: PASS.

The sealed release `rr-20261024-43a4e675`, digest
`093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`, and its
four per-host bundle hashes above remain unchanged. The correction is
promotion/probe-controller tooling, not a service-bundle change. Ubuntu
fast-forward delivery and native source checks for this correction are still
pending at this evidence checkpoint. The exact Linux direct probe result has
not yet been recovered into the durable promotion acceptance record; Dev
acceptance, BUG-026 live exit-1 acceptance, QAS acceptance, and filesystem-
mailbox acceptance remain NOT RUN.
