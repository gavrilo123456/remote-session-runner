# LANES-L3-14 — authority and host capacity-policy integration

**State:** IN PROGRESS; the unit remains BLOCKED in the ordered sequence.
The four registered H assertions and Mac source gates pass. Post-source
validation on the three Ubuntu hosts and required physical F acceptance are
not yet complete. No unit PASS or evidence-closeout E is claimed.

## Pre-unit state, predecessor, and scope

- Pre-unit Mac `dev` HEAD: `a6412be9640fb15c130c4cd7bf49833b9f7c16fc`, clean.
- Ordered predecessor L3-13 remains BLOCKED because L3-12 F is NOT RUN. Its
  evidence at pre-unit HEAD is `LANES-L3-13.md`, blob
  `33e285a92d39a10035589c2ec23f9de150ee253a`; underlying L3-12 blocker
  evidence is `LANES-L3-12.md`, blob
  `a7b7667fdd93bbece6c1e558fd193fb4059334d2`. L3-13 source S2
  `551e9b550ae630fdaed7b6d987df7cf4d76d596c` and its source gates passed, but
  it has no ordered PASS/E. The owner's incremental-readiness authorization
  permits this isolated source work without upgrading either predecessor.
- The separate required L3-14 F gate depends on actual approved capacity
  fixtures. L2-07 remains BLOCKED: no A01 Linux data/backup devices or measured
  device performance; `linux-host` hybrid-cgroup compatibility and
  `factory-sandbox` I/O-controller propagation remain unresolved. Its current
  evidence blob is `4b6555ec34039b13b2846d46891d6750850f0a36`. L2-08 remains
  BLOCKED: no approved disposable Mac APFS lane/cache/backup fixtures; current
  evidence blob is `871fea76b91ed41f0536b89886c4c6942980e929`. Neither F gate
  has passed.
- B021/B024 remain BLOCKED with their existing evidence unchanged; BUG-022/023
  remain deferred. The maintenance snapshot was not rebuilt. No production
  credentials, mailbox requests/ACKs, services, host configuration, storage,
  firewall, boot state, or installed files were changed. Tests use generated
  signing keys and temporary SQLite anchor fixtures only.
- This source S is the commit containing the implementation and this
  in-progress evidence. Its exact SHA, primary/mirror delivery, and remote
  validation will be recorded by a later evidence update; the source commit
  does not embed its own SHA.

## Inputs read at pre-unit HEAD

