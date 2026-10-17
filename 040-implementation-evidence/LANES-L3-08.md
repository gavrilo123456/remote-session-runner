# LANES-L3-08 — verified release cache and same-host backup recovery

**State:** BLOCKED — Source S `1844fb5abfd4dd5c6c7be731e5ac07fa9148c8ce`
and registered H/source gates pass on Mac and all three synchronized Ubuntu
checkouts. The required field acceptance needs Linux storage/controller
fixtures and approved Mac APFS fixtures that are not available. No unit PASS or
evidence closeout E is claimed.

## Pre-unit record and scope

- Pre-unit Mac `dev` HEAD: `9a1b3e10276437a5006e124c925e583cf757bff0`. The
  worktree contained only the in-progress L3-08 builder/cache integration edits
  completed in this unit; no unrelated changes were present.
- The latest delivered L3-07 revision at that predecessor is
  `203447fd18cf1fbd9ec53d74c3ff7562925868cc`, with its evidence and synchronized
  validation recorded at the current predecessor HEAD. L3-07 remains BLOCKED
  by the earlier L3/L2 field-acceptance chain; this source work does not turn it
  into PASS or claim live builder installation.
- `LANES-L1-02` is already PASS at source `fbb866789a01f7dcc442164c51ccf255b95405db`;
  its six H assertions and exact delivery are preserved. This unit does not
  repeat or amend L1-02.
- The owner-authorized incremental source-work scope applies. B021/B024
  maintenance closeout, publisher pause, production credentials, a maintenance
  window, and service changes are not prerequisites for this hermetic unit.
  B021/B024 remain BLOCKED with their evidence and held ACKs preserved;
  BUG-022/023 remain deferred. The existing maintenance package remains a
  snapshot and was not rebuilt.
- Tests use synthetic signed records, keys, and isolated temporary roots. No
  production credential or mailbox state was read; no request or ACK was
  published; no host storage, service, configuration, firewall, or production
  Runner was changed.

## Read inputs at the predecessor

The lane idea, detailed design, phased plan, execution handbook, `AGENTS.md`,
ledger, L3-07 evidence, L2-07/L2-08 blocker evidence, coverage manifest, and
relevant release, test-dispatch, Makefile, and module files were read for this
unit at pre-unit HEAD `9a1b3e10276437a5006e124c925e583cf757bff0`.

| Input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `38ea8044a0ee8033213fe4aa9bf0438ffb6de808` |
| L3-07 evidence | `a55480139bebca1ee9013d3cedcc11e614012dda` |
| L2-07 field blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 field blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| Coverage manifest | `b6b02a6bf96327eedb29c4a1668a53c87d05816f` |
| `release/sealed_builder.go` | `f066642088c1991f4195639313ca8827bb51430d` |
| `release/lanes_l307_test.go` | `3f8866eb40a7d878ba9e27dc4b89350e66668a48` |
| `release/envelope.go` | `31a032fc8b7e726c681b2f2d295f556a9d655653` |
| `release/payload_schema.go` | `20602b239115a11034bb4326862f27b37d44ec5b` |
| `release/l3_payload.go` | `f3cb21f3b0e5e8012face0592bdaae2962a25413` |
| `release/lanes_l302_test.go` | `d9c6811dd8d67a13b4b8aa693ad3af7a80d21ad4` |
| `lanetests/manifest_test.go` | `a84fad7fc9df0d705578a7f4b5a596cead44d556` |
| `Makefile` / `go.mod` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` / `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |

## Source work

`VerifiedReleaseCache` accepts only the fixed per-platform cache and backup
roots, root-owned `0700` directories, positive bounded-by-policy capacities,
and distinct device identities. It does not create the roots. Artifact caching
re-reads the signed local `host_build_attestation` from the trusted outbox,
checks current host-key trust and the exact host/platform tuple, then hashes the
read-only sealed descriptor before making content-addressed immutable copies
in both stores. Writer locks serialize budget accounting and publication.

Candidate caching validates the authority signatures for the candidate and
required host-management manifest, all 16 runtime and 20 management build
attestations against current host keys, the complete schemas/matrices, exact
management-manifest reference, and every local artifact digest. The stored
chain contains signed records only; trust keys are resolved from current
protected state on every open.

Opening an artifact re-verifies the complete chain and local host/platform
tuple. It returns a verified cache descriptor when present. If the cache copy
is absent or mismatched, it restores only matching bytes from the same-host
backup; mismatched cache entries are moved into a retained quarantine before
replacement. If neither store has the verified bytes or chain, the operation
fails closed. No source checkout rebuild path exists.

Seven registered H assertions cover installed-constructor/root/device rules,
signed local build record and exact descriptor hashing, complete candidate and
management chains, untrusted/incomplete evidence refusal, budget exhaustion,
backup rehydration, corrupt-evidence preservation, and missing-cache-plus-
backup closed stop. L3-08's required F tier remains a separate live storage
gate and is not represented by these temporary-root fixtures.

## Mac source gates

