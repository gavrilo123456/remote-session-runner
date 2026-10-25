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

## Linux probe evidence recovery implementation and source gates

The Linux direct-mTLS helper did complete successfully. The retained Dev
operation `dea80fa695a606e64985f0b2b53ebeb4` has a hash-chained final event
(sequence 25, SHA-256
`7cb8b3d2730b0a8a6b1fb585b3d22dc3b1e1db0826aacc9dd4735b7c32b31251`) at
`2026-10-24T21:05:40.977410Z`. Its exact error is
`linux-host direct-mtls functional probe: catalog-probe-once did not emit a valid bounded outcome (exit=<nil>): release workflow record is invalid or corrupted`.
The controller source then used is at `3e8afc9`; its strict result decoder
omitted helper fields `accepted_at`, `http_status`, and
`readiness_http_status`, while the helper emitted those fields. `cmd.Run()`
returned nil, and the helper's exit-zero contract requires a complete,
successful terminal result, exit code 0, positive final event sequence,
complete untruncated output, and closed teardown. The accepted scope is still
owner-only (0600, one link) and binds Linux/Dev/direct-mTLS, operation
`dea80fa695a606e64985f0b2b53ebeb4`, controller
`runner-controller-dev-linux-host`, configuration SHA-256
`842a17572ba218ee770b39d8cf95c442c4885204d479d5b34ef6d2e4e5ff82f5`, request
SHA-256 `5f422ed1d2e7f61d21dec92437ef566f29abba73ea321410070929ca23152f60`,
and job `job-705974bc8f474d77898670837be15b61`. Scope creation was
`2026-10-24T21:05:35.923209659Z`; expiry was
`2026-10-24T21:07:35.404263511Z`, so the controller error followed creation by
about 5.05 seconds and preceded expiry. The later status-only read returned
HTTP 503 `maintenance_admission_closed`; no request was resubmitted.

The recovery correction accepts only this exact latest hash-chained
controller-error event paired with the accepted owner-only scope, exact job,
operation, host, transport, configuration, and an event timestamp inside the
scope lifetime. It records the helper exit-zero contract and event digest,
does not invent an accepted-at timestamp or numeric final sequence, and never
replays the probe. All other accepted scopes still require successful
status-only observation; a failed status read remains a failure. Regression
coverage exercises the public `runProbe` path without replay, malformed or
mismatched scope data, configuration drift, time-boundary failures, a later
operation event, event-chain tampering, and the normal-versus-recovered
acceptance rules. The diagnostic status reader now reports bounded HTTP and
response-code details.

Source gates for controller revision
`6b3ed49a90356c52b881192fcfa7e426ce30561a` (Go 1.27.1) are now recorded on
the required machines. Mac `make test`, `make vet`, `make build`, `make smoke`,
the focused `releaseflow`, `catalog-probe-once`, and `runner-lane` packages,
and `git diff --check` passed. On `linux-host`, those three changed-package
tests and native `make vet`, `make build`, `make smoke`, and `git diff --check`
passed; its earlier interrupted full-suite attempts remain NOT PASS and are
not relabeled. On `factory-sandbox`, the full Linux/AMD64 `make test` suite,
`make vet`, `make build`, `make smoke`, and `git diff --check` passed. On
`sandbox-host`, Go reported Linux/arm64; its full `make test` suite, `make
vet`, `make build`, `make smoke`, and `git diff --check` passed. Sandbox free
space was 1,536,241,664 bytes immediately before the suite and 1,436,053,504
bytes after it, above the 500,000,000-byte operating floor. All three
Ubuntu checkouts were clean at this exact commit before the runs.

The earlier Sandbox 327,888,896-byte reading remains a historical failed
capacity check, not a current gate result; no cache, retained data, or project
file was deleted. The configured Mac module cache later failed `go mod verify`
because 12 extracted module directories were incomplete. Their contents were
preserved; the attempted move for cache repair was denied. Read-only command
inspection and status were then run with the existing verified user module
cache and the required shared build cache. This cache issue does not change the
passing Mac source-gate record, but should be repaired or explicitly resolved
before relying on that configured module cache again.

## Resumed-operation recovery correction