The full lane idea, design, plan, handbook, original plan, repository
instructions, ledger, predecessor evidence, relevant source/tests and build
configuration were freshly read before implementation. Their pre-unit Git
blobs are:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original PoC detailed design source | `5659973dddaf886c9e872b24c2702dc8ae64d34e` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `70d104686956a9029f3f28e3703efa8931352860` |
| L3-13 predecessor evidence | `33e285a92d39a10035589c2ec23f9de150ee253a` |
| L3-12 blocker evidence | `a7b7667fdd93bbece6c1e558fd193fb4059334d2` |
| L2-07 blocker evidence | `4b6555ec34039b13b2846d46891d6750850f0a36` |
| L2-08 blocker evidence | `871fea76b91ed41f0536b89886c4c6942980e929` |
| L2-06 capacity source predecessor | `42e04f613dddd7c94007f2716a318c19f7e8238e` |
| PREP-00 readiness evidence | `843ea1b986da917f5b831e66a2ccc93826d376ba` |
| Makefile / `go.mod` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` / `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |
| Lane coverage manifest / dispatcher manifest test | `55fc34d784843d2ef73efd6314e6ab47c2afaf89` / `06357468d093daae53368923212ab6ebe1369027` |
| `lanecapacity/policy.go` / policy tests | `fab5a8c899bdbdd8f44938f3c2aa77ab5fe89e7a` / `ec73da30a05416487c2064d1805f4e1ad89c30e1` |
| `lanecapacity/linux_controls.go` | `7bba518ff1cd3ab4af0b22b10afbdc5c89766a87` |
| release payload validator / L3-03 tests | `0288399e3119d52f73f3ba7d9fbcb71a3759e2af` / `9905ae2f5162a003e721d0b01d19d1d654e61bc3` |
| capacity policy / L3 payload schemas | `9c7f46264cb43675abd12070cbf1c68c043a5fe0` / `346cd46358e3468917effbca97ff62a206a34be0` |
| release envelope / trust | `31a032fc8b7e726c681b2f2d295f556a9d655653` / `e463407256c6ea710dd663a33d8021dea6a5e80b` |
| security anchor / L3-05 tests | `5498d8abbd9dbd1b1c9ad12ffae5d6112458a149` / `0a57a8e164cd185d193415aab64eefc93d198ba8` |
| authority signer / journal | `368657abfe81f7cddc0178f0a934a7f9348a6891` / `b5fe239ab30cc1fa653536c087c0ae706b459e2c` |
| host attestation / verified release cache | `e97d17e626af0a30283c0b9f27bf9c77f3f93f93` / `63e1fae96a6dae194f223cf6b4f34ed053f829af` |
| installer preflight / Linux controls / Mac controls | `61e015241fccb31c2f78fe7711320d475590c0db` / `9e54f99be41a752e2f568231ae0785448e900719` / `a506c314a3a58c0ef7a28cd01852ab1f4024b9f5` |
| B021-02 / B024-02 retained evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` / `15211861260284d0923c9d64259bd7746700399e` |

Execution-machine scope: `mac-control-plane` source and fixture tests; after S
delivery, `linux-host`, `sandbox-host`, and `factory-sandbox` source checks on
their clean synchronized Git checkouts. No live host capacity probe or F
operation was attempted.

## Source contract implemented

- Adds an anchor-owned `VerifyCapacityPolicy` path. It uses the helper's
  accepted authority time, current non-revoked authority and host-attestation
  keys, and a fresh revocation ledger to verify the authority-signed policy
  envelope and the same-host host-signed capacity-measurement envelope.
- It rejects cross-host, wrong-type, revoked, tampered, stale, and incorrectly
  linked records before observation. It checks the policy once against the
  signed measurement, requests a fresh snapshot through the fixed
  `CapacityPolicyObserver`, rechecks the exact same capacity equations and
  identities against that live snapshot, and refuses if anchor state changed
  during observation. The returned result is short-lived and non-persistent;
  it does not install policy or change services.
- `ValidateCapacityPolicyAgainstLiveObservation` checks host/scope, exact
  signed measurement-record ID and evaluation digest, time validity, unchanged
  resource capacity, Linux quota-device identity and fixed cgroup-v2 policy
  mode, cache paths,
  backup identity, APFS volume/quota identity and all existing byte/inode,
  cache, backup low-water, snapshot, and PRD-floor equations.
- Corrects an identifier conflation found in the first H run. The internal
  `Measurement.ID` identifies the local measurement body, while
  `Policy.MeasurementID` identifies the signed envelope. The release validator
  binds the latter to the exact envelope record ID; the low-level evaluator
  checks that it is syntactically valid and evaluates the embedded measurement
  digest. Treating them as the same value would make the record hash depend on
  itself.
- Four H assertions cover both Linux scopes through signed anchor verification,
  a mismatched measurement-envelope ID, changed resource/quota-device/cache/
  backup identity and stale/low-water refusals, plus both Mac scopes with APFS
  quota/cache/backup/snapshot/free-floor regressions. They are synthetic
  hermetic fixtures, not physical acceptance evidence.

The implementation does not persist a policy or create a service integration
call site. The protected helper must use this verification result immediately
before an approved operation. F still must prove real cgroup membership,
controller propagation, quota/device identity, low-water refusal and continued
PRD behavior on approved fixtures.

## Source revision and Mac gates

The first H run exposed the measurement-ID conflation described above; that
was corrected before source commit, and all four registered H assertions were
rerun successfully. No intermediate source revision is treated as delivered.

| Machine | Command | Result |
| --- | --- | --- |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-14 LANES_TIER=H` | PASS — four registered assertions |
| `mac-control-plane` | `make check build` | PASS — full Go suite, `go vet ./...`, and command builds |
| `mac-control-plane` | `make smoke` | PASS |
| `mac-control-plane` | focused `go test -race` for release/capacity/manifest packages | PASS after rerunning with the Makefile shared cache; first invocation fell back to the restricted default Go cache and failed before tests started |
| `mac-control-plane` | `git diff --check`; JSON parse of `LANES-test-coverage.json` | PASS |

Exact focused race command:

```sh
GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release ./src/internal/lanecapacity ./src/internal/lanetests -run 'TestLanesL314|TestLanesL303CapacityPolicy|TestLanesL303MacCapacityPolicy' -count=1
```

The complete source check was `make check build`; native H used `make
test-lanes UNIT=LANES-L3-14 LANES_TIER=H`; the smoke target was `make smoke`.
All used Go 1.27.1 from the Makefile-pinned toolchain and the shared Mac
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

## Remaining delivery and acceptance gates

At this evidence revision, the post-S Gitea push, exact automatic GitHub
mirror verification, three Ubuntu fast-forward pulls and source checks are
NOT RUN. Required order remains Mac Source S → Gitea primary → exact GitHub
mirror SHA → clean Ubuntu `dev` fast-forwards → exact-S validation. No tracked
source is copied or edited on Ubuntu.

Before S delivery, all remote development checkouts were read-only checked on
`dev`, clean, and at predecessor `a6412be9640fb15c130c4cd7bf49833b9f7c16fc`:

| Machine | SSH identity | Pre-source checkout |
| --- | --- | --- |
| `linux-host` (`129.151.232.40`) | `/Users/tomasz.walczuk/.ssh/remote-session-runner` | `dev`, clean, predecessor SHA |
| `sandbox-host` (`132.226.205.205`) | `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | `dev`, clean, predecessor SHA |
| `factory-sandbox` (`145.241.255.66`) | `/Users/tomasz.walczuk/.ssh/factory-sandbox` | `dev`, clean, predecessor SHA |

The first SSH-alias check for `linux-host` and `sandbox-host` failed before
connecting because those aliases do not resolve in this Mac's SSH config. The
checks were retried against their pinned IPs with `StrictHostKeyChecking=yes`
and the listed identities. The successful `linux-host` check emitted an
`UpdateHostKeys` write warning after verification; the remote Git state read
completed successfully. `factory-sandbox` resolved by its configured alias.

The required L3-14 F gate is NOT RUN. It requires the blocked L2-07 A01 Linux
fixtures/controller prerequisites and L2-08 disposable APFS fixtures listed
above. No capacity values or F result are inferred from synthetic tests. Keep
L3-14 BLOCKED until the actual dependencies pass; record the exact remaining
gate after post-S source validation. B021/B024 evidence stays BLOCKED and
BUG-022/023 stays deferred.
