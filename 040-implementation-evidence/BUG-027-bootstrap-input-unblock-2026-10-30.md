# BUG-027 bootstrap input and renewal-ordering unblock — 2026-10-30

## Exact original inputs found and preserved

The original inputs were present under `/private/tmp/cat027-prd-309de02/`.
They were not reconstructed. Independent checks verified regular, single-link,
current-owner files with mode `0600` and the exact recorded SHA-256 values:

| Input | Original file | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| Request | `bootstrap-request-corrected.json` | 15001 | `a87e040f25e5f04f3c10dac448cd79ece259c2252ba0b51842b1e30ed7b7ef22` |
| Approval | `bootstrap-approval-20261029T110127Z.json` | 1367 | `e6cfd2a34957bda938056b6e32b0ee6cc8ac5d79b9ee289378d88dc4696d5ac9` |

Verified byte-identical copies were preserved under the private durable root
`/Users/tomasz.walczuk/projects-runtime/runner-release-operations/recovery-inputs/02bfdc603d2ea3360ecd32501fe347d1/`:

- `bootstrap-request.json`
- `original-approval.json`
- `MANIFEST.json` (source paths, byte counts, hashes and modes only)

The destination directories are `0700`; files are `0600`. Existing originals
were not edited or replaced, and no private contents were printed or committed.
The earlier missing-input blocker is superseded by these verified paths.

## Actual next blocker and correction

The saved operation is at `linux_namespace_ready` on unchanged sealed release
`rr-309de02fca-ddc84f50`. Its last controller amendment is `3187ec3`; there are
seven amendments against an eight-record bound. The original continuation
expired at `2026-10-30T03:07:45.17119Z`.

The old public sequence was circular: controller update required an unexpired
continuation, while window renewal required an amendment matching the new
controller. Restoring the inputs alone could not complete that sequence.

The correction lets the public renewal command, under its existing PRD lane
lock and fresh preflight, validate the exact owner-authorized deadline,
request, original approval, release, operation checkpoint and unchanged
non-controller identities; append the matching controller amendment; then
write the immutable renewal. A retry after interruption between these writes
reuses the amendment. Existing renewal records remain immutable, ordinary
controller-update expiry checks remain strict, and the eight-record bound is
unchanged. A changed profile-only input is rejected before any amendment write.

This changes releaseflow operator code only. It does not rebuild service
bundles or invalidate the recorded Dev/QAS acceptance of the sealed release.

## Validation and delivery

Tests cover the actual expired-continuation/older-controller state, seven to
eight amendments, interrupted renewal and exact retry, changed non-controller
inputs, preserved original records and refusal after the authorized deadline.

Mac Go 1.27.1 darwin/arm64 validation passed on the final source patch:

- Full `go test ./src/internal/releaseflow -count=1`: PASS, 36.822 seconds.
  Log `/private/tmp/rsr-bootstrap-renewal-ordering-releaseflow-20261030.log`,
  SHA-256 `0de9cde0fa276cc025743ad11b847b98dba5741b136f1c4842033b79c23a5cdf`.
- `go vet ./src/internal/releaseflow`: PASS; log
  `/private/tmp/rsr-bootstrap-renewal-ordering-vet-20261030.log` is empty.
- Native `runner-release` build and `--help` / `--version`: PASS. This
  validation-only binary is unattested and is not approved for live commands.
- `git diff --check`: PASS; independent read-only review found no remaining
  blocker after the pre-write non-controller identity check was added.

The checks use `GOTOOLCHAIN=local`, the shared Mac Go build cache and the
already-existing `/Users/tomasz.walczuk/go/pkg/mod` module cache used by the
preceding passing gates. No shared cache was cleared or copied.

The source correction and initial handoff/evidence were committed on Mac `dev`
as `51bee111dbb78c4be4cd73228777cb2b1cab50ec`. Primary Gitea and the automatic
GitHub mirror were both verified at that exact SHA before remote validation.
All three workers began clean on `dev` at `4eeb9d0`, fast-forwarded from the
mirror, and remained clean at the tested source commit.

Each worker used its pinned Go 1.27.1 and normal Go caches, with one task-owned
disk-backed temporary directory removed after all checks exited. Each passed:
`go test ./src/internal/releaseflow -count=1`,
`go vet ./src/internal/releaseflow`, native `runner-release` build with the
source revision embedded, `--help`, `--version`, and `git diff --check`.
These are complete changed-package and operator checks, not a claim that a new
whole-repository suite or live lane acceptance was run for this correction.

| Worker | Platform | Checks elapsed | Source-check log SHA-256 | Free bytes afterward |
| --- | --- | ---: | --- | ---: |
| Replacement Linux | linux/amd64 | 52.78 s | `e487d2c2ed15d2c58d95f886ed01274e1994f2642eb228111030bb54cb489114` | 38757711872 |
| Sandbox | linux/arm64 | 57.36 s | `9ad3db3177f26c5358a32214cb104e5f0e186a4a4d90a754555645e5f3708c3d` | 1568571392 |
| Factory | linux/amd64 | 57.43 s | `fb9919f049af2810fd01899ab6dcb33183821c8b5dc7af0ecad23be5f52da9db` | 34015375360 |

On each worker the log is
`/home/ubuntu/.cache/remote-session-runner/bug027-input-unblock-51bee111/source-checks.log`.
Mac capture summaries are under
`/private/tmp/rsr-bug027-maintainer-unblock-20261030/*-validation.json`.
The final evidence-only checkpoint is delivered after these results, before
building/freezing the live operator. No further controller amendment has been
consumed by this source and documentation delivery.

## Operational boundary and next sequence

No live renewal, controller amendment, root-mode correction, Mac route update,
service/selector change, request, ACK or #514 recovery was performed by this
assistance. The preserved coordinator journal SHA-256 remains
`56a76c0d30be801d4cfcdad4c8fcfd9a336cf4c7c4bdfce5ded365ba1d3521f8`;
the original continuation remains
`a3a601b3ea63738c98778d7fac6fb49187d0e04f6fc64fef6438c60a09c42f09`.

Finish source/evidence delivery, then freeze the final controller HEAD through
renewal and bootstrap completion. The reviewed assistance helper is
`/private/tmp/rsr-bug027-maintainer-unblock-20261030/resume-bootstrap.py`;
its adjacent `controller-manifest.json` binds the final controller revision
and the attested operator binary digest. It verifies a clean, unchanged Mac
HEAD, operator/input hashes, modes, checkpoint and deadline before invoking
the two supported public commands. The helper was syntax-checked, not executed;
its underlying public-command behavior is covered by the package regressions.
Do not execute it before final primary/mirror/worker synchronization and the
attested operator manifest are complete. Use the durable original-input copies with
`prd-bootstrap-window-renew` directly, followed by the same operation's
`prd-replacement-bootstrap` and existing continuation file. The authorized end
is `2026-10-30T10:00:00Z` (11:00 Warsaw), for bootstrap only. Fresh guards must
pass; no result in this document claims PRD installation or acceptance.

After the Mac route completes, prepare the separate current PRD promotion
plan and release-bound approval, then use the toolkit's cohort deployment and
eight acceptance gates. Preserve mailbox routing and keep clients paused.
