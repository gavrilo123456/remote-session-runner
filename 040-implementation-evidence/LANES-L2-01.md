# LANES-L2-01 — Fixed lane plans and management role packages

**State:** PASS — source acceptance only. No Dev/QAS installation, live lane,
management baseline, or PRD acceptance is claimed.

## Predecessor and scope

- Ordered predecessor: L1 aggregate evidence closeout `e52a9501b295777b4a474f494031d06d1097ccc7`, clean on Mac `dev`, primary Gitea, GitHub mirror, and all three Ubuntu `dev` checkouts.
- This unit renders fixed Dev/QAS service plans for the four canonical hosts and defines closed host-management role, sensitive-asset, socket-peer, and protected-state packages. It refuses PRD and unknown hosts. The plans are in-memory descriptions and templates only; this unit installs nothing and creates no service, key, account, socket, mailbox root, external directory, or APFS volume.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED** with their existing evidence preserved. Their maintenance closeout is not a prerequisite for this source unit. BUG-022/023 remain deferred. PREP-00 and L0 retain their prior statuses.
- Tests use isolated temporary fixtures and native validators. No production credential, request/ACK publication, mailbox state, service, host setting, storage, firewall, publisher, or production Runner was used or changed.

## Pre-unit inventory

Pre-unit HEAD was `e52a9501b295777b4a474f494031d06d1097ccc7` on clean Mac `dev`; the primary and mirror refs and all three Ubuntu checkouts matched it. The L2 plan and handbook gates, lane design, L1 aggregate closeout, ledger, native baseline templates, namespace derivation, test dispatcher, and coverage manifest were reviewed.

| Read input at pre-unit HEAD | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `0f271d85c46f7206dfe748e13caca5b4b4a52d42` |
| L1 aggregate closeout | `f410d8bfb418bfc6e2f9314ef30ca6d191faf71d` |
| L2 test coverage manifest | `f8215dcff7da4be2240ac786172ae7b13c0e1498` |
| Makefile | `29a5cf0a1c6874e66d608ca879d960ab363a533d` |
| Lane namespace derivation | `29618b35c8c2c17621334673e5a8a212169c92a9` |
| Lane test dispatcher | `b8b94c45bafb48811b0119bd71fa4186e7ffd18e` |
| Lane test dispatcher tests | `2bb59845c600db8dad878a8050d3bf1151a1d7f6` |
| Existing Mac local LaunchAgent | `12557424cd95a9dbddabba56d50c2c490ae05298` |
| Existing Mac locald LaunchAgent | `d5010810d20819b7d0aa3c3c9a80ef9db733dbf7` |
| Existing Linux `runnerd.service` | `02edf8f1d57d5354825547d0fc17b047dd1c9d84` |
| Existing Linux service entrypoint | `c7a3476ff1ddf325996a9543d18a90b0014a5171` |

## Source implementation and H/N proof

Added `src/internal/laneinstall`:

- `BuildPlan` accepts only `dev` or `qas` and the four canonical host IDs, derives the fixed `config.LaneNamespace`, and renders Mac LaunchAgent or Linux service/slice/entrypoint artifacts with fixed identities and paths. The generated LaunchAgents and systemd services route streams through the log-capture interface while sending direct stdout/stderr to `/dev/null` or `null`. Linux entrypoint checks the fixed account, directory ownership/modes, and required files before executing the lane binary.
- `ManagementPackage` describes the closed Mac/Ubuntu role matrix, host-key and authority-state preservation, private-key reader/owner boundaries, and exact socket peers. `ValidateManagementPackage` rejects any drift from the fixed matrix. Packages do not create the described OS identities or resources.
- Six H assertions cover all host/lane derivations, PRD and unknown-host rejection, fixed service identities and paths, management role matrix, key/socket/state boundaries, and rejection of altered management policy. They check that generated artifacts contain no external mailbox root and no live/destructive setup command.
- One N assertion validates generated Mac plist syntax with `plutil`, or Linux systemd units with `systemd-analyze verify` and the entrypoint with `sh -n`, using temporary fixtures only.
- The lane test dispatcher now permits ordinary full-suite execution without native-host context, but requires an explicit canonical host when the N tier is selected. Its missing-assertion regression now correctly points to the still-unimplemented next unit, L2-02.

### Failed checks and corrections