After source gates and evidence delivery at `6b3ed49`, the supported Dev
resume was retried for operation
`dea80fa695a606e64985f0b2b53ebeb4`. The first invocation stopped before
workflow execution because the sandbox denied creating the coordinator
`dev.lock`; it recorded no operation event. The authorized retry reached the
public command but failed status-only observation of the already accepted
Linux job. It did not create another probe scope or replay the command. The
operation remains held, with its original event 25 and this exact event 26:

- UTC `2026-10-24T22:23:01.362164Z`
- SHA-256 `ca2f018046faeadc1fa12725f1b6ea3369f78f285a6917d13fc7b6083c818b36`
- Error: `linux-host direct-mtls functional probe: existing accepted probe could not be safely observed (exit=exit status 1): release workflow record is invalid or corrupted`

The defect is in the controller recovery matcher. A supported retry changes a
blocked operation back to `Prepared` and clears `LastError` before calling
`AcceptCohort`; the matcher incorrectly required `Blocked`, the original
failure in `LastError`, and the helper-error event to be the latest event. It
therefore fell through to status observation instead of recovering from the
preserved helper-exit event 25. The next controller correction accepts only a
unique exact hash-chained helper-error event whose scope, job, host, lane,
transport, controller, configuration, request and event time still match. It
also permits only the exact later failed status-only observations of that same
scope, while rejecting unrelated or successful later events. The probe helper
remains observation-only; no accepted command is replayed.

The new regression reproduces resume at `Prepared` after a failed status-only
observation, verifies recovery is tied to event 25 rather than event 26, and
asserts the only host call is `--observe-existing`. Negative tests reject
unrelated later failures and later success. The focused `releaseflow` package
passes on Mac Go 1.27.1. The full Mac suite initially failed only because the
restricted execution sandbox denied the test's `/bin/ps` call for its own
fixture child. The rerun with process-inspection access passed `make test`,
`make vet`, `make build`, `make smoke`, and `git diff --check`; the harmless
Darwin linker warning about duplicate `-lproc` remains. For these Mac commands,
the required shared GOCACHE was used. The configured shared GOMODCACHE had
incomplete extracted modules; `go mod verify` flagged those directories and
cache repair was denied. The existing user Go module cache supplied the
already verified dependencies for the passing gates and controller run.

The controller-only correction at source commit
`af688abc344b9e62f2066b445ad5f4599fcecbdb` does not change the sealed release
`rr-20261024-43a4e675` or any host service bundle. On that exact revision,
`factory-sandbox` passed the full Linux/AMD64 `make test`, `make vet`,
`make build`, `make smoke`, and `git diff --check`. `sandbox-host` passed its
full Linux/arm64 suite and the same remaining gates; free space was
1,290,891,264 bytes before and 1,280,495,616 bytes after. `linux-host` passed
focused `catalog-probe-once`, `runner-lane`, and `releaseflow` tests plus native
vet/build/smoke/diff checks; its earlier interrupted full suite remains NOT
PASS. All worker checkouts were clean and at `af688ab` before testing. Mac
Go 1.27.1 full test/vet/build/smoke/diff gates also passed on this source.

The Dev operation remains held after event 26; the `af688ab` correction has
not yet been exercised by a live retry. No complete Dev acceptance is
recorded. Other direct/queued transports, Mac-local route, limits, capacity,
lifecycle, isolation, BUG-026 exit-1, and dedicated filesystem-mailbox
acceptance remain NOT RUN. QAS promotion and acceptance remain NOT RUN. QAS/PRD
services, selectors, configurations, databases, and mailbox paths remain
unchanged.

## Current walkthrough checkpoint — 2026-10-25 00:02 UTC

This checkpoint supersedes the earlier NOT RUN statements above for the Dev
operation. It does not change the separately pending QAS pause/drain gate.

The sealed candidate is `rr-20261024-43a4e675`, digest
`093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`, source
`43a4e675ccccb89e1ea1c333e975ea028c9600fa`, Go 1.27.1. Per-host bundle
SHA-256 values remain unchanged:

