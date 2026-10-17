# SIMPLE-01 — existing-host inventory and reusable-code audit

**State: PASS — inventory and source audit complete.** This is a read-only
SIMPLE-01 result. It does not install Dev/QAS, certify a physical test gate,
change PRD, or claim three-lane acceptance. All measured service identities
below are separate from the source checkout revision.

## Scope and method

The active scope is the existing Mac plus `linux-host`, `sandbox-host`, and
`factory-sandbox`, using existing accounts, disks, and filesystems. PRD remains
unsuffixed and installed as observed. No volume, filesystem, service,
configuration, credential, mailbox, request, or ACK was changed. The retained
B021/B024 evidence and held ACKs were not opened or modified.

At 2026-10-17 20:53–20:57 UTC, read-only Mac shell and SSH observations were
used to inspect mounts, memory, installed paths/modes/sizes, service state,
listeners, and aggregate health metrics. SSH used the configured explicit
identities with batch mode and strict host-key checking. This fallback was
needed because systemd, mount/resource facts, and local Unix-socket health are
host-local observations that mailbox requests would publish as new Runner work.
Health was read with `GET /health/ready` over each existing local Unix socket;
only aggregate activity counters and build identity were retained here. No
mailbox files were enumerated or read.

Source checkout `dev` was clean at `f487dd1ed8fb8afe83d86317fb130fed0a6107bc`.
The primary Gitea `origin/dev` and automatic GitHub `github/dev` both resolved
to that exact SHA. All three Ubuntu source checkouts were clean on `dev` at the
same SHA.
SSH fallback used these read-only host identities; private key contents were
not read: `linux-host` via
`/Users/tomasz.walczuk/.ssh/remote-session-runner`, `sandbox-host` via
`/Users/tomasz.walczuk/.ssh/dev.slidestud.io`, and `factory-sandbox` via
`/Users/tomasz.walczuk/.ssh/factory-sandbox`.

The requested instructions were read from that predecessor revision:

| Input | Blob at `f487dd1` |
| --- | --- |
| `AGENTS.md` | `a08a459db6167032a716b51a254110574cbf1faf` |
| Active existing-host scope (`025`) | `0ef25adc0b1ffb5f0ba083a852f12b25d786e02c` |
| Active phased plan (`015`) | `6330312e343ec436a4f41f8c45ed436ddd666d1b` |
| Active execution handbook (`020`) | `9592dcb2d36403a42b20791ee5429349cf063e25` |
| Execution ledger | `e639ca00d2c8d3e700baaaeaecd48929bcbdfbe0` |

## Installed PRD identities and current activity

All three Ubuntu `runnerd.service` units were active and healthy on HTTPS 8443.
Their current binaries report source revision
`2802f00c93eb51a80f23b9380238c309545d6329`, which differs from the current
source checkout. The Mac local and locald health sockets both reported
`live/ready` at source revision
`77910358a149d6e8fcdf4694498ebdf6c1f07f58`. The installed Mac LaunchAgent
plists point at the unsuffixed root and `config/mac.yaml`; their labels were
not listed by `launchctl list` in this shell's service domain, so I did not
reload or otherwise alter them.
The bounded `runner-log-capture` executable is available in source but was not
present in any of the four installed roots; Dev/QAS bundles must include it to
meet the active per-component log-rotation contract without changing PRD.

