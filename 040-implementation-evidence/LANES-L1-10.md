# LANES-L1-10 — provisional ingress admission contexts

**State:** PASS — Source S and all required synchronized-host gates passed.
This document is the evidence-only closeout E; its commit does not embed its
own SHA. No installed or live lane acceptance is claimed.

## Predecessor and pre-unit read inventory

- Ordered predecessor: `LANES-L1-09` evidence closeout E at
  `1be94391806503c8239c93143e8d12e98bdb9619`.
- The earlier L1-02 predecessor `b25468d` is historical and already delivered;
  it does not replace L1-09 as the immediate predecessor of this unit.
- Pre-unit branch was `dev`, with HEAD at E. Direct `ls-remote` checks showed
  Gitea `origin/dev` and GitHub `github/dev` both at E. The local
  `origin/dev` tracking ref lagged and displayed `ahead 4`; it was not treated
  as remote authority.
- All three Ubuntu checkouts were clean on `dev` at exact E before this
  source work. After source delivery they must be rechecked clean at E before
  `--ff-only` pulls.
- Fresh review covered repo instructions, lane and original designs/plans,
  handbook, ledger, L1-09 predecessor, B021/B024 blocker records, coverage
  dispatcher, store acceptance/migration paths, direct/private APIs, and
  existing test conventions. Pre-unit blob IDs:

| Read input | Blob at pre-unit E |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| Original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| Original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| Execution ledger | `23c49d8690f45ce838bcaf0fc2ef94047b851199` |
| L1-09 predecessor evidence | `aa667e63ca7623ed5ee26bd0facbbf633214524e` |
| B021-02 blocker evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| B024-02 blocker evidence | `15211861260284d0923c9d64259bd7746700399e` |
| H coverage manifest | `af7714080d3a48283d16b9d949c77d4d16abf218` |
| Test dispatcher | `b8b94c45bafb48811b0119bd71fa4186e7ffd18e` |
| Dispatcher tests | `d34b0496f371ed0e479681a6df5448f8925792b8` |
| Store schema/migration runner | `77c52a2ecb63818ef8065443b571efd594ce2f44` |
| Store sessions | `8f17b0717aeb2c3953cf38af01ebda28b6acb72c` |
| Store commands | `031847f65ae1ad39eeac1c24c4714b60842868aa` |
| Store jobs | `a1bff3ef636d999d7a7af101fd7b63f2258cf347` |
| Execution service | `fd3ccc4af9114d611209c4defe27c73fd7f54899` |
| Private runner API | `99572fb8c8b2663ce99708237ad33e8016b7b0c9` |
| Direct HTTPS API | `0b2549b5fa780b5e966608f10191c22823db4301` |
| SSH bridge forwarder | `8530d8af5115dc8f29519c188b51be9b08f987ca` |
| Lane registry | `b9c3ddcfa077d1babec3c9a388c6f6a2fa05d24b` |
| Mailbox fence | `8fd007648299833be51bf55e213d579de00f7149` |
| Direct API test helpers | `a88909c8133653ec5df5e26ebf764c28f2dd6e9c` |
| Private API test helpers | `962c9e52bf0ee346933d4da696f33af0aef10954` |
| Prior migration 0037 | `a7ac80877217c4cfb27da964247427beed1fe0cc` |
| Existing-schema online-open fixture | `f9eb74aca59bd8d5a73fc628ff8a54393b80d79b` |
| Read-only-open fixture | `1da9d9444870087e1d34fb228b0940b47d246878` |
| Teardown-recovery migration fixture | `9dbc43ac62c2e6d14ae9ab61525afcbc788cce39` |
| Controlled-restart status fixture | `9da447ca328c7b1a33edbfaedfc7a41c97a122c7` |
| Mailbox-retirement migration fixture | `b35aad301cbe65cc2c491714c82ec57a833c20ac` |
| P154 metrics fixture | `d64641afa3961fc753fa8269091f70fef4d1fa9c` |
| P164 mailbox-ingress fixture | `65014e8973ddbf8a5181b9536b9dddffe7f5429e` |

## Scope and implementation

Implement the provisional host-local admission state required by phased-plan
L1 item 7 and handbook unit `LANES-L1-10`. It is not signed enforcement and
does not install or change any lane service.

- Added typed lane/host/transition/candidate/configuration/role/epoch identity
  contexts; monotonic per-host epoch checks; exact-identity idempotence;
  `active_runtime_closure` and `candidate_admission`; closed candidate staging,
  pointer switch and `closed → transport_ready → open`; and durable
  `fencing/fenced/aborting/aborted` migration states.
- Added SQLite schema 38 with a canonical local-state row and SHA-256
  integrity check. Added the known schema-37 migration predecessor to read-only
  restart/maintenance preflights and corrected historical fixture shapes so
  schema-37/older fixtures do not accidentally claim schema-38 tables.
- Session creation, command acceptance, and queued-job acceptance check the
  gate in the same `BEGIN IMMEDIATE` transaction that persists work. Direct
  HTTPS and private/forced-wrapper create, submit and run therefore share the
  transactional gate.
- Added a store `RWMutex` guard for direct/private cancel and close requests.
  Admission closure/fence updates take the exclusive side; already admitted
  lifecycle work may complete before closure commits, while later calls fail
  closed with HTTP 423. Internal shutdown/cleanup calls remain able to drain
  work after ingress closes. The guard is provisional process-local
  coordination; later signed L3 enforcement and all Mac/local-intent/mailbox
  importer and ACK surfaces remain assigned to L1-11/L3.
