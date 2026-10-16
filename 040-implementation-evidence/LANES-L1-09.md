# LANES-L1-09 — reversible PRD migration fence and deferred-pair ledger

**State:** PASS — S2 source gates and all required synchronized-host gates
passed. Factory-sandbox race testing is unavailable (`CGO_ENABLED=0`, no `cc`)
and is recorded as NOT RUN. This source-only unit does not close maintenance
or claim installed/live acceptance.

## Predecessor and read inventory

- Ordered predecessor: `LANES-L1-08` evidence closeout E at
  `36477d3cd598c2f98543a02fc1d7fd4c1b6cbf44`.
- Pre-unit HEAD was the same E on clean Mac `dev`.
- Fresh reads covered repository instructions, lane idea/design/plan/handbook,
  original design documents and plan, execution ledger, predecessor and blocker
  evidence, coverage manifest/dispatcher, mailbox fencing/snapshot/client code,
  and registry code. Their pre-unit blob revisions are:

| Read input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Execution ledger | `a0900f71b3d5266b768bd6d8e1d960f2388306bb` |
| L1-08 predecessor evidence | `9dc26a2c3678b9c562827e485a63ffc86a461b1c` |
| B021-02 blocker evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| B024-02 blocker evidence | `15211861260284d0923c9d64259bd7746700399e` |
| H-coverage manifest | `485e59fe47045bebed5da4a5068ed3c24801ad36` |
| lane test dispatcher | `b8b94c45bafb48811b0119bd71fa4186e7ffd18e` |
| dispatcher tests | `4c77400d572bc61c6e8842c537ff2c26a7cd77c1` |
| lane registry | `d1b17daeca2a35ca483089fbfcbf4b256791005d` |
| lane registry tests | `1a7f6ed2b69a557f87bc92c2054dd6c9bc9c77b6` |
| mailbox fence | `ce3e9d8a5bf5846f595123170c311f8fc0c906b4` |
| mailbox snapshot | `cfd107cc2be4416013fbe2d5fb776b79511d7559` |
| mailbox fence tests | `30f51b7115bc3b5b0a0c5a8e9d42e90e6b3cad8f` |
| mailbox client | `c6c5edf63d65400a04db202da8e58459d66a2694` |
| mailbox client tests | `938a80413ddcd57cfb8e757d2c17272a970b2ebb` |

The blocked B021-02 and B024-02 evidence remains applicable and unchanged.
Those maintenance units are not prerequisites for this isolated H source unit;
no production credential, publisher pause, mailbox publication, service
change, or installed host configuration is used. BUG-022/023 remain deferred.

## Scope and implementation

Implement the reversible PRD migration-fence record and controller distinct
from mailbox retirement. The durable `fencing` record captures publisher
capability/pause-control inventory and every V2 member's initial state before
any pause or service action. Progress requires ordered pause, stop, service
manager disable, process/socket absence, and zero-work evidence. The controller
will write a root-identity/configuration/snapshot-bound owner-only marker only
after all evidence passes and an exclusive descriptor snapshot proves both
request and ACK trees empty.

The marker and registry use canonical digests and survive restart. Shared
publishers fail closed when either the final or atomic staging marker exists.
The deferred ledger records complete request/ACK pairs by descriptor-derived
paths, device/inode identities, file modes/sizes, SHA-256 digests, and
observation generation. Reconciliation and abort re-open and verify every
recorded pair; abort removes only the exact `aborted` marker. No code executes,
ACKs, deletes, replays, or manually releases deferred work, and no code starts
V2 or changes a host service.

Eight registered H assertions cover durable fencing before actions and
restart, tested publisher controls, ordered V2 proof, empty paired snapshot,
marker/registry equality, mismatch/replacement rejection, deferred request and
ACK preservation, exact-pair abort, and crash-gap reconciliation including an
atomic pending marker. Every fixture uses `t.TempDir`, synthetic identities,
and test-only roots.

## Mac source gates

