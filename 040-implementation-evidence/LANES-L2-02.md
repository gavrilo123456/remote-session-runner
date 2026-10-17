# LANES-L2-02 — Dedicated queued SSH listeners and forced wrappers

**State:** PASS — source acceptance only. No queued listener was installed or
started; this does not claim runtime cgroup or live bridge acceptance.

## Predecessor and scope

- Ordered predecessor: L2-01 evidence closeout E `88e44f4666bbc1a706ea5f66710e989d3b3d5803`, clean on Mac `dev`, Gitea, GitHub, and all three Ubuntu `dev` checkouts.
- This unit derives Dev/QAS queued SSH plans for the three canonical Ubuntu profiles. It emits root-owned standalone `sshd_config`, a single restricted dispatcher `authorized_keys` line, a fixed forced wrapper, a root-owned systemd listener unit attached to the lane slice, and a separate Mac per-host/per-lane `known_hosts` entry. It emits public identity material only and never writes files, generates/reads/transports private keys, starts a listener, changes global port 22, or edits `/home/ubuntu/.ssh/authorized_keys`.
- The L2-01 closeout remains delivered. `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED** with their evidence preserved; BUG-022/023 remain deferred. PREP-00 and L0 retain their previous status.
- Tests use generated public test identities and temporary files. No production credentials, mailbox publication, service changes, firewall changes, or production Runner interaction occurred.

## Pre-unit inventory

Pre-unit HEAD was `88e44f4666bbc1a706ea5f66710e989d3b3d5803` on clean Mac `dev`; primary Gitea, automatic GitHub mirror, and the three Ubuntu `dev` checkouts matched it. The L2-02 route and listener requirements, L2-01 plan/role packages, existing namespace and V3 profile validation, current SSH client/authorized-key renderer and forced-wrapper behavior, coverage dispatcher, and ledger were reviewed.

| Read input at pre-unit HEAD | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2be22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `4e0ece8b20d48df786c8af9ae742623c17f39f72` |
| L1 aggregate | `f410d8bfb418bfc6e2f9314ef30ca6d191faf71d` |
| L2-01 predecessor evidence | `88cf6e63c5b4dbf41d65918094e0318f17ae96ab` |
| Lane coverage manifest | `7b6f05f63c089dc4266c034ac363e06dbf271b9e` |
| Lane namespace derivation | `29618b35c8c2c17621334673e5a8a212169c92a9` |
| V3 lane profile validation | `499d48132bf4f60a1ac652a8489dc021c41ada29` |
| Existing SSH authorized-key renderer | `1408b97edb784ee140b851d657e5c752d7694f3e` |
| Existing SSH client | `ceea9666db06b7d9716acb466c7a8facb10b9140` |
| Existing forced SSH wrapper | `4147491aeb9357327ccb3bb53698e30e50e42ce8` |

## Source implementation and proof

Added `BuildQueuedSSHPlan` to `src/internal/laneinstall`:

- Only Dev/QAS and `linux-host`, `sandbox-host`, or `factory-sandbox` are accepted. The plan derives public endpoint and private bind addresses from the closed lane namespace: `129.151.232.40` / `10.0.0.200`, `132.226.205.205` / `10.0.0.14`, and `145.241.255.66` / `10.0.0.30`. Dev uses port `2224`; QAS uses `2225`.
- Each listener has a root-owned independent host-key path and configuration under `/etc/remote-session-runner/ssh/<lane>/`. The server config accepts only the `ubuntu` account and public-key authentication, disables password/PAM/PTY/agent/TCP/stream-local/X11/tunnel forwarding and user startup files, limits sessions and authentication attempts, and uses a fixed `ForceCommand`. No `Include`, SFTP subsystem, global sshd config, port-22 authority file, or PRD lane is emitted.
- The root-owned forced wrapper accepts exactly `runner-ssh-bridge --stdio`, the fixed server-supplied dispatcher fingerprint, and no PTY; it passes only fixed binary, controller-map, and lane socket paths. The public-key file has the existing restricted `sshdeploy.Entry` form. A unique `/run/runner-sshd-<lane>/sshd.pid` avoids sharing the production sshd PID path.
- The systemd listener runs as root, uses `KillMode=control-group`, disables PAM session handling with `UsePAM no`, and attaches to the corresponding `remote-session-runner-<lane>.slice`. The H fixture checks those declarations. It does not observe actual session descendants in a live cgroup; the real `/proc/<pid>/cgroup` acceptance belongs to the planned L2-07 functional gate.
- The Mac `known_hosts` output pins `[public-IP]:port` to the supplied server public key. Per-profile private-key and known-host paths remain separate. Linux V3 profile validation now requires its host key at `/etc/remote-session-runner/ssh/<lane>/ssh_host_ed25519_key`; the previous user service-root path contradicted the design's root-owned lane authority tree. A regression rejects the old path.
- Eight registered H assertions cover the fixed host/lane matrix, invalid hosts/lanes/keys, restricted server settings, forced-command behavior, port-22 and identity separation, lane-slice/PAM declarations, the exact restricted dispatcher key, and the Linux host-key path. One registered N assertion validates Mac OpenSSH trust-file syntax or Linux native `sshd` and systemd configuration.

### Failed gates and corrections

1. Before Source S1, two H assertions had false positives: one matched the test's own comment text, and another treated `Port 2224` as `Port 22`. The checks were narrowed to actual config directives; H then passed.
2. Source S1 `202d4fcdc05d01d24e7d92d18162cf0d04729aae` passed H on all hosts and Mac N, but Linux native N failed because the OpenSSH `.pub` file's terminal newline was rejected. The parser now accepts one terminal LF or CRLF while rejecting additional lines. Source S2 below passed N on every host.
3. A first Linux S2 gate script invoked `jq`, which is not installed on linux-host. No test or source gate failed. The JSON was parsed with `python3 -m json.tool` on all Ubuntu hosts; the Go coverage-loader tests also passed.

## Source S2 and validation

Corrected Source S2 is `6d6171d773ef450447198ec3dad7a48fe2c7d7d7` on Mac `dev`. It follows Source S1 above and includes the public-key newline correction. All final results below use S2.

| Machine | H, 8 assertions | Native N | `make check build` | Focused race | JSON/format/diff | Final checkout |
| --- | --- | --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | PASS | PASS (`ssh-keygen -lf` for generated known_hosts) | PASS | PASS | PASS | `dev`, clean, S2 |
| `linux-host` | PASS | PASS (`sshd -t`, effective `sshd -T`, `systemd-analyze verify`) | PASS | PASS | PASS | `dev`, clean, S2 |
| `sandbox-host` | PASS | PASS (`sshd -t`, effective `sshd -T`, `systemd-analyze verify`) | PASS | PASS | PASS | `dev`, clean, S2 |
| `factory-sandbox` | PASS | PASS (`sshd -t`, effective `sshd -T`, `systemd-analyze verify`) | PASS | NOT RUN: `CGO_ENABLED=0`, no `cc` | PASS | `dev`, clean, S2 |

`make check build` passed the full `go test ./...`, vet, smoke, and command
build gates. The focused race command was:

```sh
GOTOOLCHAIN=local go test -race ./src/internal/laneinstall ./src/internal/config ./src/internal/lanetests -run '^TestLanesL202' -count=1
```

Mac used the pinned Go 1.27.1 and shared
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`. Ubuntu used the pinned Go
1.27.1 toolchain and the selected account's normal caches. Native N generated
ephemeral test host keys under `t.TempDir()` and invoked validation commands
only; it never launched `sshd`.

