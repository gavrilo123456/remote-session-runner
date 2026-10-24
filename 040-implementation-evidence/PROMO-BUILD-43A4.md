# PROMO build evidence — source 43a4e675

**Result:** `ready_without_activation`. One immutable four-host release was
built and published to the existing host catalogs. No lane selector, service,
runtime configuration, mailbox, database, backup, retained work, or ACK was
changed. Lane promotion and live acceptance remain NOT RUN.

## Frozen identities

| Item | Identity |
| --- | --- |
| Candidate source | `43a4e675ccccb89e1ea1c333e975ea028c9600fa` |
| Captured `dev` tip / primary and mirror witness | `dbeb69d846ffaeff13193bfd7e9c4feaac0c6388` |
| Controller used for successful resumed build | `416c400f0b6580b1fa2315f3602f573edee4dfff` |
| Build ID | `fcf5bff3e0e3b62ce4fd88d9e7e7af62` |
| Build record | `created_utc=2026-10-24T13:56:34.16047Z`; `updated_utc=2026-10-24T14:09:18.746677Z`; stage `ready` |
| Release | `rr-20261024-43a4e675` |
| Release digest | `093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69` |
| Provenance digest | `aa45563daad56ef891f8f8b16664bcc9fa8edaeed55cb1c3b8ba786578c0a527` |
| Go toolchain | `go1.27.1` |
| Database schema compatibility in bundles | maximum/write schema 40 |

The resolver froze the requested candidate commit while proving its membership
in the captured `dev` history. Primary and GitHub mirror witnesses in the
durable build record both resolved to the captured tip above. The controller
revision is separate from the candidate source and is recorded in sealed
provenance.

## Controller corrections and delivery

The source candidate at `43a4e675` was not modified. Controller/provenance
corrections were committed and delivered on `dev`:

| Commit | Correction | Validation and delivery |
| --- | --- | --- |
| `91d16f90a432690075608579068558ad4538dc19` | Report CGO per native target and record the source-revision linker argument in actual build flags. | `go test ./src/internal/releaseflow -count=1` and Mac `make vet build smoke` passed; Gitea primary and exact GitHub mirror verified; workers fast-forwarded and package tests passed. |
| `dbeb69d846ffaeff13193bfd7e9c4feaac0c6388` | Accept verified owner-only SSH key modes `0400` and `0600` without changing key permissions. | Focused package test and Mac `make vet build smoke` passed; Gitea/mirror verified; all three workers fast-forwarded and focused tests passed. |
| `416c400f0b6580b1fa2315f3602f573edee4dfff` | Create a missing native bundle output path safely; verify and reuse identical existing artifacts; reject and preserve corrupt or mismatching artifacts. | Added create/reuse/mismatch/corrupt-path regressions. Focused package tests and Mac `make vet build smoke` passed; exact mirror verified; all workers fast-forwarded and focused tests passed. |

The first invocation through the sandboxed coordinator could not create its
private operation directory outside writable workspace roots. It failed
before allocating a build ID or starting a host build. The approved elevated
retry created and retained build ID `fcf5bff3e0e3b62ce4fd88d9e7e7af62`.
That first actual build attempt preserved a `build_blocked` record after the
Linux native helper rejected the missing output directory as an invalid
bundle. Reproduction on a disposable Linux worktree isolated the cause to
callers treating `ErrInvalidSimpleBundle` as “path absent.” The correction in
`416c400` added an `Lstat`-based absent/create vs existing/verify path and
regressions. The same build ID and frozen source were resumed; the candidate
was not rebuilt under a new identity.

## Source-test receipts

The four `native-full-v1` inputs were created from successful original test
results at source `43a4e675`; passing suites were not rerun to make receipt
files. Original UTC completion timestamps and evidence SHA-256 values were
preserved. Raw selected command-result records and canonical receipt JSON are
owner-only files outside Git under:

```text
~/projects-runtime/runner-release-operations/source-test-evidence/43a4e675ccccb89e1ea1c333e975ea028c9600fa/
~/projects-runtime/runner-release-operations/source-test-inputs/43a4e675ccccb89e1ea1c333e975ea028c9600fa/
```