| Host | Installed PRD identity | Installed component SHA-256 | Direct listener | Health activity: sessions / commands / queued / mailbox backlog |
| --- | --- | --- | --- | --- |
| Mac (`mac-control-plane`) | `RemoteSessionRunner`; Go 1.27.1 `darwin/arm64`; Mac config schema v2; local + locald live/ready | `runner` `9fc0af3f8439451f0e8ed570be9462b93a99746db01ee5c3ba6ae10ffbcd9b3c`; `runner-local` `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`; `runner-locald` `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13` | Local API and locald Unix sockets; three remote profiles below | local/locald: `1 / 0 / 0 / 0`; locald cumulative `cleanup_failures=1`, `storage_errors=351` while ready |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | `/home/ubuntu/.local/share/remote-session-runner`; service `runnerd.service`; config v1; Go 1.27.1 `linux/amd64`; live/ready | `runnerd` `22f33d6cd3d429c6d91537031954222d7a648e3a3d46e1e169ddbeaa89c21104`; bridge `61f8cbfa22a0dec7e2916db7cf6077d5d2716a90f4db0b9f9506494383ec3203`; entrypoint `83e5e56d300eeea4a56a336c13706ed7de7697f324e22278e9e3fbdf6be9f2f3` | `10.0.0.200:8443`; existing SSH `:22` | `0 / 0 / 0 / 0` |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | Same unsuffixed root and service; config v2; Go 1.27.1 `linux/arm64`; live/ready | `runnerd` `b0d864b1ff285f6591868aa463e913537955bf1a0a06130f31e8fdd635004491`; bridge `45a77bf5fcb28a89c23ec1f3718089e8793768c5294b9e97e216f3af8c760934`; entrypoint same as linux-host | `10.0.0.14:8443`; existing SSH `:22` | `1 / 0 / 0 / 0`; cumulative `cleanup_failures=1` while ready |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | Same unsuffixed root and service; config v2; Go 1.27.1 `linux/amd64`; live/ready | `runnerd` `fafb7d9ba5d343d425fa1752c21a4859a3f42c344d5eca5a43ee5f3dfc0ee374`; bridge `3a13bff38320ab0098cecea38e349e4868c9ab9702a75f2f3ff88cbaad7ce42c`; entrypoint same as linux-host | `10.0.0.30:8443`; existing SSH `:22` | `0 / 0 / 0 / 0` |

Session counts are aggregate live slot counts; their identities were not
inspected. No active command slots, queued commands, queued intents, or
mailbox backlog were reported on the observed services. The Mac session and
sandbox session remain untouched. Health counters are cumulative and are not
interpreted as newly occurring errors in this snapshot.

## Existing resources and policies

| Host | Filesystem available | Memory snapshot | Existing PRD policy facts |
| --- | --- | --- | --- |
| Mac | Data volume 460 GiB total, 278 GiB used, 157 GiB available (64%) | 48 GiB physical; memory pressure reported 44% free | Existing LaunchAgents use the same account and direct log files. Config has no `limits` override. |
| `linux-host` | `/dev/sda1`, 45 GiB total, 25 GiB used, 21 GiB available (56%) | 952 MiB total, 480 MiB available; 1 GiB swap, 885 MiB free | `CPUQuota`, `MemoryHigh`, and `MemoryMax` are unlimited; `TasksMax=1035`. |
| `sandbox-host` | `/dev/sda1`, 52 GiB total, 49 GiB used, 2.8 GiB available (95%) | 5.8 GiB total, 4.4 GiB available; 8 GiB swap, 7.6 GiB free | `CPUQuota`, `MemoryHigh`, and `MemoryMax` are unlimited; `TasksMax=7000`. |
| `factory-sandbox` | `/dev/sda1`, 48 GiB total, 4.8 GiB used, 43 GiB available (11%) | 951 MiB total, 589 MiB available; no swap | `CPUQuota`, `MemoryHigh`, and `MemoryMax` are unlimited; `TasksMax=396`. |

The current V1/V2 configs have no explicit `limits` override, so Runner's code
defaults apply: 20 active sessions and four running commands per host, a
30-minute command timeout and idle timeout, four-hour session maximum, 100 MiB
output per command, 90-day metadata retention, and 30-day output retention.
These are Runner application limits; they are not CPU, memory, disk, or hostile
process isolation. The SIMPLE profile will set Dev/QAS to one running command,
two sessions, and 16 MiB × four retained task logs per component. No numeric
existing disk safety floor was present in the inspected Runner configs; the
sandbox's 2.8 GiB free must be rechecked against the measured candidate and
runtime footprint before that host is installed. No files will be removed to
create room.

## Frozen host, lane, route, and credential map

