# LANES-L3-16 — management update/rollback and PKI rotation

**State:** IN PROGRESS. The registered H source assertions and full source
gates pass at Source S. Required physical F is NOT RUN because the ordered
L3-15 F predecessor is still blocked behind L3-14 F. This evidence does not
claim L3-14, L3-15, or L3-16 PASS and does not claim a live host-management
update, PKI rotation, installation, or service acceptance.

## Pre-unit revision, predecessor, and scope

- Mac `dev` was clean at pre-unit HEAD
  `ceb1c3847034cfbe6c67eeeb90a77e7330081957`.
- The immediate ordered source predecessor is L3-15 Source S
  `c9bd10dad99ad9f233a17ecbb1f2db0d5ef391a3`, delivered with evidence E at
  `ceb1c3847034cfbe6c67eeeb90a77e7330081957`. Its evidence blob at the
  pre-unit revision is `1506d98d46ad84a610e593e6d9173965eacda7c8`. L3-15 remains
  IN PROGRESS because its ordered physical F was not run; it is not treated as
  a PASS prerequisite for this independent source work.
- The existing ordered F blocker is L3-14 F, NOT RUN, on the missing L2-07
  approved Linux fixture/controller prerequisites and L2-08 approved
  disposable Mac APFS fixtures. The retained evidence blobs are
  `0dae129af6ac63a7c7fb863cd50194801fecf1bd` (L3-14),
  `4b6555ec34039b13b2846d46891d6750850f0a36` (L2-07), and
  `871fea76b91ed41f0536b89886c4c6942980e929` (L2-08). No capacity or physical
  control result is inferred from this unit's test fixtures.
- The owner's incremental-readiness decision permits this independent
  hermetic source unit to proceed while those future F inputs are unavailable.
  L3-16 F remains unstarted until its ordered predecessor gates pass and its
  own approved host-maintenance fixtures/window are available.
- `LANES-B021-02` and `LANES-B024-02` remain BLOCKED; their evidence was not
  changed. BUG-022/023 baseline repair remains DEFERRED. The earlier B021
  maintenance package remains a snapshot and was not rebuilt. No mailbox
  request or ACK was published; no service, publisher, credential,
  installation, storage, firewall, boot setting, or retained work was changed.
  Tests use isolated generated state and test identities only.

## Inputs reviewed at pre-unit HEAD

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased implementation plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `a66d887cfdf13cdbb1d1b0c33a987f8339397c07` |
| Test coverage manifest | `0cc53a3d5f21bf65f39544ec96f3a9e1b823cd7b` |
| L3-15 predecessor evidence | `1506d98d46ad84a610e593e6d9173965eacda7c8` |
| L3-14 / L2-07 / L2-08 blocker evidence | `0dae129af6ac63a7c7fb863cd50194801fecf1bd` / `4b6555ec34039b13b2846d46891d6750850f0a36` / `871fea76b91ed41f0536b89886c4c6942980e929` |
| B021-02 / B024-02 retained evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` / `15211861260284d0923c9d64259bd7746700399e` |
| Existing PKI implementation / payload validator / L3 payload / authority signer | `775cbd3ed4c0c9be3f08a2942d192dcea801447b` / `20602b239115a11034bb4326862f27b37d44ec5b` / `3d851a93aa174c7b94b9a328818005e4bfce1b43` / `368657abfe81f7cddc0178f0a934a7f9348a6891` |

Execution scope was source and isolated fixture testing on `mac-control-plane`,
then validation from synchronized Git checkouts on `linux-host`, `sandbox-host`,
and `factory-sandbox`. No privileged F operation was attempted.

## Source contract implemented

- Adds a closed `host_management_authorization` V1 schema for the exact
  host-specific artifact subset, capacity policy, installation generation,
  bootstrap-or-installed predecessor, rollback artifact, ledger generation,
  time interval, and optional PKI rotation bindings. Authority signer context
  includes those predecessor and PKI receipt/package/artifact-tuple fields.
- Adds cross-record validation binding authorization to the verified
  host-management build matrix, exact per-host artifact tuples, measured
  host-management capacity policy, current ledger/time, and authenticated local
  bootstrap or installed anchor state. PKI receipt, policy, measurement,
  manifest, and authorization signatures must be verified by their trust
  layers before this semantic validator is used for installation.
- Adds a host update guard requiring a fresh ledger, the exact installed
  manifest/peer-set/rollback identities, all three lane directives with at
  least 900 seconds remaining, a fixed exact component set, and an update plus
  verified rollback budget no greater than 600 seconds. The staged flow
  snapshots authority state/public anchor, security-anchor state, lane roots,
  processes, configuration, mailboxes, and ingress. It commits only after
  health/time and unchanged-state checks; failures roll back and independently
  verify the prior state. If an operation cannot fit, the caller must obtain a
  planned signed lane closure before it starts.
- Adds serial release-transport PKI rotation for the fixed four-host order.
  It binds unique request/receipt/package/peer-set identities and rollback
  material, bounds overlap to 14 days, installs dual trust and the exact
  old/new peer pair before changing the leaf, and checks the new mTLS/time
  path while retaining the old working chain. Before retirement it refreshes
  the all-host guards and protected-state snapshots against the exact overlap
  manifest and peer sets. It verifies old-leaf denial at every endpoint before
  recording revocation or removing old leaf material. Failed overlap restores
  and verifies the prior chain.
- Adds a manifest transition validator that requires exact CA/trust/leaf/peer
  inventory changes for overlap and retirement, with no unrelated trust or
  retained old-leaf entries.
- Eight registered H assertions cover guard margin and time bounds, exact
  predecessor checks, successful update ordering, verified and failed
  rollback, serial overlap, all-host retirement ordering, refusal on old-leaf
  denial failure, exact transport manifest transitions, and authorization
  binding to capacity/build/predecessor records. All use generated test keys
  and temporary fixtures. They are not live host acceptance.
- The source exposes narrow backend interfaces; these tests use isolated
  backends. This H result does not prove that a protected host helper is
  installed or that a production backend is operating on a real host. That
  remains part of physical F and later approved host installation.

## Source revision and Mac gates

Source S is `ef3e96a10eab52c0eb4bda691b8c19990d502b20`, parent
`ceb1c3847034cfbe6c67eeeb90a77e7330081957`.

| Machine | Command | Result |
| --- | --- | --- |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-16 LANES_TIER=H` | PASS — eight registered assertions |
| `mac-control-plane` | `make check build` | PASS — full Go suite, vet, smoke, command builds |
| `mac-control-plane` | Focused `go test -race ./src/internal/laneinstall ./src/internal/release -run 'TestLanesL316' -count=1` | PASS |
| `mac-control-plane` | Coverage JSON parse and `git diff --check` | PASS |

