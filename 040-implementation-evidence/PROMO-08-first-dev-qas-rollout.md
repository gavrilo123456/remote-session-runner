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