| Host/resource | PRD as observed | Dev target | QAS target |
| --- | --- | --- | --- |
| Mac account/root | `tomasz.walczuk`; `~/Library/Application Support/RemoteSessionRunner` | `.../RemoteSessionRunner-dev` | `.../RemoteSessionRunner-qas` |
| Mac services | `com.remote-session-runner.local` and `.locald`; matching unsuffixed plist names | add `-dev` to both labels and plist names | add `-qas` to both labels and plist names |
| Ubuntu account/root (each worker) | `ubuntu`; `/home/ubuntu/.local/share/remote-session-runner` | `.../remote-session-runner-dev` | `.../remote-session-runner-qas` |
| Ubuntu execution service | `runnerd.service` | `runnerd-dev.service` | `runnerd-qas.service` |
| Linux-host direct HTTPS | `129.151.232.40:8443`, bind `10.0.0.200:8443` | `129.151.232.40:8444`, bind `10.0.0.200:8444` | `129.151.232.40:8445`, bind `10.0.0.200:8445` |
| Sandbox direct HTTPS | `132.226.205.205:8443`, bind `10.0.0.14:8443` | `132.226.205.205:8444`, bind `10.0.0.14:8444` | `132.226.205.205:8445`, bind `10.0.0.14:8445` |
| Factory direct HTTPS | `145.241.255.66:8443`, bind `10.0.0.30:8443` | `145.241.255.66:8444`, bind `10.0.0.30:8444` | `145.241.255.66:8445`, bind `10.0.0.30:8445` |
| Queued SSH | Existing system SSH listener on port 22 | Same listener and port 22; separate key with a fixed command to the Dev bridge/socket | Same listener and port 22; separate key with a fixed command to the QAS bridge/socket |
| Config, database, sockets | Mac `config/mac.yaml`, `state/local.db`, `run/local-api.sock`, `run/locald.sock`; Ubuntu `config/linux.yaml`, `state/remote.db`, `run/runnerd.sock` | Same leaf names inside Dev root | Same leaf names inside QAS root |
| Mailbox/workspace/temp/logs/backup/staging | Existing PRD paths below the unsuffixed root and the current external Mac inbox roots | Fresh lane-rooted paths | Fresh lane-rooted paths |
| Per-profile credentials | Existing PRD references remain as installed | `secrets/profiles/<profile>/{ssh,tls}/...` below the selected lane root, unique Dev key/certificate/controller identity per host/profile | Same path layout with unique QAS identities |

Canonical profiles remain `linux-host` (`129.151.232.40`, `10.0.0.200`),
`sandbox-host` (`132.226.205.205`, `10.0.0.14`), and `factory-sandbox`
(`145.241.255.66`, `10.0.0.30`). The Mac V2 config currently has all three
routes on SSH 22 and HTTPS 8443. Its existing dispatcher, known-host, client
certificate, and CA references are below the unsuffixed `RemoteSessionRunner`
root and are retained unchanged. Dev/QAS client credentials will live under
their own roots. Worker Dev/QAS server certificates, client CAs/principal maps,
and forced-command bridge keys will be unique per lane. A forced-command entry
will be added to the existing `/home/ubuntu/.ssh/authorized_keys`; existing PRD
and unrelated entries remain intact. This is the application bridge identity,
separate from the three explicit SSH identities used for administrative host
access.

The concrete installed Mac PRD credential file map is:

| Profile | SSH known-hosts / dispatcher key | Direct CA / client certificate / private key |
| --- | --- | --- |
| `linux-host` | `secrets/ssh_known_hosts` / `secrets/dispatcher_ed25519` | `secrets/poc-ca.pem` / `secrets/direct-client.pem` / `secrets/direct-client.key` |
| `sandbox-host` | `secrets/sandbox_known_hosts` / `secrets/sandbox_dispatcher_ed25519` | `secrets/poc-ca.pem` / `secrets/sandbox-direct-client.pem` / `secrets/sandbox-direct-client.key` |
| `factory-sandbox` | `secrets/factory-sandbox_known_hosts` / `secrets/factory-sandbox_dispatcher_ed25519` | `secrets/factory-sandbox-ca.pem` / `secrets/factory-sandbox-direct-client.pem` / `secrets/factory-sandbox-direct-client.key` |

All paths in that table are relative to the observed unsuffixed Mac root. Each
worker's existing direct service references `secrets/server.pem`,
`secrets/server.key`, and `secrets/client-ca.pem`, with
`config/client-principals.yaml`; these remain PRD-only. The SIMPLE V3 path
derivation under `secrets/profiles/<profile>/...` is the separate future
credential map for Dev/QAS and contains no credential bytes in source.