| Host | Bundle SHA-256 |
|---|---|
| `linux-host` | `f3628574b1ffd50e0e0f11790039f8b404701de475a34770b7b64b63d9f56c37` |
| `sandbox-host` | `3da6a23a44ef96bbd8681bc7f8504e6ec114fa9d5049336240b80085e543f0d9` |
| `factory-sandbox` | `206a1b7e010aadf64139f81b26077716be7a5090de4c4e19e318c975f3309e29` |
| `mac-control-plane` | `a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062` |

Dev operation `dea80fa695a606e64985f0b2b53ebeb4` is complete. Its
hash-chained event 27 records `cohort-accepted`, outcome `passed`, at
`2026-10-24T22:43:54.467199Z`; the acceptance evidence SHA-256 is
`cf414c289550f07f7b1836cacedc4c8979248b77143c137e5231cd1a7adaac39`. The
`catalog-functional-v1` gates all passed: capacity, direct-mTLS, isolation,
lifecycle, limits, Mac-local, queued-SSH, and teardown. The sealed release
metadata and all four bundle hashes match the acceptance event. The earlier
failed probe/controller events remain in the receipt history.

BUG-026 and route-specific Dev checks:

- A preliminary direct run using the stale environment name `mac-dev` was
  rejected as `environment_not_configured`; it is retained as a failed
  attempt, not acceptance. The configured Dev mapping is
  `mac-workstation-dev` → `local/mac-workstation`.
- The exact exit-1 probe produced
  `BUG026_EXPLICIT_EXIT1_DIAGNOSTIC`; job
  `job-e39bf19bc056ce08ba3cea4bd84fcffe`, session
  `sess-15b4f5afcfef6fb563de1c5544b13311`, command
  `cmd-0e31e8692c08730a331bd29be5f846aa`. The terminal command result was
  `failed`, exit code 1, complete and untruncated output, complete event
  history through sequence 4, and confirmed closed/runtime-closed teardown.
- A successful Mac-local command emitted `PROMO_DEV_MAC_LOCAL_SUCCESS`; job
  `job-6cde3330aa1f16c01301f25bfaa6b6a7`, session
  `sess-7cf1ee999d6ad19b7d859c5a976fda8e`, command
  `cmd-200c311f77a5d2f3f85c48c3e7c0a8a6`. It exited 0 with complete,
  untruncated output, event history through sequence 4, and confirmed teardown.
- Dedicated Dev filesystem-mailbox request
  `req-promo-dev-local-mailbox-20261025-01` completed as job
  `job-0fc1b28eb00fb8c45046fd73188f041d`, command
  `cmd-7576e71b42cbb5a4057c55a30f0aedcd`; its response revision 4 had four
  available events through cursor 4 and emitted `PROMO_DEV_MAILBOX_OK`. The
  exact response was ACKed through the native marker-last publisher; the
  lifecycle read returned `terminal_acknowledged`, and post-run work inventory
  was empty. No historical mailbox content or other ACK was changed.

The first supported Mac Dev stop reported completion while an orphaned
`runner-local` still owned the Dev `local-api.sock`. Read-only process and
`lsof` evidence bound PID 53881 to the Dev `current/bin/runner-local`, Dev
configuration and Dev service root; PID 53850 was its expected
`runner-log-capture` parent. Under the already authorized Dev maintenance,
the verified wrapper received SIGTERM; the listener disappeared and supported
start/status/readiness then passed. This was a controller defect, not a
service-bundle defect.

Controller correction commit `1b81c7b340ff29b5b2178fe7eef320a883b2ed7c`
adds a Mac stop path that runs the existing stopped-work proof before
inspection, identifies only the exact lane socket/process/executable/config
and expected log-capture parent, signals the wrapper gracefully, waits for
wrapper and child exit plus socket release, and repeats the quiescence proof.
It refuses an unexpected binary, wrapper, multiple listener, or nonempty-work
proof. Regressions cover a valid orphan, wrong executable, wrong wrapper,
failed work proof, and the distinct `local.log`/`locald.log` paths. It changes
no Runner service bundle, selector, configuration, database, or release.

Source delivery for `1b81c7b340ff29b5b2178fe7eef320a883b2ed7c`:

