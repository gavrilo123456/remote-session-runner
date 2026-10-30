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
preceding passing gates. No shared cache was cleared or copied. Native worker
validation and primary/mirror delivery of this patch follow its source commit;
their results will be recorded before a live operator is handed over.

## Operational boundary and next sequence

No live renewal, controller amendment, root-mode correction, Mac route update,
service/selector change, request, ACK or #514 recovery was performed by this
assistance. The preserved coordinator journal SHA-256 remains
`56a76c0d30be801d4cfcdad4c8fcfd9a336cf4c7c4bdfce5ded365ba1d3521f8`;
the original continuation remains
`a3a601b3ea63738c98778d7fac6fb49187d0e04f6fc64fef6438c60a09c42f09`.

Finish source/evidence delivery, then freeze the final controller HEAD through
renewal and bootstrap completion. Use the durable original-input copies with
`prd-bootstrap-window-renew` directly, followed by the same operation's
`prd-replacement-bootstrap` and existing continuation file. The authorized end
is `2026-10-30T10:00:00Z` (11:00 Warsaw), for bootstrap only. Fresh guards must
pass; no result in this document claims PRD installation or acceptance.

After the Mac route completes, prepare the separate current PRD promotion
plan and release-bound approval, then use the toolkit's cohort deployment and
eight acceptance gates. Preserve mailbox routing and keep clients paused.