1. The first focused H run exposed a dispatcher regression that still expected L2-01 to have missing assertions. The expected next uncovered unit was corrected to L2-02.
2. The first full Go suite then exposed that ordinary tests incorrectly required `LANES_NATIVE_HOST_ID`; native validation is now explicit for the N tier and inactive in the normal suite. The full suite passed after the correction.
3. An early external-root assertion treated an empty fixture root as a substring of every artifact. The assertion now tests only non-empty roots; the H suite passed.
4. The first N implementation used `systemd-analyze --root`, which the installed systemd 245 supports only for `cat-config`. The native test was changed to validate the generated temporary unit files directly. The corrected N suite passed on all Linux hosts.
5. A first ad-hoc Mac race invocation hit the restricted default Go build cache (`operation not permitted`) before test execution. It was rerun with the Makefile-shared cache paths and passed.

## Source S and gates

Source S is `6bd44e183b2946f0634614eb4433cec9f85efee4` on Mac `dev`.
The initial source commit `15e22ed0887c51553cc69660bedac9e8434a52d` was corrected by S after the systemd N-gate failure. Both commits were delivered to Gitea and through its automatic mirror; all final validation below used S.

| Machine | L2-01 H | Native L2-01 N | `make check build` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | PASS, 6 assertions | PASS (`plutil`) | PASS | PASS with shared Makefile Go caches | `dev`, clean, S |
| `linux-host` | PASS, 6 assertions | PASS (`systemd-analyze`, `sh -n`) | PASS | PASS | `dev`, clean, S |
| `sandbox-host` | PASS, 6 assertions | PASS (`systemd-analyze`, `sh -n`) | PASS | PASS | `dev`, clean, S |
| `factory-sandbox` | PASS, 6 assertions | PASS (`systemd-analyze`, `sh -n`) | PASS | NOT RUN: pinned Go reports `CGO_ENABLED=0` and `cc` is unavailable | `dev`, clean, S |

On Mac, `make check build` passed the complete test suite, vet, smoke, and
build gates. The focused race command was:

```sh
GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local go test -race ./src/internal/laneinstall ./src/internal/lanetests
```

On Ubuntu hosts, `make check build` and the same focused package race test
passed on linux-host and sandbox-host. Factory-sandbox completed the H, N,
full, vet, smoke, and build gates; its race test is explicitly **NOT RUN** for
the toolchain/compiler limitation above. On all four machines,
`gofmt`, `jq empty 040-implementation-evidence/LANES-test-coverage.json`, and
`git diff --check` passed.

Native H and N commands were run serially on each machine:

```sh
make test-lanes UNIT=LANES-L2-01 LANES_TIER=H
LANES_NATIVE_HOST_ID=<canonical-host-id> make test-lanes UNIT=LANES-L2-01 LANES_TIER=N
```

The Mac used the shared Go caches and pinned Go 1.27.1 from the Makefile. Each
Ubuntu host used its pinned Go 1.27.1 toolchain and normal account caches.

## Git delivery and synchronized host validation

At S, the Mac `dev` checkout was clean. Explicit-key pushes delivered S to
primary Gitea `origin/dev`; direct `ls-remote` verification confirmed both
`origin/dev` and the automatic `github/dev` mirror at exactly
`6bd44e183b2946f0634614eb4433cec9f85efee4`. The mirror was not pushed
manually. Each Ubuntu checkout was confirmed clean on `dev` before its
fast-forward pull from GitHub, then tested at S. Current read-only rechecks
confirmed all three remain clean at S and both remote refs still equal S.

| Host | Mac SSH alias / explicit identity | Hostname | Final checkout |
| --- | --- | --- | --- |
| `linux-host` | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | `oracle-yuta-konopka-ubuntu-micro-02` | clean `dev`, S |
| `sandbox-host` | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | `oracle-gustaw-janecki-ubuntu-flex-02` | clean `dev`, S |
| `factory-sandbox` | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | `oracle-waclaw-dziwulski-ubuntu-micro-01` | clean `dev`, S |

SSH used `IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`,
and `UpdateHostKeys=no` with the already verified host keys. Ubuntu Git pulls
used `/home/ubuntu/.ssh/gavrilo123456-github` and `--ff-only`. No tracked
source was edited or copied on Ubuntu; all changes arrived through Git.

Direct SSH was used because these commands validate synchronized source
checkouts and do not publish through the paused Runner mailbox. The source
tests use isolated fixtures and do not touch the unsuffixed production Runner
or any installed service.

## Acceptance and next unit

This source unit is **PASS**. Its evidence closeout is a documentation-only
commit containing this file and the L2-01 ledger update; that closeout commit
is the ordered predecessor for L2-02. The next unit is `LANES-L2-02`, dedicated
queued SSH listeners and forced wrappers. No later L2 gate is claimed by this
closeout.
