# LANES-L3-11 — finite renewal grants and host nonce-time probes

**State:** BLOCKED in the ordered lane sequence. Source S and this unit's H/N
source gates passed on Mac and all three Ubuntu checkouts. The ordered
predecessor `LANES-L3-10` remains blocked on its required privileged F gate, so
this record does not claim L3-10 PASS, L3-11 unit acceptance, an evidence
closeout E, or live acceptance.

## Pre-unit state and scope

- Pre-unit Mac `dev` HEAD was `53cdbdb2f779512d581a38bf7a9edff5ec31dedb`, the
  L3-10 evidence commit. The active worktree already contained only the
  unfinished L3-11 source changes from the prior continuation; they were
  preserved and completed in this unit.
- The exact blocked predecessor is `040-implementation-evidence/LANES-L3-10.md`
  at evidence commit `53cdbdb2f779512d581a38bf7a9edff5ec31dedb`, blob
  `dd8b4404296477599e79a55cd224d5c27332177d`. L3-10 H/source gates passed;
  privileged F remains NOT RUN because its disposable privileged Mac fixture
  and approval are unavailable. L3-09/L3-08 acceptance and closeout also remain
  blocked by the previously recorded physical cache/backup fixtures.
- The owner's incremental-readiness decision permits this successive
  source-only unit while retaining those blockers. L3-11 requires H+N only;
  it needs no production credential, publisher pause, service change, root
  fixture, maintenance window, or B021/B024 recovery.
- Tests use temporary journal/anchor directories, deterministic synthetic
  Ed25519 test identities, a fake interactive-approval receipt, and simulated
  verified peer identities. No production key, socket, service, mailbox,
  credential, storage, firewall, or host configuration was read or changed.
  B021/B024 evidence remains preserved and BLOCKED; BUG-022/023 remain
  DEFERRED.

## Pre-unit input inventory

The following inputs were checked at pre-unit HEAD `53cdbdb2f779512d581a38bf7a9edff5ec31dedb`:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `14a365dba50468eb40f09382d23baa802b48b768` |
| L3-10 predecessor evidence | `dd8b4404296477599e79a55cd224d5c27332177d` |
| Lane coverage manifest | `8bf31c2b96f7752d9ada7daab9be29fb4035a33e` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| `Makefile` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` |
| `go.mod` | `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |
| Authority journal | `4f51c675b3650d595878b5cecaf3aa1de0076631` |
| Authority signer | `22c1ea3c035e76b065821d2e2e6d3e61fa95b3ce` |
| L3 payloads | `f3cb21f3b0e5e8012face0592bdaae2962a25413` |
| L3 trust and time-probe verifier | `e463407256c6ea710dd663a33d8021dea6a5e80b` |
| Security anchor | `e41b0fb24e739ad0eecee7470d2c9b9cd5fbd698` |
| L3-10 regression tests | `f22b92e6e4aea01ad81c7b33ed9584166a8fd76a` |
| Closed L3 payload schema | `f8a6c9d770e358ed2a341f07feb6b4b83c8f26b1` |
| Lane test dispatcher | `f5cc9bec4d6046257e5fa7853bb72dc9a278fe1b` |
| Lane test manifest checks | `56c156593a4853ed4a867280660f71cc0e309597` |

## Source implementation

- Added durable, finite directive-renewal and global-ledger grants to the
  authority journal. Each grant binds exact context, initial record/high-water,
  finite generation and issuance caps, expiry, and a warning with at least two
  renewal cycles of lead time. Issuance appends the signed envelope and advances
  the journal high-water atomically before returning it.
- Directive renewals derive the next same-phase lease and exact predecessor
  from stored grant state. The signer accepts no caller-selected payload. Host
  anchor validation now rejects skipped lease generations and mismatched
  predecessor records.
- Ledger renewals preserve the exact sorted revoked-record/key sets. A single
  active global grant serializes concurrent compare-and-swap attempts; another
  generation is denied until every canonical host has acknowledged publication
  of the previous generation.
