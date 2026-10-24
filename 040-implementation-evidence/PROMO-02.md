# PROMO-02 — source freeze and published history

**Status:** PASS — public-command fixture qualification; no candidate build.

- Source implementation commit: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- The resolver verifies primary/mirror refs, captures one source SHA, proves
  branch ancestry, rejects unpushed or unrelated commits, and detects branch
  movement during the freeze. Disposable Git tests preserve dirty canonical
  checkouts and cover branch-only, commit-only, and branch-plus-commit inputs.
- Linux-host initially exposed a fixture portability failure at the
  implementation SHA: its Git 2.25.1 rejects `git init -b`. The regression
  setup was changed to `git init` followed by `git checkout -b` in
  `43a4e675ccccb89e1ea1c333e975ea028c9600fa`; the Linux source tests passed
  after that correction.
- No real source branch was advanced by the tests and no release was built.

Test and delivery details: [PROMO-07](PROMO-07.md).