| Host | Native toolchain binary SHA-256 | Original latest result UTC | Evidence SHA-256 | Canonical receipt SHA-256 |
| --- | --- | --- | --- | --- |
| `mac-control-plane` | `132b69336a1f809932a8a20b0201dbbb980e86e3a323ae32e893639d83d71598` | `2026-10-24T12:14:56.807Z` | `38fb53391778e80ae8cfd424fcb0f1548ee6e8a4a4c5b92d17d41616bbdfb20f` | `f8c2f6fefe65210c7e3174a26f6db970438db070d7f7eb3293a7637596f281e2` |
| `linux-host` | `30969f97169d7f43fe6a085873d75613adc21e30818a8c61d95bd27275df4624` | `2026-10-24T12:33:51.936Z` | `9536333e0a8071c65f246ccce30c39b1a6b11a7bb1b92fce7ecd7d9062044e8e` | `7239404c459188256131837c21a8b85d1c82f0cc6bc3672247aaa177948d41d5` |
| `sandbox-host` | `1675694ef690db0f18fbe7046a886170904bede1d9db6ec96ae27945c1705c64` | `2026-10-24T12:51:56.596Z` | `3c2e8a10d2e89009deb26c36faba3dcdbfffc636498a6e4c456819a01abc588b` | `214a18ba379fbc0d174b60797731d842c9cec7f158cc12cdd5766d2ba5796ee9` |
| `factory-sandbox` | `30969f97169d7f43fe6a085873d75613adc21e30818a8c61d95bd27275df4624` | `2026-10-24T12:39:13.615Z` | `1dc6b4d7e33eb99eecc9e5a05e7497b42cc4c63dbd5acaa44c0f077583a850cc` | `bbb02e99b036eb539aacfded9f2332c2e68faf34d412577572843bb420bc6599` |

The Mac and worker receipts each record all required native full-suite gates
as PASS with the original source, platform, Go binary hash, UTC test time and
evidence hash. The build verified receipt host identity, source identity,
target platform and toolchain against each native bundle.

## Host profile and space budget

The owner-only profile was created atomically at
`~/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json`;
it is mode `0600`. Its four entries use the existing verified hosts, SSH
identities, known-host files, repositories and Go 1.27.1 toolchains. Existing
owner-only SSH keys were left unchanged, including read-only `0400` keys.
Every `runtime_root` points to the workflow area
`~/projects-runtime/runner-release-operations/`, not the release catalog.

| Profile entry | Verified SSH target / identity source | Repository and toolchain |
| --- | --- | --- |
| `mac-control-plane` | Local Mac; existing Git SSH identity under `~/.ssh/gavrilo123456-github` | `/Users/tomasz.walczuk/projects/remote-session-runner`; `~/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go` |
| `linux-host` | `ubuntu@129.151.232.40`; existing Mac SSH identity `~/.ssh/remote-session-runner` | `/home/ubuntu/projects/remote-session-runner`; `/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go` |
| `sandbox-host` | `ubuntu@132.226.205.205`; existing Mac SSH identity `~/.ssh/dev.slidestud.io` | `/home/ubuntu/projects/remote-session-runner`; `/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go` |
| `factory-sandbox` | `ubuntu@145.241.255.66`; existing Mac SSH identity `~/.ssh/factory-sandbox` | `/home/ubuntu/projects/remote-session-runner`; `/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go` |

Sandbox's measured build budget was:

| Peak-space component | Bytes |
| --- | ---: |
| Frozen source/worktree bound | 41,900,669 |
| Candidate staging reserve | 50,000,000 |
| Additional catalog-copy reserve | 50,000,000 |
| Temporary files and cache-growth allowance | 300,000,000 |
| Residual operating buffer | 500,000,000 |
| **Required before build** | **941,900,669** |

