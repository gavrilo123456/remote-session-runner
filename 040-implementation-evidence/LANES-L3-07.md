# LANES-L3-07 — sealed fixed builder and host-build outbox

**State:** BLOCKED — source S and registered H/N source gates are delivered
and verified. The ordered L3-06/L2-09 acceptance chain remains BLOCKED by the
unprovisioned L2-07 Linux and L2-08 Mac fixtures. This unit is not PASS and has
no evidence-closeout E or live host acceptance.

## Pre-unit record

- Pre-unit Mac `dev` HEAD: `e0510c629774a898c3637c87c09dac9f0eb8b7f7`.
- Source predecessor: L3-06 source S
  `8d1b7c876b00274f0284f15f27ebee8939a8d5a3`.
- L3-06 evidence at pre-unit HEAD: blob
  `8da8e221d08af9f48ba7a8714f3212e3700560f7`,
  `040-implementation-evidence/LANES-L3-06.md`. L3-06 remains BLOCKED by the
  ordered L2-09/L2-07/L2-08 acceptance chain; no predecessor E is asserted.
- Owner-approved incremental source work remains in force. B021/B024
  maintenance closeout, publisher pause, production credentials, and service
  changes are not prerequisites for this isolated source unit.
- B021/B024 remain BLOCKED with their retained evidence and held ACKs.
  BUG-022/023 remain deferred. The existing maintenance package remains a
  snapshot and was not rebuilt.

## Pre-unit read inventory

Blob IDs are from pre-unit HEAD `e0510c629774a898c3637c87c09dac9f0eb8b7f7`.

| Source | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `d6935a9d6643a2c4b28c8210a194e27f7ee2e9d9` |
| L3-06 predecessor evidence | `8da8e221d08af9f48ba7a8714f3212e3700560f7` |
| Test coverage manifest | `76bed5bd1370bff3813fd82ff7813549ca9f2bb8` |
| `src/internal/release/payload_schema.go` | `20602b239115a11034bb4326862f27b37d44ec5b` |
| `src/internal/release/envelope.go` | `31a032fc8b7e726c681b2f2d295f556a9d655653` |
| `src/internal/release/l3_payload.go` | `f3cb21f3b0e5e8012face0592bdaae2962a25413` |
| `src/internal/lanetests/manifest_test.go` | `6a5d73d503663e15ad2fe7c8ac9304c537a8dd3f` |
| `Makefile` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` |
| `go.mod` | `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |

## Source deliverable

`src/internal/release/sealed_builder.go` adds a fixed build service whose public
request contains only an opaque 64-character authorization ID. A trusted
resolver supplies the host/component/platform matrix row, matching Mac/Gitea/
GitHub source commit IDs, toolchain/input/template digests, and sorted evidence
references. Its resolver atomically consumes the one-use authorization ID
before returning the job. Invalid/replayed tuples, identities, digests, and
evidence fail before source export or build execution.

The service generates all workspace and output paths beneath a verified private
staging root. It exports the resolved commit, rejects symlinks and special
files, makes the source tree read-only, and invokes only its configured fixed
executor with the resolved build tuple and service-generated paths. It opens
the single fixed output name with `O_NOFOLLOW`, requires a bounded regular file
owned by the configured builder UID, copies it into a separate private sealed
directory, hashes the sealed bytes, and cleans the read-only workspace even
when the build fails.

The resulting closed `host_build_attestation` payload is passed to a separate
signer/outbox interface. The concrete `SignedHostBuildOutbox` constructor
requires a root-owned process and outbox directory, holds a copied host
attestation identity, enforces the local host binding, signs the canonical
record, and publishes it by fsyncing a private temporary file and linking it
into the fixed outbox without replacement. Identical retries are idempotent;
conflicting record IDs are refused. The build request and executor never
receive signing-key bytes or an outbox path.

The production constructors require a root service and a distinct non-root
builder identity. H/N fixtures use an isolated temporary root and synthetic
test key through package-private test construction; they do not prove an
installed process ran under a privileged UID transition. The configured
`FixedBuildExecutor` implementation must use the approved installed binary,
toolchain/input set, and builder identity and must stop/reap the process tree
before returning. No fixed builder, signing key, root helper, or host outbox was
installed or used against production. Those live installation and identity
checks remain required by later L3 package/rehearsal gates.

Six H assertions cover request closure, root/dedicated-identity constructor
guards, exact provenance-to-attestation/outbox binding, refusal of unsafe or
inconsistent resolved inputs before execution, sealed-output isolation and
idempotent outbox behavior, and symlink/size-limit refusal. One N assertion
uses the host filesystem to verify no-follow output handling and durable
signed-record publication in a temporary outbox.