The Mac used Go 1.27.1 from the Makefile-pinned toolchain and the shared
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

## Git delivery and post-S validation

| Gate | Command or check | Result |
| --- | --- | --- |
| Mac primary | Explicit-key `git push origin dev` using `/Users/tomasz.walczuk/.ssh/local-gitea-key` | PASS — Gitea `origin/dev` is S |
| Mac mirror | Explicit-key `git ls-remote github refs/heads/dev` using `/Users/tomasz.walczuk/.ssh/gavrilo123456-github` | PASS — exact SHA S |
| `linux-host` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; H, full source gates, smoke, focused race | PASS at S after retry described below |
| `sandbox-host` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; H, full source gates, smoke, focused race | PASS at S |
| `factory-sandbox` | Clean `dev` at predecessor; GitHub `origin/dev` `--ff-only` pull; H, full source gates, smoke | PASS at S; race NOT RUN because `cc` is unavailable (`CGO_ENABLED=1`) |

All three Ubuntu checkouts were clean on `dev` at predecessor
`ceb1c3847034cfbe6c67eeeb90a77e7330081957`, then fast-forwarded to S and left
clean at S. SSH used the configured explicit host identities and strict host
key checking. The pulls used each host's
`/home/ubuntu/.ssh/gavrilo123456-github`; no tracked file was edited or copied
outside Git. All hosts used the Makefile-pinned Go 1.27.1 toolchain.

The first Linux `make check build` attempt failed once in the existing
`TestLanesL311LedgerGrantSerializesCasAndPreservesRevocationSets` while
snapshotting its concurrent journal fixture (`release authority state
unavailable`). The isolated test passed 10 consecutive runs, and a full
`make check build` retry passed; no source change was made for that nonrepeating
failure. Linux focused race passed. Sandbox full checks and focused race passed.
Factory full checks passed; its race was not attempted without a C compiler.

## Ordered F gate and current disposition

L3-16 physical F remains NOT RUN. L3-15 F remains NOT RUN, so the serial
sequence has not reached L3-16 F. L3-15 is itself blocked by L3-14 F; L3-14
requires the retained L2-07 approved Linux capacity/controller fixtures and
L2-08 disposable Mac APFS fixtures referenced above. No credentials, publisher
pause, maintenance window, host services, production storage, or installed PKI
material were requested or changed for this source unit.

After evidence delivery, continue the next independent source unit in the
handbook order without rebuilding the historical B021 maintenance snapshot.
Rebuild and freeze that maintenance package only when publisher pause and a
valid maintenance window are available, against the then-selected synchronized
revision, as the owner directed.
