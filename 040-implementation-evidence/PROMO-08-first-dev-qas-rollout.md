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

The corrected controller has not yet been committed or delivered to the
workers. Live promotion remains NOT RUN. The prior completed Dev receipt is
still holding admissions and must be handled only by supported workflow
controls after the corrected read-only plan exposes its state.