On all hosts, `gofmt -d`, JSON parsing, and `git diff --check` passed. Ubuntu
JSON parsing used `python3 -m json.tool` because linux-host lacks `jq`.
Factory-sandbox's race test is explicitly **NOT RUN**, not PASS, because its
pinned toolchain reports `CGO_ENABLED=0` and `cc` is unavailable.

## Git delivery and synchronized host validation

Source S1 and S2 were committed on Mac `dev`, pushed to primary Gitea, and
verified against the automatic GitHub mirror. Direct `ls-remote` reads
confirmed both Gitea `origin/dev` and GitHub `github/dev` equal S2 exactly.
Each Ubuntu checkout was clean on `dev` before the S2 pull and fast-forwarded
from GitHub with the explicit `/home/ubuntu/.ssh/gavrilo123456-github` key.
No tracked files were edited or copied on Ubuntu.

| Host | Mac SSH alias / explicit identity | Hostname | Final checkout |
| --- | --- | --- | --- |
| `linux-host` | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | `oracle-yuta-konopka-ubuntu-micro-02` | clean `dev`, S2 |
| `sandbox-host` | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | `oracle-gustaw-janecki-ubuntu-flex-02` | clean `dev`, S2 |
| `factory-sandbox` | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | `oracle-waclaw-dziwulski-ubuntu-micro-01` | clean `dev`, S2 |

SSH used explicit identities, `IdentitiesOnly=yes`, `BatchMode=yes`,
`StrictHostKeyChecking=yes`, and `UpdateHostKeys=no` with the already verified
host keys. The S1/S2 pushes succeeded remotely; the sandbox could not update
its local `refs/remotes/origin/dev` tracking lock, so remote commits were
verified directly with `ls-remote`. The Mac worktree is clean at S2; its local
tracking ref can therefore display stale ahead-count metadata even though
both remote refs match.

## Acceptance and next unit

L2-02 is **PASS** for source acceptance. The documentation-only evidence
closeout E contains this file and the ledger update; that E commit is the
ordered predecessor for `LANES-L2-03`, endpoint and TLS material validation.
No listener was installed, no cgroup descendant was observed, and no live
queued request was accepted.