Execution machine: `mac-control-plane` (Darwin arm64), Makefile-pinned Go
1.27.1, shared Mac `GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-08 LANES_TIER=H` | PASS — 7 registered H assertions |
| `make test-lanes UNIT=LANES-L3-07 LANES_TIER=H` | PASS — 6 registered H assertions after read-only sealed-descriptor handoff change |
| `LANES_NATIVE_HOST_ID=mac-control-plane make test-lanes UNIT=LANES-L3-07 LANES_TIER=N` | PASS — 1 registered N assertion |
| `make check build` | PASS — full `go test ./...`, `go vet ./...`, and command builds |
| Focused race `go test -race ./src/internal/release -run '^TestLanesL30[78]' -count=1` | PASS |
| Pinned `gofmt`, JSON parse, `git diff --check` | PASS |

During development, the initial restricted-shell formatting attempt failed
because `gofmt` was not on `PATH`; the pinned Go 1.27.1 tool was used. The first
L3-07 H rerun found the sealed descriptor was still write-only (`bad file
descriptor`) after cache handoff was added. The builder now closes its write
descriptor and reopens the sealed output read-only with no-follow checks; L3-07
H/N, L3-08 H, full source gates, and focused race tests then passed. A Mac test
fixture initially traversed `/tmp`'s symlink; it now uses `/private/tmp`. No
failure was waived.

## Source S delivery and synchronized validation

Source S is `1844fb5abfd4dd5c6c7be731e5ac07fa9148c8ce`, committed on Mac `dev`
as `phase(LANES-L3-08): add verified cache and backup recovery`. The explicit
identity push updated primary Gitea `origin/dev` to S. Git could not update the
Mac's local `origin/dev` tracking ref in the initial sandboxed push; a direct
Gitea `ls-remote` verified exact S, then an authorized `git fetch origin dev`
updated the local tracking ref. Direct `ls-remote` verified automatic GitHub
`github/dev` at exact S before any Ubuntu test.

Before pulling, all three Ubuntu checkouts were clean on `dev` at
`9a1b3e10276437a5006e124c925e583cf757bff0`, with `origin` pointing to the
GitHub mirror. Each fast-forwarded from that exact predecessor to S with the
configured `/home/ubuntu/.ssh/gavrilo123456-github` identity. Tests ran only
after each checkout's clean `dev` status and exact S HEAD were confirmed.
Normal SSH fallback ran checkout-local Go commands because they are not Runner
mailbox work; no tracked source was edited or copied on Ubuntu.

| Machine | Platform | L3-08 H | L3-07 H/N | `make check build` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- | --- |
| `mac-control-plane` | Darwin arm64, Go 1.27.1 | PASS (7) | PASS (6/1) | PASS | PASS | Mac `dev`, S |
| `linux-host` | Linux amd64, Go 1.27.1 | PASS (7) | PASS (6/1) | PASS | PASS | clean `dev`, S |
| `sandbox-host` | Linux arm64, Go 1.27.1 | PASS (7) | PASS (6/1) | PASS | PASS | clean `dev`, S |
| `factory-sandbox` | Linux amd64, Go 1.27.1 | PASS (7) | PASS (6/1) | PASS | NOT RUN — `CGO_ENABLED=0`, `cc` absent | clean `dev`, S |

The Ubuntu gates ran these commands at S:

```sh
make test-lanes UNIT=LANES-L3-08 LANES_TIER=H
make test-lanes UNIT=LANES-L3-07 LANES_TIER=H
LANES_NATIVE_HOST_ID=<canonical-host-id> make test-lanes UNIT=LANES-L3-07 LANES_TIER=N
make check build
GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/release -run '^TestLanesL30[78]' -count=1
```

The focused race command passed on `linux-host` and `sandbox-host`. Factory's
Go toolchain reports `CGO_ENABLED=0` and no `cc` is installed, so its focused
race test was not run. All three checkouts were rechecked clean on `dev` at S
after validation. No required source gate failed or was waived.

## Field acceptance blockers

The required L3-08 F acceptance is **NOT RUN**. Its real saturation and
rehydration checks require the exact fixed cache plus independent backup
devices on all hosts, with measured identities and sufficient capacity:

- L2-07 records no approved disposable A01 ext4 `project,quota` data and
  independent backup devices on the Ubuntu hosts. `linux-host` also has the
  documented hybrid cgroup hierarchy; `factory-sandbox` lacks required `io`
  propagation. Their specific maintenance, performance, and approval gates
  remain open.
- L2-08 records no approved disposable APFS lane/cache/backup fixture volumes
  on the Mac. Their UUIDs, mount/container identities, quotas, and isolated
  capacity have not been provisioned and measured.

The hermetic tests prove source logic on temporary directories only. They do
not prove distinct physical volumes, quotas, real saturation, reboot/device
recovery, or production acceptance. Until those fixtures and approvals exist,
L3-08 remains BLOCKED and cannot receive a PASS or evidence closeout E.

This blocked validation record and the ledger update are documentation-only
closeout C, committed after S. C does not constitute evidence closeout E: the
mandatory F acceptance remains blocked, so the unit and every dependent
acceptance gate stay open.

## Documentation delivery C

The L3-08 source and synchronized validation record is committed separately
from source S, after the required source gates and Ubuntu validation were
complete. This documentation-only update changes this unit evidence and the
execution ledger. It does not change the frozen source candidate at S or imply
F acceptance, unit PASS, or E.
