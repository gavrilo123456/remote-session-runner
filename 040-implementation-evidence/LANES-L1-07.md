# LANES-L1-07 — registry-loss, corruption, rollback stop and reconstruction validation

**State:** IN PROGRESS — Mac H and source gates passed. Source S delivery and
the three Ubuntu validations remain NOT RUN.

## Predecessor and pre-unit read inventory

- Ordered predecessor: `LANES-L1-06` evidence closeout E
  `ec802f7cfa6cd25ce85c74e7dcc9c6b7ca834024`; the Mac `dev` worktree was
  clean at this revision before L1-07 edits.
- Pre-unit HEAD: `ec802f7cfa6cd25ce85c74e7dcc9c6b7ca834024`.
- The current lane documents, repository instructions, original PoC documents,
  ledger, predecessor evidence, and source contracts were reloaded at that
  HEAD. Their blob revisions were:

| Read input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `0deba872bda483c70fd3329c5cdc0ee5943f95c5` |
| L1-06 predecessor evidence | `21c49e0ee008219338ae1959b81058b419bd1069` |
| Prior L1-05 evidence | `534ab6ab88b022487b328a366322919dd01eda3e` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |

Relevant implementation read set at the pre-unit HEAD:

| File | Blob |
| --- | --- |
| `src/internal/laneregistry/registry.go` | `239b9786e3d09abdfa7cdfe03f58647468604b58` |
| `src/internal/laneregistry/registry_test.go` | `1a7f6ed2b69a557f87bc92c2054dd6c9bc9c77b6` |
| `src/internal/laneregistry/operations.go` | `da6c45bc8149005e4be59d7201c5c98e40c95e2f` |
| `src/internal/laneregistry/operations_test.go` | `cc7cc32de8e8c65cace098fc48eb61304a5f3899` |
| `src/internal/laneroot/validator.go` | `9646b7322f1394f14ae917d423bf5ac8c742b76c` |
| `src/internal/lanetests/manifest.go` | `b8b94c45bafb48811b0119bd71fa4186e7ffd18e` |
| `src/internal/lanetests/manifest_test.go` | `b1dcefe7fc21c2c1b02b7fc80ae9be102f6e54d4` |
| `040-implementation-evidence/LANES-test-coverage.json` | `fdbdc2c288ba5c0464a02f9d701211c4a5e7f86d` |
| `Makefile` | `29a5cf0a1c6874e66d608ca879d960ab363a533d` |

The read confirmed the design §4 registry-recovery stop, A09 evidence inputs,
the handbook's L1-07 H contract, the exact lane sequence, and the L1-06 record
and transitions. No plan amendment or external approval is needed for this
hermetic source unit. A real A09 reconstruction remains a separate root-
maintenance operation requiring its incident evidence, verified authority and
anchor facts, fresh L0 descriptors, explicit approval, and closed dispositions.

## Scope and implementation

This unit adds a pure reconstruction-proposal validator and an
anchor-generation-aware open path. It does not write or initialize a registry,
repair evidence, invoke authority or anchor services, change pointers, close a
live service, or perform an A09 operation.

- `OpenAtVerifiedGeneration` accepts a structurally valid registry only when
  its generation meets an externally verified high-water floor. A lower
  generation returns `ErrRegistryRecoveryRequired` and preserves the bytes.
  Missing or corrupt state continues to fail through `Open`; `Initialize`
  cannot replace an existing incomplete/corrupt registry directory.
- `ValidateRegistryReconstruction` binds each proposed record to a separate
  verified root fact, requires a new generation above the verified floor, and
  checks fresh Mac root descriptors, the exact five mailbox children, volume
  identity, ancillary inventory, and root/identity collision uniqueness.
- Active roots require all four verified surface anchors. Prepared roots must
  remain tied to a retained in-flight lane operation.
- Every in-flight Dev/QAS or PRD operation requires one exact operation digest
  and a closed evidence disposition. PRD pre-commit V2 disposition requires
  matching fence marker, paired snapshot, service/anchor and commit evidence;
  post-commit can select only V3 forward repair; ambiguous state can only
  remain closed. No disposition restarts V2 or infers a pointer.
- Evidence digests in these inputs are references supplied by separately
  verified readers. This package validates their shape and relationships; it
  does not cryptographically verify signatures or attestations.
- The tests use synthetic identities and temporary local fixture directories.
  They do not read production credentials or mailbox roots.

Source S is intended to contain only:

- `040-implementation-evidence/LANES-test-coverage.json`
- `040-implementation-evidence/LANES-L1-07.md` (this in-progress evidence)
- `src/internal/laneregistry/registry.go`
- `src/internal/laneregistry/reconstruction.go`
- `src/internal/laneregistry/reconstruction_test.go`
- `src/internal/lanetests/manifest_test.go`

## Mac source gates

Commands ran on `mac-control-plane` (Darwin arm64), Go 1.27.1, using the
Makefile's shared Go caches:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-07 LANES_TIER=H` | PASS — all 8 registered H assertions |
| `make test` | PASS — full suite after correcting the missing-assertion test to use L1-08 |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| `go test -race ./src/internal/laneregistry ./src/internal/lanetests -run 'TestLanesL107\|TestLanesL101ManifestMatchesPlan' -count=1` | PASS |
| configured `gofmt`, coverage JSON parse, `git diff --check` | PASS |

Execution history: the initial `gofmt` lookup on `PATH` failed because this
shell does not expose the configured Go toolchain binaries; rerunning the
toolchain's absolute `gofmt` path passed. The first full `make test` exposed a
manifest-test expectation still treating L1-07 as unregistered. The test was
updated to use L1-08, and the full suite then passed. Neither failure was a
product-contract failure.

## Source delivery and remaining gates

- Source S, primary Gitea push, exact GitHub mirror SHA, and all three clean
  Ubuntu `dev` fast-forwards: **NOT RUN** at this in-progress evidence stage.
- After the exact S is synchronized, each Ubuntu host must run the H gate,
  full test suite, vet, build, smoke, and the focused race test where cgo is
  available. Host results and final checkout SHAs will be recorded before E.
- Runner was not used: these are checkout-local Go tests. The authorized SSH
  fallback is suitable for the Ubuntu build/test commands after the GitHub
  mirror and clean-`dev` guards pass.

## Independent blockers preserved

- `LANES-B021-02` remains **BLOCKED**, and its retained evidence is preserved.
- `LANES-B024-02` remains **BLOCKED**, and its retained evidence is preserved.
- `LANES-PREP-00` remains **BLOCKED**; `LANES-L0` remains **BLOCKED / NOT
  STARTED**. BUG-022/023 baseline repair units remain deferred.
- No production credentials, Runner requests or ACKs, services, publisher
  state, storage, firewall rules, or runtime configuration were changed for
  L1-07. No live reconstruction or lane acceptance is claimed.