- Publication receipts are append-only and host-bound. The journal accepts
  incremental acknowledgements without allowing a prior acknowledgement to be
  removed or the signed issuance to be changed. A losing concurrent CAS returns
  a replay/conflict error while leaving the journal usable.
- Added durable renewal-warning and one-issuance-remaining alerts. Added a
  host-bound authenticated challenge record for the Mac authority time probe;
  the signer returns only the stored nonce response, enforces the monotonic
  two-second window, and durably consumes both successful and stale challenges.
- Extended the closed directive payload schema with the predecessor record ID.
  Added five H assertions and one Linux N assertion to the checked-in coverage
  contract.

## Source gates and corrections

Mac execution used the pinned Go 1.27.1 toolchain and shared caches
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`.

| Machine | Required gates | Result |
| --- | --- | --- |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-11 LANES_TIER=H` | PASS — 5 registered assertions |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-10 LANES_TIER=H` | PASS — 6 regression assertions |
| `mac-control-plane` | `make check build` | PASS — complete Go suite, vet, command builds |
| `mac-control-plane` | Focused `go test -race ./src/internal/release -run '^TestLanesL311' -count=1` | PASS |
| `linux-host` | L3-11 H (5), N (1), `make check build`, focused L3-11 race | PASS |
| `sandbox-host` | L3-11 H (5), N (1), `make check build`, focused L3-11 race | PASS |
| `factory-sandbox` | L3-11 H (5), N (1), `make check build` | PASS |
| `factory-sandbox` | Focused race | NOT RUN — Go reports `CGO_ENABLED=0`; no race-capable toolchain |

The Ubuntu N commands set `LANES_NATIVE_HOST_ID` to the executing profile;
the first linux-host N attempt omitted that required identity and correctly
failed closed. The rerun with `LANES_NATIVE_HOST_ID=linux-host` passed. The
same explicit host binding was used for sandbox-host and factory-sandbox.

Focused test iterations exposed and corrected two journal issues: a normal
losing concurrent CAS was incorrectly marking its handle unhealthy, and the
append-only publication check rejected valid incremental per-host receipts.
The ledger fixture clock was also advanced consistently before later
publication receipts. A host-anchor regression initially requested a fresh time
probe after a rejected update; rejected gap/fork inputs leave the existing
accepted probe available, so the test now exercises both refusals before the
valid contiguous update consumes it. All corrected focused and full gates pass.

## Source S delivery and host validation

Source S is `83c74829cdf95ffeca448b77b1e82ff6fc4d8d0f`
(`phase(LANES-L3-11): add bounded authority renewal grants`). The Gitea primary
`origin/dev` and automatic GitHub mirror `github/dev` both resolved directly to
exactly S. Every Ubuntu checkout was clean on `dev` at predecessor
`53cdbdb2f779512d581a38bf7a9edff5ec31dedb` before pull, fast-forwarded from
`origin/dev` using its configured `/home/ubuntu/.ssh/gavrilo123456-github`
identity and strict host-key checking, validated at S, and remained clean:

| Required machine | SSH profile / host | Final state | Validation |
| --- | --- | --- | --- |
| `linux-host` | `remote-session-runner` / `oracle-yuta-konopka-ubuntu-micro-02` | clean `dev`, S | H, N, full tests, vet, build, race PASS |
| `sandbox-host` | `sandbox.env` / `oracle-gustaw-janecki-ubuntu-flex-02` | clean `dev`, S | H, N, full tests, vet, build, race PASS |
| `factory-sandbox` | `factory-sandbox` / `oracle-waclaw-dziwulski-ubuntu-micro-01` | clean `dev`, S | H, N, full tests, vet, build PASS; race NOT RUN as above |

The Mac-side SSH client once emitted a non-fatal known-hosts update-permission
warning for `linux-host`; strict host-key verification and all remote commands
succeeded. Gitea accepted the source push; the local `origin/dev` tracking-ref
write was denied by the managed filesystem, so the primary and mirror were
verified directly with `git ls-remote` instead.

The ordered L3-10 F blocker remains unchanged and this unit is still BLOCKED in
the ordered sequence. This source evidence does not authorize installation,
publisher activity, production key use, live signing, or any service or host
configuration change.
