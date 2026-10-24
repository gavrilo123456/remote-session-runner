# PROMO-07 — public workflow and native source qualification

**Status:** PASS for executable fixture workflow and full native source gates
on all four platforms. A real release build/publication and live
deployment/acceptance are NOT RUN.

## Source revisions and delivery

- Feature implementation: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- Older-Git test fixture correction: `43a4e675ccccb89e1ea1c333e975ea028c9600fa`.
- The correction was pushed to primary Gitea `origin/dev`; the exact GitHub
  `github/dev` ref was verified at `43a4e675ccccb89e1ea1c333e975ea028c9600fa`.
- `linux-host`, `sandbox-host`, and `factory-sandbox` were clean on `dev` at
  `1979fa6` before pull; each fast-forwarded from the mirror to `43a4e67` and
  then reported that exact HEAD with a clean worktree.
- Git fixture versions observed: Linux 2.25.1, Sandbox 2.34.1, Factory 2.53.0.
  The Linux 2.25.1 failure on `git init -b` was preserved as a failure at the
  initial implementation revision and fixed in test setup; no runtime behavior
  or release data changed.

## Tests and host coverage at `43a4e675ccccb89e1ea1c333e975ea028c9600fa`

| Machine | Native platform / toolchain | Results |
| --- | --- | --- |
| Mac | Darwin/arm64, Go 1.27.1; binary SHA-256 `132b69336a1f809932a8a20b0201dbbb980e86e3a323ae32e893639d83d71598` | `make test` PASS (full repository); `make vet`, `make build`, and `make smoke` PASS. Linker emitted duplicate `-lproc` warnings for existing test binaries; exit status remained 0. |
| linux-host | Linux/amd64, Go 1.27.1; binary SHA-256 `30969f97169d7f43fe6a085873d75613adc21e30818a8c61d95bd27275df4624` | Uncached `go test -count=1 ./...` PASS; `make vet`, `make build`, and `make smoke` PASS. `GOMAXPROCS=1`, `GOFLAGS=-p=1`; store package 150.909 s, execution 63.304 s, localapi 76.788 s. |
| sandbox-host | Linux/arm64, Go 1.27.1; binary SHA-256 `1675694ef690db0f18fbe7046a886170904bede1d9db6ec96ae27945c1705c64` | Uncached `go test -count=1 ./...` PASS using `nice -n 10`, `GOMAXPROCS=1`, `GOFLAGS=-p=1`; `make vet` and `make build` PASS. Bounded one-binary-at-a-time help/version smoke for all 12 Makefile commands PASS. Store package 181.122 s, execution 70.002 s, localapi 95.100 s. Root free space at final pre-build inventory: 364,351,488 bytes. |
| factory-sandbox | Linux/amd64, Go 1.27.1; binary SHA-256 `30969f97169d7f43fe6a085873d75613adc21e30818a8c61d95bd27275df4624` | Uncached `go test -count=1 ./...` PASS; `make vet`, `make build`, and `make smoke` PASS. `GOMAXPROCS=1`, `GOFLAGS=-p=1`; store package 49.623 s, execution 23.985 s, localapi 30.102 s. |

The uncached full repository suite and native vet/build/smoke gates passed on
all four platforms. Sandbox smoke used the same 12 help/version assertions as
the Makefile target but reused one temporary binary to limit peak disk usage.
No canonical four-host source-test receipt files were created, and no
non-plan `build-release` was started. Public-command fixture tests exercise the
workflow on disposable four-host backends; their results are part of the Mac
full suite. These are source/fixture results only: no release bundle
publication, lane acceptance, direct/queued functional acceptance, or live
deployment is claimed.

## Scope and remaining stage

- No services were stopped or restarted; no selectors, runtime configuration,
  databases, backups, mailbox roots, requests, or ACKs were changed.
- The default owner-only host profile
  `~/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json`
  was confirmed absent. Sandbox had 364,351,488 bytes free on `/dev/sda1` and
  retained 843,551,092 bytes of catalog data; no retained artifact was
  deleted. A safe capacity budget and owner-only build profile are required
  before actual build/publication.
- PROMO-08 live walkthrough is separately instructed and NOT RUN. It requires
  four current owner-only native-full source receipts, the owner-only host
  profile, a selected immutable release, and its independent operational
  gates before promotion.
