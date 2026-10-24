# PROMO-03 — native build, seal, and publication workflow

**Status:** PASS — implementation and disposable-backend qualification;
actual release publication NOT RUN.

- Source implementation commit: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- The workflow freezes one source identity, builds host-native bundles through
  pinned host helpers, verifies a common source/toolchain and native manifest,
  seals one immutable four-host release identity, and resumes publication
  only for missing verified bytes. Conflicting labels, hashes, or host
  identities fail closed.
- Public subprocess fixtures cover build resumption, interrupted publication,
  exact source binding, partial-host publication, and immutable identity.
- Full `go test -count=1 ./...` and native vet/build/smoke gates passed on all
  four host platforms; see [PROMO-07](PROMO-07.md). A real non-plan build was
  not invoked, so no canonical owner-only `native-full-v1` receipt files were
  generated and no release ID or bundle hashes are claimed.
- The host profile at
  `~/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json`
  is absent. Sandbox also had only `364,351,488` bytes available on its full
  root filesystem, with `843,551,092` bytes of existing release catalogs
  preserved. No release catalog, rollback bundle, cache, or other retained
  data was removed. Without the profile and a safe build-space preflight, the
  real four-host build/publication remains NOT RUN.
- No catalog or service state was changed.

Qualification details: [PROMO-07](PROMO-07.md).