The existing Mac V2 config has eight current PRD inbox roots. They remain
unchanged; Dev/QAS receive fresh lane-rooted inboxes:

| Mailbox | Existing PRD root |
| --- | --- |
| `agentic-factory-test-project-01-runner` | `/Users/tomasz.walczuk/projects/agentic-factory-test-project-01/runner/mailbox-` |
| `agentic-factory-test-project-02-runner` | `/Users/tomasz.walczuk/tmp/agentic-factory-test-project-02/runner/mailbox-` |
| `agentic-software-factory-runner-direct` | `/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-` |
| `agents-work-dispatcher` | `/Users/tomasz.walczuk/projects/agents-work-dispatcher/tmp/mailbox-` |
| `analytics` | `~/Library/Application Support/RemoteSessionRunner/mailboxes/analytics` |
| `default` | `~/Library/Application Support/RemoteSessionRunner/mailbox` |
| `remote-session-runner-workspace` | `/Users/tomasz.walczuk/projects/remote-session-runner/runner/mailbox-` |
| `slidestud-io` | `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` |

## Reusable implementation and exact gaps

Reusable and already present:

- `src/internal/config/lane_namespace.go` defines Dev/QAS/PRD identities,
  deterministic lane-root paths, per-host database/socket names, direct HTTPS
  endpoints, and profile names.
- `src/internal/config/lane_profiles.go`, V3 schemas, and focused tests validate
  closed configuration documents, lane/profile/controller relationships,
  endpoint identity, credential file references, and path separation.
- `src/internal/laneinstall` renders selected-lane Mac/Ubuntu artifacts without
  writing them; `runner-log-capture` exists in source for bounded log capture.
- Existing V1/V2 parsing and installed PRD config remain separate. New Dev/QAS
  config must state its release lane; pulling source does not replace PRD.

Corrections required before using that source for this SIMPLE profile:

1. `DeriveLaneNamespace` currently assigns SSH ports 2224/2225, separate
   `runner-sshd-{dev,qas}` units, `/etc/remote-session-runner/ssh/{lane}`
   authorities, and new `remote-session-runner-{dev,qas}.slice` scopes. These
   conflict with the existing port-22 listener and excluded dedicated-sshd,
   root-authority, and cgroup work. SIMPLE-02 will bind lane keys to the
   existing listener and remove those artifacts from the active path. Its Mac
   namespace also names a separate `RemoteSessionRunner-lane-registry` root;
   do not create or depend on that root for this profile.
2. The current Linux renderer emits a systemd `Slice=` and a slice unit; it
   cannot be used unchanged. The current SSH renderer generates dedicated
   daemon configuration. Keep only the fixed lane bridge invocation over the
   existing SSH service.
3. `ValidateV3Target` and L1-01 tests currently reject Mac-local execution in
   Dev/QAS. The active profile permits it when that lane's existing capability
   and policy are configured; it is not a prerequisite when remote-only.
4. Existing `deploy/macos/install-launchagents.sh` and
   `deploy/linux/install-systemd-service.sh` are pinned to unsuffixed PRD. No
   selected-lane installer/status/start/stop/rollback path is complete yet.
   The full V3 profile parser is separate from the V1/V2 runtime loader and
   does not yet supply a complete selected-lane runtime configuration.
5. No Dev/QAS roots, service units/agents, installed binaries, databases,
   mailboxes, sockets, or lane credentials were present on any observed host.

SIMPLE-02 will adapt the route/config contracts and regression tests to this
map. SIMPLE-03 will add guarded selected-lane installation and rollback. The
measured sandbox disk is revisited before any Dev installation there. B021/B024
and their retained evidence continue to block only operations that touch their
specific service/state; neither was changed in this audit. BUG-022/023 remain
deferred and OPEN.

## Read-only command record

Execution machines: Mac `mac-control-plane`; SSH aliases `remote-session-runner`
(`linux-host`), `sandbox.env` (`sandbox-host`), and `factory-sandbox`.
Commands used included `df -h`, `free -h`, `systemctl show`, `ss -lntp`,
`sha256sum`/`shasum -a 256`, `go version -m`, `git rev-parse`, `git status`, and
GET requests over the pre-existing health sockets. Every observation was
read-only. No build, Runner request, ACK, fixture, service change, or storage
operation was performed for SIMPLE-01.