## Source gates

Execution machine: `mac-control-plane` (Darwin arm64), Makefile-pinned Go
1.27.1; shared Mac Go caches reused.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-07 LANES_TIER=H` | PASS — 6 registered H assertions |
| `LANES_NATIVE_HOST_ID=mac-control-plane make test-lanes UNIT=LANES-L3-07 LANES_TIER=N` | PASS — 1 registered native N assertion |
| `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release -run '^TestLanesL307' -count=1` | PASS |
| `make check build` | PASS — full tests, vet, smoke, and command builds |
| `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json` and `git diff --check` | PASS |

The full macOS suite emitted existing nonfatal duplicate `-lproc` linker
warnings.

## Source delivery and synchronized host validation

Intermediate source S2 is `516bf8a557450c25767862bfb8f2bbe79ab62c37`, committed on
Mac `dev` as `fix(LANES-L3-07): clean read-only build workspaces`. Initial
source S1
`b98b8a5da8bd8f70b1a00720c9a72d6c58a08ce7` was also pushed and mirrored, but
its H gate exposed a cleanup error: read-only exported directories prevented
non-root test cleanup. The fix restores write permission only on the generated
workspace directories before removal. The first S1 host H runs failed on all
three Ubuntu hosts; no S1 host N gate ran. S2 was then pushed to primary Gitea,
and direct `ls-remote` queries verified Gitea `origin/dev` and automatic
GitHub `github/dev` at exact S2.

Final source S is
`203447fd18cf1fbd9ec53d74c3ff7562925868cc`, committed on Mac `dev` as
`fix(LANES-L3-07): claim build authorizations once`. It adds a persisted
one-use claim contract to the resolver and a regression proving replay does
not re-export or rebuild. The final explicit-key push to Gitea succeeded;
direct `ls-remote` verification returned exact S for both Gitea `origin/dev`
and automatic GitHub `github/dev`. S2's corrected cleanup passed all required
gates before S3; after S3, every registered gate was repeated against final S.

Before S2 pulls, all Ubuntu checkouts were clean on `dev` at S1. Before final
S pulls, all three were clean on `dev` at S2. Each fast-forwarded through its
GitHub mirror using the configured explicit identity. All three checked out
exact S and remained clean after validation. No tracked source was edited or
copied directly on Ubuntu.

Delivery commands:

```sh
# Mac, primary Gitea push
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Mac, automatic GitHub mirror verification
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev

# Ubuntu, after clean dev/pre-pull verification
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

| Machine | SSH alias / Mac identity | H | N | `make check build` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- | --- |
| `mac-control-plane` | local Mac checkout | PASS (6) | PASS (1) | PASS | PASS | `203447fd18cf1fbd9ec53d74c3ff7562925868cc` |
| `linux-host` | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS (6) | PASS (1) | PASS | PASS | `203447fd18cf1fbd9ec53d74c3ff7562925868cc` |
| `sandbox-host` | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS (6) | PASS (1) | PASS | PASS | `203447fd18cf1fbd9ec53d74c3ff7562925868cc` |
| `factory-sandbox` | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS (6) | PASS (1) | PASS | NOT RUN — `CGO_ENABLED=0`, `cc` absent | `203447fd18cf1fbd9ec53d74c3ff7562925868cc` |

Each Ubuntu host ran:

```sh
make test-lanes UNIT=LANES-L3-07 LANES_TIER=H
LANES_NATIVE_HOST_ID=<canonical-host-id> make test-lanes UNIT=LANES-L3-07 LANES_TIER=N
make check build
```

`linux-host` and `sandbox-host` also ran the focused race command with the
Makefile-pinned Ubuntu Go 1.27.1 toolchain:

```sh
/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/release -run '^TestLanesL307' -count=1
```

The first S1 H failure on each Ubuntu was caused by the fixture cleanup, not a
host-specific code or toolchain failure. After final S, registered H/N, full
source gates, focused race on Mac/linux-host/sandbox-host, and final clean HEAD
checks passed. Factory-sandbox reported `CGO_ENABLED=0`; `cc` was absent, so
only its focused race gate was NOT RUN.

## Scope and acceptance state

This was source implementation and isolated validation only. No production
credentials, Runner requests/ACKs, installed helpers, services, configuration,
or production work were changed. L3-07 remains BLOCKED by the ordered
L3-06/L2-09/L2-07/L2-08 acceptance dependencies; no PASS, live build, actual
privilege-transition proof, host installation, or evidence-closeout E is
claimed. B021/B024 remain BLOCKED with retained evidence and held ACKs;
BUG-022/023 remain deferred.
