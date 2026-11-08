# BUG-030 — Selector discovery requires unavailable artifacts from an unrelated historical rollout

| Field | Value |
| --- | --- |
| Status | `CLOSED` — controller correction and live Dev continuation verified |
| Severity | High — blocks guarded promotion after partial cohort switching |
| Reported / closed | 2026-11-08 |
| Component | `src/internal/laneinstall/catalog_selector.go`, `FindCatalogSelectorOperationsForPlan` |
| Fixed revision | `1ecf91075d75a71944a7dc33b68c0a7e72ee07b5` |
| Scope | Controller correction; sealed BUG-029 service release unchanged |
| Closeout | [BUG-029 Dev deployment evidence](../040-implementation-evidence/BUG-029-dev-deployment-2026-11-08.md) |

## Problem and reproduction

Discovery of one cohort's selector journals incorrectly required every older
journal's referenced catalog and backup artifacts to remain available. Missing
artifacts from an unrelated rollout therefore blocked the current deployment.

Disposable reproduction:

1. Preserve a canonical journal for historical cohort A, then retire its fixture
   target, predecessor or backup.
2. Prepare independent cohort B with valid release and backup evidence.
3. Request B's journals through `runner-lane catalog-selector-find`.
4. Observe `invalid or inconsistent catalog selector operation`, caused by A's
   missing payload before the cohort filter runs.

Do not reproduce this by deleting installed releases, backups or journals.

## Exact incident

BUG-029 Dev operation `9dd615f3f0ee4e54b02a8dc1863a3c3d` stopped safely after
Linux switched and before Sandbox switched. Common Dev preparation kept the
lane held/stopped; the blocked attempt was not recorded as acceptance.

The first Sandbox journal in lexical discovery order was:

| Field | Value |
| --- | --- |
| Journal | `010b89fcb44d08cff6ed6e0d844d3d39.json` |
| SHA-256 | `3851fb68b59347c5cac0e816cb2b1d14253c88205063e42419868861a5da1a6d` |
| Historical cohort | `33b2c3a3f6399253444c6ae29f0ecb97` |
| Stage / missing target | `complete` / `cat-20261021-c76bac5` |

It was canonical JSON in an ordinary UID-1001-owned `0600` single-link file.
Its legacy predecessor passed supported bundle verification. Its backup
manifest existed and matched the recorded hash; diagnostic inspection did not
open the database. Supported inspection of the historical catalog target
returned `lstat ...: no such file or directory`. None of the 32 preserved
journals belonged to the current cohort.

All original journal bytes and metadata were captured privately. No journal
was deleted, edited or relabelled to unblock the deployment.

## Cause and correction

Discovery called full `validateCatalogOperation` on every journal before
filtering `CohortOperationID`. That validation resolves release/predecessor
bytes and verifies backups, creating an unnecessary historical dependency.

The correction separates metadata checks from physical artifact validation:

- Every row still requires a protected filename, owner, mode, single link,
  canonical JSON, allowed fields, correct host/lane, exact target binding and
  confined predecessor/backup paths.
- Matching rows still require full physical evidence. Missing or inconsistent
  release, predecessor or backup data fails closed for the requested cohort.
- Direct inspection, selection, reconciliation and rollback retain full
  validation. Multiple matching journals remain visible to the ambiguity guard.

## Verification and delivery

Disposable fixtures cover unrelated missing targets, predecessors and backups;
strict matching-artifact checks, including a separately missing backup;
retagged incomplete history; unsafe metadata/JSON/modes/links; and multiple
matching journals. They also verify preserved historical bytes and selectors.

Go 1.27.1 `laneinstall`/`releaseflow` package tests, relevant vet checks and native
operator builds passed on Mac, Linux, Sandbox and Factory. The first Mac attempt
remains **NOT PASS**: its private temporary root inherited group 0 instead of
primary group 20. The corrected capture root passed the same packages without
changing the failing fixtures. These were operator checks, not a new full
repository qualification or service-release build.

The correction was pushed to primary Gitea, verified at the exact automatic
GitHub mirror SHA and fast-forwarded to all three clean Ubuntu `dev` checkouts
before live use.

## Live result

The toolkit resumed the same operation and completed with exit 0 at
**2026-11-08 08:27:10 UTC**. All eight cohort acceptance gates passed. Health
and peer-lane postflight checks passed across all four Dev hosts.

Release `rr-20261108-bug029-1263205` retained source
`126320552acf0a2609d7d75c09016b867b1dbd13` and its sealed bundle hashes.
No service bundle was rebuilt or resealed for this correction. QAS and PRD
were not promoted. This closeout does not claim filesystem-mailbox acceptance.

Exact incident captures: [journal diagnosis and preserved copies](/private/tmp/rsr-bug029-sandbox-selector-diagnostic-24v2xooo/diagnosis-compact.json).