Commands ran on `mac-control-plane` (Darwin arm64), Go 1.27.1, with the
Makefile's shared `GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-09 LANES_TIER=H` | PASS — 8 registered assertions |
| `make test` | PASS — full `go test ./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| focused race test for `TestLanesL109*` in `lanemigration`, `laneregistry`, and `mailboxfence` | PASS |
| coverage JSON parse and `git diff --check` | PASS |

Development failures retained in the history: PATH does not expose `go` or
`gofmt`, so the explicit Go 1.27.1 Makefile toolchain was used. The first
standalone race invocation used the shell's default build cache and hit a
sandbox cache-read denial; rerunning with the Makefile shared caches passed.
The first complete `make test` exposed a stale negative-dispatch test that
still expected L1-09 to have no assertions; it now checks L1-10, and the full
suite passed on rerun. Earlier fixture and controller assertions were corrected
as they surfaced and the focused H suite was rerun successfully.

Initial source S1 `62323c2c49bf1120c30d5d9699530720a284468c` was pushed to
Gitea, mirrored exactly to GitHub, and fast-forwarded to all three clean Ubuntu
checkouts. Its L1-09 H fixture failed on all three Linux hosts at
`open fixture mailbox root: mailbox fence path is unsafe`; because the focused
gate failed, subsequent full gates did not run there. Read-only `strace`
confirmed the fixture root opened and the failure occurred during temporary
root validation. The test fixture now explicitly restricts its temporary trust
root to `0700`, preserving the production safety checks. S1's failed host result
is retained here; S2 below is the corrected source revision.

## Source S2 and synchronized-host validation

Corrected source S2 is
`342ce30e2d23249404cb1c5c874d0436dc5ceb06`, committed on Mac `dev` as
`fix(LANES-L1-09): restrict temporary fixture root`. Primary Gitea
`origin/dev` and automatic GitHub mirror `github/dev` both returned exact S2.
The source push emitted a local read-only `.git` tracking-ref warning after
Gitea accepted the push; direct primary and mirror `ls-remote` checks verified
the exact SHA. Each Ubuntu checkout was clean on `dev` at S1 before pull, with
the expected GitHub `origin`; it then fast-forwarded `origin/dev` and was
verified clean at exact S2 before testing.

Against S2, each host ran:

```text
make test-lanes UNIT=LANES-L1-09 LANES_TIER=H
make test
make vet
make build
make smoke
```

`linux-host` and `sandbox-host` additionally ran:

```text
/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/lanemigration ./src/internal/laneregistry ./src/internal/mailboxfence -run TestLanesL109 -count=1
```

| Host | Platform / Go | Checkout and required gates at S2 | Focused race |
| --- | --- | --- | --- |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | Linux amd64 / Go 1.27.1 | PASS — clean `dev`, exact S2; H (8), full suite, vet, build, smoke | PASS |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | Linux arm64 / Go 1.27.1 | PASS — clean `dev`, exact S2; H (8), full suite, vet, build, smoke | PASS |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | Linux amd64 / Go 1.27.1 | PASS — clean `dev`, exact S2; H (8), full suite, vet, build, smoke | NOT RUN — `CGO_ENABLED=0`, no `cc` |

Post-validation checks confirmed each host remained clean on `dev` at exact S2.
Connections used explicit configured identities and
`StrictHostKeyChecking=yes`: `linux-host` via `remote-session-runner` and
`~/.ssh/remote-session-runner`, `sandbox-host` via `sandbox.env` and
`~/.ssh/dev.slidestud.io`, and `factory-sandbox` via `factory-sandbox` and
`~/.ssh/factory-sandbox`. The Mac SSH client printed a non-fatal warning that
the sandboxed `known_hosts` file could not be rewritten; strict host-key checks
and SSH commands completed successfully. No host checkout source was edited
outside Git; no mailbox publication or service action ran.

The maintenance package remains a historical snapshot. No candidate was
refreshed as part of L1-09, and B021/B024 evidence and BLOCKED states remain
unchanged. Evidence closeout E is this documentation-only delivery; its Git
SHA is not embedded here.