- Mac Go 1.27.1 `make test vet build smoke`: **PASS** on the committed source;
  `git diff --check`: **PASS**. The first restricted full-suite attempt was
  blocked only because the existing Darwin process-lifecycle test could not
  invoke `/bin/ps` for its test-owned child; the complete suite passed when
  that read-only host inspection was permitted. The harmless duplicate
  `-lproc` linker warning remains.
- `linux-host`, `sandbox-host`, and `factory-sandbox` were clean at predecessor
  `459014e3b5b66ba002661866a56b9f35990cb57c`, fast-forwarded from GitHub
  `origin/dev`, and verified clean at the exact new commit. Each passed
  `go test -count=1 ./src/internal/laneinstall`, native `make vet build
  smoke`, and `git diff --check`. Sandbox ran on Linux/ARM64; Linux and Factory
  ran on Linux/AMD64.
- Mac pushed `dev` to primary Gitea and verified `origin/dev` by
  `ls-remote`; the automatic GitHub mirror resolves to the exact same SHA.
  The push succeeded, though the sandbox denied updating Mac's local
  remote-tracking ref. Remote primary and mirror verification and all worker
  HEADs are exact.

Fresh QAS plan-only observations after source delivery show all four QAS
selectors still on `cat-20261021-b84caf0`, all four selected-lane services
running, and a common empty-work inventory hash
`92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b`.
Backups and selector journals are not yet verified; no QAS mutation occurred.
The plan confirms QAS common effects include an all-four-host selected-lane
admission/dispatch fence, inventory/config/schema capture, backups, then
Linux → Sandbox → Factory → Mac switching. QAS publishers/clients have not
been confirmed paused and drained, so QAS deployment and acceptance remain
NOT RUN. PRD remains outside this rollout.

After worker gates, a direct verified-SSH Sandbox `/dev/sda1` read measured
`1,768,181,760` bytes available (`df -B1`). This exceeds the 500,000,000-byte
operating buffer by `1,268,181,760` bytes; the QAS backup allowance still
needs a fresh operation-specific measurement before backup or selector
writes. No caches, retained work, backups, mailboxes, or files were deleted.

## QAS deployment and installed acceptance — 2026-10-25

This current closeout supersedes the earlier QAS pending/NOT RUN checkpoint
statements above; those earlier statements describe the pre-deployment state.

The owner confirmed QAS clients were not in use and their clients were working
through PRD. The supported QAS promotion applied its selected-lane maintenance
hold, collected fresh work/capacity/configuration/schema evidence, verified
backups, and switched the complete cohort in the documented order. The owner
confirmation and supported hold are the pause basis; no pause was inferred
from directory counts. PRD was not held, restarted, reconfigured, or routed
through QAS.

The promoted release is the same sealed Dev-accepted candidate:

| Field | Value |
|---|---|
| Release | `rr-20261024-43a4e675` |
| Release digest | `093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69` |
| Source revision | `43a4e675ccccb89e1ea1c333e975ea028c9600fa` |
| QAS operation | `12f901fcfdf216d633938653ad4a7871` |
| Dev operation | `dea80fa695a606e64985f0b2b53ebeb4` |
| QAS branch synchronization | Complete |

| Host | Sealed bundle SHA-256 |
|---|---|
| `linux-host` | `f3628574b1ffd50e0e0f11790039f8b404701de475a34770b7b64b63d9f56c37` |
| `sandbox-host` | `3da6a23a44ef96bbd8681bc7f8504e6ec114fa9d5049336240b80085e543f0d9` |
| `factory-sandbox` | `206a1b7e010aadf64139f81b26077716be7a5090de4c4e19e318c975f3309e29` |
| `mac-control-plane` | `a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062` |

Dev acceptance passed all `catalog-functional-v1` gates: capacity,
direct-mTLS, isolation, lifecycle, limits, Mac-local, queued-SSH, and teardown.
Its evidence SHA-256 is
`cf414c289550f07f7b1836cacedc4c8979248b77143c137e5231cd1a7adaac39`. QAS
acceptance passed the same eight gates on the identical release and bundle
hashes; its evidence SHA-256 is
`fcd7c6a515416904a53088f2f96f138bc1a8fccbe0a1d302c3455c35819b0f30`. Both
acceptance records are release-bound. Earlier failed controller and probe
attempts remain preserved in their operation histories and are not counted as
passing gates.