- A newly constructed process remains closed even when the persisted candidate
  row was open. It must revalidate the exact candidate identity before work
  is accepted. No production credential or trust anchor is used.
- Eight registered H assertions cover the state machine, migration fence,
  transaction gates, restart, exact identity, corrupt state, schema-37
  migration, concurrent closure, and direct/private cancel/close rejection.
  Fixtures use temporary databases and test-only identities.

## Mac source gates

Commands ran on `mac-control-plane` (Darwin arm64), Go 1.27.1, using the
Makefile's shared Go caches and explicit toolchain path:

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-10 LANES_TIER=H` | PASS — 8 registered assertions |
| `make test` with restricted process sandbox | FAIL — `runnerlocald` process-integration tests could not use sandbox-blocked process inspection; no source failure was established |
| `make test` with host process inspection enabled | PASS — full `go test ./...` |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| focused race: laneadmission, store, runnerd; `-run '^TestLanesL110' -count=1` | PASS |
| explicit Go 1.27.1 `gofmt`; `git diff --check` | PASS |

The initial restricted full-suite failure is retained. A direct `ps` diagnostic
was itself denied by the restricted shell (`operation not permitted`); the
full suite passed with host process inspection enabled. The shell PATH does
not supply the required Go tools, so the Makefile's explicit Go 1.27.1
toolchain was used. The linker emitted a duplicate `-lproc` warning for test
binaries; it did not affect the passing gates.

## Source S delivery and synchronized-host validation

- Pre-unit E: `1be94391806503c8239c93143e8d12e98bdb9619`.
- Source S: `10864883d428c7229740fe467b0b36c8301d2f4e`, committed on Mac `dev`
  as `phase(LANES-L1-10): provisional ingress admission contexts`.
- Primary Gitea `origin/dev` accepted S. Direct primary `ls-remote` returned
  exact S. The automatic GitHub mirror `github/dev` was initially still at E;
  after it caught up, direct mirror `ls-remote` returned exact S. No manual
  mirror push occurred.
- Before pulling, each Ubuntu checkout was clean on `dev` at E and its `origin`
  was `git@github.com:gavrilo123456/remote-session-runner.git`. Each ran the
  required GitHub `origin/dev` `--ff-only` pull using the host's explicit
  `/home/ubuntu/.ssh/gavrilo123456-github` identity. Each post-pull and
  post-validation checkout was clean on `dev` at exact S.

The delivery commands were the repository's explicit-key commands:

```sh
# Mac
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev

# Each Ubuntu host, after verifying a clean dev checkout at E
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

| Machine | Platform / Go | Checkout and gates at S | Focused race |
| --- | --- | --- | --- |
| `mac-control-plane` | Darwin arm64 / Go 1.27.1 | PASS — H (8), full `make test`, `make vet`, `make build`, `make smoke`, `git diff --check` | PASS — `laneadmission`, `store`, `runnerd`, `-run '^TestLanesL110' -count=1` |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | Linux amd64 / Go 1.27.1 | PASS — clean `dev`, exact S; H (8), full `make test`, vet, build, smoke | PASS — same three packages and test filter |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | Linux arm64 / Go 1.27.1 | PASS — clean `dev`, exact S; H (8), full `make test`, vet, build, smoke | PASS — same three packages and test filter |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | Linux amd64 / Go 1.27.1 | PASS — clean `dev`, exact S; H (8), full `make test`, vet, build, smoke | NOT RUN — `CGO_ENABLED=0`; `command -v cc` found no compiler |

The required host commands were `make test-lanes UNIT=LANES-L1-10
LANES_TIER=H`, `make test`, `make vet`, `make build`, and `make smoke`. Focused
race used the explicit Go 1.27.1 toolchain on Mac, linux-host and sandbox-host.
The race invocation was `GOTOOLCHAIN=local <go1.27.1>/go test -race
./src/internal/laneadmission ./src/internal/store ./src/internal/runnerd -run
'^TestLanesL110' -count=1`, with the Makefile shared Go caches on Mac and each
Ubuntu account's normal Go caches. The Mac SSH client printed a non-fatal
warning that its sandboxed `known_hosts` file could not be rewritten; all
connections completed with strict host-key checking.
Ubuntu SSH used verified host aliases and explicit identities: Mac alias
`remote-session-runner` with `/Users/tomasz.walczuk/.ssh/remote-session-runner`,
`sandbox.env` with `/Users/tomasz.walczuk/.ssh/dev.slidestud.io`, and
`factory-sandbox` with `/Users/tomasz.walczuk/.ssh/factory-sandbox`; all used
`IdentitiesOnly=yes`, `BatchMode=yes`, and `StrictHostKeyChecking=yes`.

The evidence-only closeout E commit follows S on the same Gitea → exact mirror
→ three-host clean fast-forward path. Its final Git SHA is deliberately not
embedded in this document.

## Rollback and production impact

There is no installed lane state to roll back. If source S needs correction,
deliver a reviewed successor commit through the same Gitea, exact-mirror and
clean-host fast-forward process; do not reset or force-push shared history.
No service, publisher, mailbox, credential, storage, firewall or production
Runner action was taken, so no live rollback or external approval was required.

- No credential, mailbox request/ACK, production service, publisher pause,
  installer, runtime configuration, storage or firewall was changed.
- `LANES-PREP-00` and L0 remain BLOCKED; B021-02 and B024-02 remain BLOCKED;
  BUG-022/023 baseline units remain DEFERRED. Their evidence is preserved.
  None blocks this isolated source unit. No maintenance package was rebuilt;
  the existing package remains a historical snapshot.