Sandbox `/dev/sda1` had `1,511,538,688` bytes free immediately before the
successful resume/build, exceeding the requirement by `569,638,019` bytes.
After publication, fresh checks reported `1,367,654,400` bytes free, exceeding
the same reserved budget by `425,753,731` bytes. The latest capacity observation
also recorded the existing operations root at `42,275,277` bytes and the
release catalog at `885,807,496` bytes. No catalogs, caches, backups, journals
or retained data were deleted.

## Build invocation and published bundles

The immutable initial invocation captured the source and allocated the build
ID. It later stopped with the recorded `build_blocked` result described above.
This is its source-selection command:

```sh
# Mac coordinator, repository root
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache \
GOTOOLCHAIN=local \
'/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' run ./src/cmd/build-release \
  --branch dev \
  --commit 43a4e675ccccb89e1ea1c333e975ea028c9600fa \
  --release-id rr-20261024-43a4e675 \
  --hosts-file "$HOME/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json" \
  --store-root "$HOME/projects-runtime/runner-release-operations" \
  --source-test-receipt "$HOME/projects-runtime/runner-release-operations/source-test-inputs/43a4e675ccccb89e1ea1c333e975ea028c9600fa/mac-control-plane.json" \
  --source-test-receipt "$HOME/projects-runtime/runner-release-operations/source-test-inputs/43a4e675ccccb89e1ea1c333e975ea028c9600fa/linux-host.json" \
  --source-test-receipt "$HOME/projects-runtime/runner-release-operations/source-test-inputs/43a4e675ccccb89e1ea1c333e975ea028c9600fa/sandbox-host.json" \
  --source-test-receipt "$HOME/projects-runtime/runner-release-operations/source-test-inputs/43a4e675ccccb89e1ea1c333e975ea028c9600fa/factory-sandbox.json" \
  --json
```

After the missing-output-path correction was delivered, this command resumed
the same ID and completed publication without re-resolving source or
rebuilding already-completed host artifacts:

```sh
# Mac coordinator, repository root
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache \
GOTOOLCHAIN=local \
'/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' run ./src/cmd/build-release \
  --resume fcf5bff3e0e3b62ce4fd88d9e7e7af62 \
  --hosts-file "$HOME/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json" \
  --store-root "$HOME/projects-runtime/runner-release-operations" \
  --json
```

The durable public status command for continuation is
`runner-release status --build fcf5bff3e0e3b62ce4fd88d9e7e7af62 --json`;
the build is already `ready`, so no further resume is needed.

| Host | Platform | Bundle bytes | Sealed bundle SHA-256 |
| --- | --- | ---: | --- |
| `linux-host` | Linux/amd64 | 44,606,928 | `f3628574b1ffd50e0e0f11790039f8b404701de475a34770b7b64b63d9f56c37` |
| `sandbox-host` | Linux/arm64 | 42,241,568 | `3da6a23a44ef96bbd8681bc7f8504e6ec114fa9d5049336240b80085e543f0d9` |
| `factory-sandbox` | Linux/amd64 | 44,606,928 | `206a1b7e010aadf64139f81b26077716be7a5090de4c4e19e318c975f3309e29` |
| `mac-control-plane` | Darwin/arm64 | 56,865,672 | `a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062` |

Provenance records CGO disabled for all Linux hosts and enabled for Darwin,
plus `-trimpath`, `-buildvcs=false`, and the exact linker setting
`-X remote-session-runner/src/internal/buildinfo.SourceRevision=43a4e675ccccb89e1ea1c333e975ea028c9600fa`.

## Publication verification and boundary

`build-release status` reports `stage=ready` and all four published hosts:
`linux-host`, `sandbox-host`, `factory-sandbox`, and `mac-control-plane`.
Read-only `host-inspect-release` checks on the Mac and all three workers exited
0 and confirmed the same release ID/digest, each host's exact bundle and
manifest hashes, source revision, native platform, component hashes, and
schema metadata. Publication is therefore verified; it is not selector
activation or functional lane acceptance.

PROMO-08 remains NOT RUN. No `promote-to` command was issued, no service was
restarted, no selectors changed, no mailbox request or ACK was published, and
no project publisher was resumed.