### Installed QAS BUG-026 explicit-exit probe

The selected Mac QAS CLI reported build revision
`43a4e675ccccb89e1ea1c333e975ea028c9600fa` and SHA-256
`60590a88ea32a42176302380b81ee8aa632896ed0d0a0819fe1ee0ff1f60c258`. Its QAS
configuration hash was the promotion-time
`e00766f638ceb1f6102989ac1b2325128fda54daf6cabe63e87fd8beeb74c89a`. A fresh
Mac-local one-off run used environment `mac-workstation-qas`, target
`local/mac-workstation`, and idempotency key
`promo-qas-bug026-exit1-20261025-01` with this harmless script:

```sh
printf 'BUG026_QAS_EXPLICIT_EXIT1_DIAGNOSTIC\n'; exit 1
```

Runner returned exit 1 as expected. The terminal result was command `failed`,
exit code 1, complete and untruncated output containing the exact diagnostic,
final event sequence/cursor 4, and teardown `closed` with reason
`runtime_closed`. The event stream included the ordinary `command_failed`
terminal event. Job `job-9933c0c2bb10e54cfbee673ea8e07b06`, session
`sess-3b0a8f6d6f9c79b505d85e2827d174e3`, and command
`cmd-60ed16ef0b3eca01c227c736df0ad6c4` identify this execution. Captured CLI
output SHA-256: `8ec868080acf5d4ce2a823599061e3837325e534532930c60ffd8b0791301a59`.

### Dedicated QAS filesystem-mailbox acceptance

One new exchange was published only to QAS service-root inbox `default`; no
project mailbox path, PRD mailbox, historical request, or unrelated ACK was
touched. Request `req-promo-qas-local-mailbox-20261025-01` used idempotency key
`promo-qas-mailbox-local-20261025-01`, environment `mac-workstation-qas`, and
target `local/mac-workstation`. It emitted `PROMO_QAS_MAILBOX_OK` and completed
as job `job-a3d3fac9413b78a9b706ed64ef7b33cd`, session
`sess-7d48581c04aed79ea877f79cf2f98ee2`, command
`cmd-52a42daa9402cadf65d6c32ba3eeaea1`. The terminal response was revision 2,
with four available events through cursor 4; state `succeeded`, exit 0,
complete/untruncated output, and closed teardown. Its exact response/cursor
was ACKed marker-last. The owner-only lifecycle route then reported
`terminal_acknowledged` and `acknowledged: true`. The response and event prefix
were inspected before ACK; the post-test QAS work inventory is empty.

### Fresh cohort and peer-lane postflight

A read-only supported QAS plan after both checks reported all four selectors
on this release, schema 40, empty work inventory hash
`92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b`, and
500,000,000-byte required headroom. Fresh available bytes were 15,234,449,408
on Linux, 2,402,414,592 on Sandbox, 33,628,258,304 on Factory, and
131,203,510,272 on Mac. Its outcome was `planned_no_effects`.

All three worker QAS services were `active`; their Unix-socket health
responses reported `live`/`ready`, build revision `43a4e675…`, and zero active
session, active command, queued command, and queued intent counts. QAS Mac
ingress and local executor were also `live`/`ready` at 2026-10-25 07:07 UTC,
with zero active capacity, mailbox backlog, storage errors, and cleanup
failures. Mac QAS storage and cleanup counters were zero before and after the
two fresh tests (delta zero); worker health observations also reported zero
storage errors and cleanup failures. Readiness and route acceptance are
separate claims; direct-mTLS and queued-SSH passed in the release-bound QAS
acceptance record.

The read-only peer-lane check showed all four PRD selectors still on
`cat-20261023-c01e633`, all three PRD worker services and Mac PRD components
active/ready, and no PRD configuration or mailbox ownership change was made.
Mac PRD health at observation time also showed two active session slots, two
active command slots, and four uncertain remote intents per router. This was
observed and left untouched; it is not QAS work and does not establish empty
PRD capacity.

Dev and QAS acceptance are complete for this release. PRD deployment and
acceptance were not authorized in this walkthrough and remain **NOT RUN**.
QAS mailbox assignment is unchanged; external project clients remain on PRD.
