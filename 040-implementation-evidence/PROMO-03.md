# PROMO-03 — native build, seal, and publication workflow

**Status:** PASS — implementation, disposable-backend qualification, and
non-activating four-host release publication.

- Source implementation commit: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- The workflow freezes one source identity, builds host-native bundles through
  pinned host helpers, verifies a common source/toolchain and native manifest,
  seals one immutable four-host release identity, and resumes publication
  only for missing verified bytes. Conflicting labels, hashes, or host
  identities fail closed.
- Public subprocess fixtures cover build resumption, interrupted publication,
  exact source binding, partial-host publication, and immutable identity.
- Full `go test -count=1 ./...` and native vet/build/smoke gates passed on all
  four host platforms; see [PROMO-07](PROMO-07.md). Four canonical, owner-only
  `native-full-v1` receipts were created from the original successful test
  evidence without rerunning passing suites.
- A native-build provenance mismatch was corrected in controller code before
  the build: Linux CGO is disabled, Darwin CGO is enabled, and provenance now
  records the exact source-revision linker argument. A subsequent first-build
  failure exposed that a missing output directory was incorrectly treated as
  an invalid existing bundle. The safe create-or-verify correction and
  regression were delivered before resuming the same build record.
- Build `fcf5bff3e0e3b62ce4fd88d9e7e7af62` sealed release
  `rr-20261024-43a4e675`, published its immutable bytes to all four existing
  host catalogs, and finished `ready_without_activation`. Release digest,
  provenance digest, host hashes, receipts, capacity budget, and verification
  are in [PROMO-BUILD-43A4](PROMO-BUILD-43A4.md).
- Catalog publication changed no selector or service state. PROMO-08 lane
  promotion and live acceptance remain separately instructed and NOT RUN.

Qualification details: [PROMO-07](PROMO-07.md).
