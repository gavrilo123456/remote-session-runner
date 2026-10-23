# CAT-06 — PRD adoption and promotion

## Owner-selected legacy environment IDs — 2026-10-23 12:10 UTC — acceptance still blocked

The owner directed the operator to keep the sealed release
`cat-20261022-7a42f3b`, avoid configuration aliases or changes, and use the
existing environment IDs through each PRD-specific endpoint or socket. This
was checked against the live configs and the exact source revision bound to
the release (`7a42f3bb6712e5186f855df4bc50493041404841`). The IDs do map to
the requested targets and routes:

| PRD service | Config version / SHA-256 | Existing environment ID → target | PRD route |
| --- | --- | --- | --- |
| Linux | V1 / `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `linux-dev` → `remote:linux-host` | direct `https://129.151.232.40:8443`; queued SSH `129.151.232.40:22` |
| Sandbox | V2 / `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `sandbox-dev` → `remote:sandbox-host` | direct `https://132.226.205.205:8443`; queued SSH `132.226.205.205:22` |
| Factory | V2 / `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `factory-sandbox-dev` → `remote:factory-sandbox` | direct `https://145.241.255.66:8443`; queued SSH `145.241.255.66:22` |
| Mac local | V2 / `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | `mac-dev` → `local:mac-workstation` | Mac PRD API socket `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock` |

Fresh read-only checks confirmed all four PRD selectors still resolve inside
`cat-20261022-7a42f3b`; the PRD receipt remains byte-identical at SHA-256
`9431286a588bbeecabeead390d7bc7baa83022cb8ba90d4b8973edc3c54aac40`, stage
`verifying`, with 17 transitions. The three unsuffixed `runnerd.service`
instances use the unsuffixed PRD `current/bin/runnerd` and
`config/linux.yaml`; each `/proc/<pid>/exe` resolves to its host directory
under the sealed release. The Mac unsuffixed `local` and `locald` LaunchAgents
are running from the PRD `current` selector. Worker direct readiness GETs
returned HTTP 200 over TLS 1.3 with the configured CA, client certificate,
and hostname verification. The supported Mac-local fence returned readiness
200 and the expected admission-closed 503. No command-level probe was
accepted, no receipt transition was appended, and QAS and its eight mailbox
assignments were not touched.

The owner-selected IDs cannot pass the sealed maintenance probe guard. At
the release's exact source revision,
`CatalogMaintenanceProbeRequestBodyForTransport` generates
`<host>-prd` for remote requests and `mac-workstation-prd` for Mac-local;
`CatalogMaintenanceProbeGuard.ValidateFixedProbeRequest` requires those exact
names. The `catalog-probe-prepare` CLI has no environment override and calls
that fixed-body generator. Replacing the generated name with an existing ID
would still be rejected by the service guard before execution. Therefore
there is no supported way to do the requested direct, queued-SSH, or
Mac-local command acceptance with the frozen service release and unchanged
configs while the cohort hold remains active. Do not hand-edit a scope or
receipt, open ordinary admissions, or claim CAT-06 acceptance.

The strict direct probe helper also refused its preflight locally with
`configured direct TLS material failed certificate, CA, fingerprint, or URI
validation`: the active Mac route is V2 path-based configuration and does not
carry the V3 fingerprint/URI fields required by that helper. This did not
reach a worker; it is not evidence of a server TLS failure. The separate
CA-validated readiness GETs passed, but they are not functional acceptance.

No config, service, selector, database, receipt, mailbox, or release bytes
were changed. No request or ACK was published, and expired probe scopes were
not reused. The four-host operation remains **BLOCKED at `verifying`**. A
future supported resolution must either add an approved compatibility path
for the existing environment IDs to the guarded probe implementation (which
changes runtime code and needs a new sealed release and requalification), or
use a separately approved config/release change. Until one is authorized and
validated, keep PRD admissions held and QAS/mailbox ownership unchanged.

## PRD verification checkpoint — 2026-10-23 10:10 UTC — release running; functional admission blocked by active config

The renewed operation `c2f074a84bc864830266a209943319eb` remains at
`verifying`. Its four-hour renewal `eff2967e19cdc8fe9f29f7c888cb2694` expires
at `2026-10-23T13:03:34.029851Z`. The receipt has 17 transitions and exact
SHA-256 `9431286a588bbeecabeead390d7bc7baa83022cb8ba90d4b8973edc3c54aac40`
on Mac, Linux, Sandbox, and Factory. No host verification or completion
transition was recorded. No further selector change is needed: every PRD
selector already points to the unchanged sealed release
`cat-20261022-7a42f3b` (`b2c0ec9d47b3ebed966e3ebd115a994865910142170358af95ad9857e8bb4e7c`):

| Host | Active per-host bundle SHA-256 | PRD service | Readiness |
| --- | --- | --- | --- |
| Linux | `cbff1c52fceb11ce76b42519de54702d3b2b628df3b134560ddaeb0cb7c1d582` | active | ready; revision `7a42f3bb6712e5186f855df4bc50493041404841` |
| Sandbox | `ff2aab3fbbcbc6a0e671c8185d8da2bda71a2170014ade18b7200d66f5263b5d` | active | ready; revision `7a42f3bb6712e5186f855df4bc50493041404841` |
| Factory | `ca0746f242be8918d58352e621235058f5c2f582fb5f17850ababd9996fe8ab1` | active | ready; revision `7a42f3bb6712e5186f855df4bc50493041404841` |
| Mac | `fe143c888978276349c3575af6e1bbc063c69cd6051b9f1e8bb7c90266afcbac` | both launch agents running | ready; revision `7a42f3bb6712e5186f855df4bc50493041404841` |

Fresh direct mTLS readiness returned HTTP 200 on all three workers with normal
certificate and hostname verification. Their ordinary POST fence checks
returned HTTP 503 `maintenance_admission_closed`. The Mac-local readiness
returned HTTP 200 and its supported fence check returned HTTP 503 with the
same code. Linux, Sandbox, and Factory health snapshots had zero active
session/command slots, queued commands/intents, mailbox backlog, storage
errors, cleanup failures, and event gaps. The Mac snapshot had zero active
slots, queued commands, mailbox backlog, storage errors, cleanup failures,
output truncations, and event gaps; it still reports `queued_intents=3` and an
old reconciliation age. No attempt was made to replay or clear those intents.
Sandbox measured `1,462,018,048` available bytes on `/dev/sda1`
immediately after its small scope-file writes, above the operation's
`849,205,489`-byte minimum by `612,812,559` bytes.

The scoped direct probe exposed a concrete authorization/configuration
failure. Command:

```sh
python3 /private/tmp/cat06-renewal-ef398f2/run-direct-probe.py --routes /private/tmp/cat06-renewal-ef398f2/direct-routes.json --prepared /private/tmp/cat06-renewal-ef398f2/probe-linux-direct.json --host linux-host
```

It exited 1 with `direct probe did not complete within 15 seconds`. The
wrapper job `job-af883bb8fa37e28842db336faed68eab` then read as
`phase=failed`, with no command state, exit code, final event sequence, or
created teardown. The Linux service audit records `record_id=968`,
`reason_code=environment_denied`, at `2026-10-23T10:00:12.996223536Z`, for
`environment=linux-host-prd`; no runtime command was executed.

The one-time queued-SSH probe also reached Linux PRD but was denied before
execution. The helper exited 0 while reporting a failed job, so it is not a
passing acceptance result:

```sh
/private/tmp/cat06-renewal-ef398f2/catalog-probe-once --mode=queued --config "$HOME/Library/Application Support/RemoteSessionRunner/config/mac.yaml" --prepared /private/tmp/cat06-renewal-ef398f2/probe-linux-queued.json
```

It returned `phase=failed`, `teardown_state=not_created`, and
`bridge_response_type=result` for job `job-20f4fc4a7375132f1159b9e0926fb87a`.
The Linux service audit records `record_id=969`,
`reason_code=environment_denied`, at `2026-10-23T10:04:54.828635764Z`, again
for `linux-host-prd`. Neither probe reached runtime execution; the subsequent
health snapshots show zero active slots and queued commands.
Both one-time Linux scopes are retained as consumed. Sandbox and Factory
direct/queued scopes and the Mac-local scope remain unconsumed; they expired
between `10:08:12Z` and `10:08:28Z` and will not be reused. No filesystem
mailbox request or ACK was published.

The active PRD configs confirm the guard's cause. The Mac service loads
`config/mac.yaml`, SHA-256
`56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11`; its
environment keys are `factory-sandbox-dev`, `linux-dev`, `mac-dev`, and
`sandbox-dev`. Worker PRD config hashes and environment keys are:

| Host | Active config SHA-256 | Registered environments |
| --- | --- | --- |
| Linux | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `mac-dev`, `linux-dev` |
| Sandbox | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `sandbox-dev` |
| Factory | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `factory-sandbox-dev` |

Thus the active PRD configuration on each host lacks the exact PRD environment
name used by its lane-bound acceptance probe. No configuration, credentials,
routes, databases, selectors, or mailbox assignments were changed to bypass
this guard. A config change would change receipt-bound configuration hashes
and requires a separately reviewed, freshly backed-up and preflighted
supported operation; it cannot be treated as a continuation of the current
receipt without its guards. The earlier strict `catalog-probe-once --mode
direct` helper also rejected the legacy V2 TLS config before network access
because its optional fingerprint/URI pin fields are absent. The mTLS health
and scoped POST checks above used the configured CA and client identity with
normal certificate and endpoint verification; no insecure TLS option was
used.

One idempotent Mac receipt-sync attempt was refused because the selected lane
was active or had retained work. It changed no receipt. The Mac copy already
matches the same four-host receipt SHA above, so no synchronization gap exists
at this checkpoint. A prior sandboxed Mac start returned launchd exit 5; the
same supported start from the authorized elevated shell then succeeded, and
both Mac agents are running. An earlier Sandbox stop recorded a shutdown
flush timeout; the subsequent start succeeded and current readiness is
healthy. These failures remain historical evidence.

**CAT-06 PRD acceptance remains NOT PASS.** Preserve the `verifying` receipt,
all four selected release identities, the operation hold, the two failed
Linux probe records, unconsumed expired scopes, backups, and Mac's three
queued intents. Keep PRD admissions closed. QAS was not accessed, restarted,
reconfigured, or moved; its eight mailbox paths remain assigned to QAS. The
specific next dependency is owner-approved, reviewed PRD environment policy
for the four existing services (or an existing approved PRD config containing
those mappings). Any resulting config hash change must go through fresh
backup/preflight and supported cohort-history handling before another
acceptance attempt. Do not resume or hand off mailbox users before PRD
functional acceptance passes.

This checkpoint was committed on Mac `dev` as
`6bf7cea05c5033b7807a42fcce194afd1b435db5`, pushed to primary Gitea, and
verified at the exact same SHA on the automatic GitHub mirror. Linux, Sandbox,
and Factory were clean on `dev` before pull and fast-forwarded to that SHA;
their checkouts remain clean. This was documentation/evidence-only; the
already-passed source suites were not rerun. `git diff --check` passed. The
untracked `src/cmd/catalog-holdcheck/` directory remains untouched.

## Current continuation-renewal checkpoint — 2026-10-23 08:53 UTC

The Sandbox capacity recheck at `08:45:14Z` measured `1649299456` bytes free
on `/dev/sda1`; immediately before host source gates `/tmp` on the same
filesystem measured `1648234496` bytes. This exceeded the last recorded
renewal threshold (`567108864` bytes). The operation-specific capacity
calculation must still be refreshed immediately before the renewal write.

The supported renewal `issue` initially refused a fresh Mac observation
because free bytes changed by `4411392` between its captured report and the
coordinator's own fresh check, even though all stable host proof fields
matched. No renewal file was written. Source correction
`7fafb20de7b74858f8ff83679d85df2ea3db1e1d` now compares stable host evidence
without capture time/free-byte jitter; every observation still independently
enforces its current capacity minimum, and the coordinator binds its fresh
Mac reading into the issued record. Regression coverage proves that capacity
jitter is accepted while configuration identity changes and below-threshold
capacity remain rejected.

The source commit is on primary Gitea and the automatic GitHub mirror at the
exact SHA above. All Ubuntu `dev` checkouts were clean before fast-forwarding
and are clean at that revision. Go 1.27.1 gates passed:

| Host | Gates | Result and capacity evidence |
| --- | --- | --- |
| Mac | Full `make test`, `make vet`, `make smoke` through `make check`, plus standalone `make build` | PASS, exit 0 for both commands. Darwin/arm64. Shared configured Go caches used. |
| Sandbox | Full `make test`, `make vet`, `make build`, `make smoke` | PASS, each exit 0 on linux/arm64. `/tmp` is on `/dev/sda1`; free bytes were `1648234496` before gates and `1576251392` after. The earlier interrupted run remains **INTERRUPTED / NOT PASS**. |
| Factory | Full `make test`, `make vet`, `make build`, `make smoke` | PASS, each exit 0 on linux/amd64 with `GOMAXPROCS=1`, `GOFLAGS=-p=1`, `nice -n 19`; temporary paths used the existing `/tmp` mount, with no new mount created. |
| Linux | Focused `laneinstall`/`runner-lane` tests; exact P025 `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`; exact P101 `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`; native vet/build/smoke | PASS, each exit 0 on linux/amd64. Full Linux suite remains outside the amended assignment; prior interrupted runs remain **NOT PASS**. |

The frozen service release `cat-20261022-7a42f3b` is unchanged. No PRD service,
selector, receipt, database, backup, mailbox path or ACK changed during these
source checks. QAS and its eight mailbox paths remain unchanged. Next, rebuild
only the temporary renewal operators from `7fafb20`, collect a wholly fresh
four-host observation set, issue/synchronize/verify the supported four-hour
renewal, then continue Sandbox → Factory → Mac while preserving Linux's
completed switch. Keep the two-minute fresh preflight before every selector
change.

## Current renewal-guard checkpoint — 2026-10-23 08:38 UTC

The first fresh observation attempt at 08:16 UTC passed on Mac, Sandbox and
Factory, but Linux was rejected by the supported renewal tool with
`existing lane update requires a verified config and database backup`. No
renewal was issued and no receipt or selector changed. The three successful
reports were retained in owner-only task directories but are stale and will
not be reused.

Read-only Linux backup inspection established the precise mismatch. The
receipt-bound backup at
`/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261023T053112.802903416Z`
has the expected manifest digest
`0d3f84432644ce745edc3e0dd8d9e2b32a07511174b2b367b1edf0c258fb1627`; all 15
archived target copies matched their manifest hashes, including the online
database snapshot. Three live service artifacts differ from those retained
pre-switch copies: `service/runnerd.service`,
`bin/runner-ssh-bridge-forced.sh`, and `bin/runnerd-entrypoint.sh`. Their
modification times (05:54:00Z) coincide with Linux's recorded selector switch
(05:54:03Z); the backup was created at 05:31:12Z. The supported history
validator had already confirmed Linux's selected service artifacts, stopped
state, work inventory, configuration/schema, and selector journal. The
renewal observer then incorrectly required those switched artifacts to remain
identical to their pre-switch bytes.

Source correction `49bfc877a49712c6b6b1dcc045212f3a65e60459` makes the
observer use the already-verified backup contents and the history validator's
current selected-artifact/journal proof for a host recorded as switched. An
unswitched host still must match every live legacy artifact to its backup. The
new regression changes a live service artifact after Linux's recorded switch,
proves the archived copy remains valid, accepts that switched state, and
continues to reject the same drift for an unswitched host. The sealed
`cat-20261022-7a42f3b` release was not rebuilt or changed.

The correction is delivered on Gitea and the automatic GitHub mirror at
`49bfc87`; all three Ubuntu `dev` checkouts were clean and fast-forwarded to
that exact commit before validation. Go 1.27.1 gates on the corrected source
all passed: Mac full `make test`, vet, build and smoke; Factory Linux/AMD64
full `make test`, vet, build and smoke; Sandbox linux/arm64 full `make test`,
vet, build and smoke; and Linux focused laneinstall/runner-lane tests, exact
P025/P101 regressions, vet, build and smoke. The prior interrupted Sandbox
run remains **INTERRUPTED / NOT PASS**. No production services, receipts,
selectors, databases or mailbox contents were changed by these source gates.

Next: rebuild temporary operator binaries from source `49bfc87`, refresh all
four host observations together, issue the one-time renewal on Mac, synchronize
and verify its identical digest everywhere, and then continue Sandbox →
Factory → Mac. Keep QAS and its eight mailbox paths unchanged.

## Historical source-gate checkpoint — 2026-10-23 08:10 UTC

The requested Sandbox rerun for runtime source
`036512d13be296b6812ddfe52c45f37d70a8deb5` is complete. The checkout was
clean on `dev` at evidence closeout `b7b48a7cfd6777dcc09286d4990c481c2eb56121`;
the only tracked difference from source `036512d` was this CAT-06 evidence
file. Gitea `origin/dev` and the automatic GitHub `dev` mirror both resolved
to `b7b48a7`, and Sandbox fast-forwarded from `036512d` to that evidence
closeout before testing. Linux and Factory were already clean at `b7b48a7`.

On `oracle-gustaw-janecki-ubuntu-flex-02` (Linux ARM64), the explicit Go
1.27.1 toolchain was
`/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go`.
The four commands ran sequentially with
`TMPDIR=/tmp GOTMPDIR=/tmp SQLITE_TMPDIR=/tmp GOMAXPROCS=2 GOFLAGS=-p=1
GOTOOLCHAIN=local`; each exited 0:

| Gate | Start (UTC) | Exit | Free bytes before → after |
| --- | --- | ---: | ---: |
| `make test` | 08:06:25 | 0 | 1,846,796,288 → 1,814,200,320 |
| `make vet` | 08:09:57 | 0 | 1,814,130,688 → 1,813,876,736 |
| `make build` | 08:10:13 | 0 | 1,813,737,472 → 1,811,894,272 |
| `make smoke` | 08:10:35 | 0 | 1,811,849,216 → 1,811,804,160 |

The earlier interrupted Sandbox suite remains **INTERRUPTED / NOT PASS**; this
successful run is recorded separately and does not relabel it. The fresh
available space exceeds the conservative 672,019,712-byte retry threshold and
the recorded 849,205,489-byte backup/staging + operating-buffer + selector
reserve requirement. No Runner service, selector, receipt, database or mailbox
was changed by these source gates. Next, collect new four-host PRD observations
and issue/synchronize the supported operation-specific renewal; the older
06:35 UTC observations remain stale and cannot be reused.

## Historical continuation checkpoint — 2026-10-23 07:39 UTC — Sandbox storage blocked remaining gates

The owner has authorized one operation-specific four-hour continuation for
PRD operation `c2f074a84bc864830266a209943319eb`, release
`cat-20261022-7a42f3b`. The 06:35 UTC expiry checkpoint below is preserved as
history; its recommendation to roll back solely for expiry is superseded. No
rollback or selector change was made in this continuation turn.

The source now adds `runner-lane catalog-cohort-renewal` and a separate
canonical owner-only evidence file outside `catalog-cohorts`. The tool binds
four newly observed host reports to the operation/release and exact receipt
transition prefix, validates current holds, stopped services, empty work,
configuration/schema, valid backups, selector state/journal, and at least
500,000,000 bytes plus the separate 64 MiB selector reserve. The coordinator
starts the fixed four-hour interval only after all observations, rejects any
observation over two minutes old or later than coordinator verification, and
keeps the original receipt bytes, fields, transition history, and timestamps
unchanged. Subsequent selector changes still require each host's fresh
two-minute receipt preflight. Scope and command sequence are recorded in the
[design](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
and [plan](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md).

Source commit `036512d13be296b6812ddfe52c45f37d70a8deb5` was pushed to primary
Gitea `origin/dev`; direct `ls-remote` checks confirmed both the primary and
automatic GitHub `dev` mirror at that exact SHA. Linux, Sandbox, and Factory
were clean on `dev` at the prior source SHA before delivery, then each
fast-forwarded to `036512d` from GitHub and verified clean at the exact commit.
The push itself succeeded; the sandbox could not update its local
`refs/remotes/origin/dev` tracking ref, so the remote SHA was independently
verified. Ubuntu builds/tests below ran only after those exact pulls.

Mac source gates used Go 1.27.1 and the Makefile's shared caches. The first
restricted-shell `make test` attempt failed only because the sandbox denied
the test-owned `/bin/ps` child in
`TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`; it was not
counted as PASS. Repeating the full gate in the normal Mac shell passed
(exit 0, log SHA-256
`7332fca388880cfb6684076f703e716589438da9b0817d0cc53880515f8487b2`); that
full run includes the final strict timestamp-ordering regression. The earlier
focused renewal package run also passed. `make vet`, `make build`, and `make
smoke` each exited 0; log SHA-256 values are
`b6104d309f1f528289d22571591532714364a7c6e03c52323e580833e4dbaca7`,
`4b874c9029f8b147785d85d743e4dd2c47bbe3018c78d560621375447c7f64b9`, and
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
The earlier sandboxed test log is retained at
`/tmp/cat06-renewal.bJH18d/mac-test.log` (SHA-256
`e251dec4bab1cc7431cfb138cd78ac0ac28b8457b475862cd250657970d51eab`).

Factory full Linux/AMD64 `make test`, `make vet`, `make build`, and `make
smoke` all passed at Go 1.27.1, with `GOMAXPROCS=1`, `GOFLAGS=-p=1`, `nice
-n 19`, and task-owned `/tmp/c06r.1DibSm` logs. Their SHA-256 values are
`e2da448ac1fb279bf0262a7f1cdaae3f3de4f844cb675db1b4e16171d40e69f5`,
`ea105d600441ee01024daa62416f82064db2d3cd45bdb6523aaeac537316da91`,
`1b46940323a3207ceca21ad2bde10aa3c58bc808a5250c76e33ac0f7c0cf6e7b`, and
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
The final `/tmp` aggregate free space was 3,426,177,024 bytes. Its mount is a
4 GiB tmpfs with `usrquota`; the installed `quota`/`repquota` tools cannot
query that tmpfs (`Cannot stat() mounted device tmpfs`), and `ulimit -f` is
unlimited. Thus Factory's effective per-user quota remains unverified even
though the full source gates passed.

Linux focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane
-count=1`, exact P025 and P101 regressions, native vet/build/smoke all passed
at Go 1.27.1. The first three direct test invocations used bare `go`, which
was not on the non-login SSH `PATH` and exited 127 before running tests; they
were rerun with the explicit toolchain path. Log SHA-256 values: focused
`efa9652f7746a628d5fb2ae71163b08fd5d70b74a4621127e5f335bf92f59932`, P025
`66f5b592dbb6e57e322bb7225c53e3b378262d4385224f3916a1037be889e4ab`, P101
`b42e6ea04df7b9e2489c9f776aa469f4516cefe2cd450b6e30613c1c3720d08b`, vet
`ea105d600441ee01024daa62416f82064db2d3cd45bdb6523aaeac537316da91`, build
`1b46940323a3207ceca21ad2bde10aa3c58bc808a5250c76e33ac0f7c0cf6e7b`, and
smoke `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
Full Linux suite remains **NOT RUN** under the owner amendment; the historical
interrupted runs remain **NOT PASS**.

Sandbox is linux/arm64 with Go 1.27.1. `/dev/sda1` had 636,420,096 bytes
available before its full-suite attempt. `make test` used a mode-0700 task
directory under `/tmp`, `GOMAXPROCS=1`, `GOFLAGS=-p=1`, and `nice -n 19`. As
free space fell to 556,769,280 bytes—below the 567,108,864-byte 500 MB plus
64 MiB reserve—the task stopped only its own test process. The command ended
143 and is **NOT PASS**, not a test assertion failure; its log SHA-256 is
`85bc7cb4d7002912ae1074d343ba3ac7171811e6e0615537e3bb34dc68574aa1`. After
the process exited, the task-owned 59,665,982-byte temporary directory was
removed; shared Go caches were preserved. Free space then measured
589,967,360 bytes, only 22,858,496 bytes above the reserve. Based on the
104,910,848-byte decline observed during the partial run, a retry needs at
least 672,019,712 bytes at start merely to repeat that observed peak while
retaining the reserve; the completed full suite and vet/build/smoke may need
more. Sandbox vet/build/smoke were not started. The exact owner action needed
to resume those gates is to free at least 82,052,352 additional bytes on
`/dev/sda1` beyond the last measured state, without deleting retained work,
shared caches, backups, or mailbox data; remeasure before retrying.

A later read-only pre-pull check measured Sandbox at 562,266,112 bytes free,
4,842,752 bytes below the 567,108,864-byte reserve. The evidence-only commit
`7da8173` was not pulled there; its clean `dev` checkout remains at source
commit `036512d13be296b6812ddfe52c45f37d70a8deb5`. At this newer measurement,
the conservative 672,019,712-byte retry target is short by 109,753,600 bytes.
No checkout write was attempted after this check.

The host source gates are incomplete, so renewal evidence has not been
collected or issued and no PRD selector effect was attempted. The previously
recorded 06:35 UTC host observations are stale for renewal. After Sandbox
gates pass, build the operator from the delivered source, freshly observe all
four hosts, issue on Mac, synchronize and verify the same renewal everywhere,
then continue Sandbox → Factory → Mac. This work did not touch PRD or QAS
services, selectors, receipts, backups or mailbox contents and published no
requests or ACKs. The unrelated untracked `src/cmd/catalog-holdcheck/`
directory remains untouched.

## Current checkpoint — 2026-10-23 06:35 UTC — source gates passed; stop at expired PRD cohort window

The frozen service release remains `cat-20261022-7a42f3b` (source
`7a42f3bb6712e5186f855df4bc50493041404841`, manifest SHA-256
`b2c0ec9d47b3ebed966e3ebd115a994865910142170358af95ad9857e8bb4e7c`). Dev
acceptance is **PASS**. QAS acceptance remains **DEFERRED, NOT PASS** under the
owner-approved one-time order. The fix below changes the `runner-lane`
operator/reconciliation code only; it does not alter the sealed Runner
components, release identity, or accepted Dev bytes.

### Receipt synchronization correction and delivery

The switched-host proof was rejected before its durable selector journal could
be checked: `validatePRDCatalogCohortHostObservation` compared the current
selector with the pre-switch target unconditionally. The corrected helper
skips that old-target comparison only for a receipt-recorded switch or
rollback; `validateCatalogCohortReconciledSelectorEvidence` still requires the
matching host/lane/release journal, exact target or rollback evidence,
configuration/schema identity, backup validity, stopped service, and empty
work inventory. New regression
`TestCatalogCohortSyncAcceptsSwitchedHostAfterMatchingJournalValidation`
exercises the local observation path and `syncCatalogCohortReceiptAt`.

Source commit `ad41b56ec82d6557b0d03db3563edf232097a17f` is on primary Gitea,
matches the automatic GitHub `dev` mirror, and is present in clean `dev`
checkouts on Linux, Sandbox, and Factory. The untracked
`src/cmd/catalog-holdcheck/` directory remains untouched.

| Host | Platform/toolchain and checks | Result and evidence |
| --- | --- | --- |
| Mac | darwin/arm64, Go 1.27.1; focused regression, full `make test`, `make vet`, `make build`, `make smoke` | **PASS**. The first sandboxed full-suite invocation was denied an existing test-owned `/bin/ps` child; the same full suite passed via the authorized elevated Mac shell. |
| Sandbox | linux/arm64, Go 1.27.1; `TMPDIR/GOTMPDIR/SQLITE_TMPDIR=/tmp`, `GOMAXPROCS=1`, `GOFLAGS=-p=1`, `nice -n 19`; full test/vet/build/smoke | **PASS**. Confirmation `make test` exit 0 at 06:25:57–06:26:02 UTC, log SHA-256 `4a598bbbdd4fa3a514805933192ecf7ef707e593797c56dd4eea555a158004ea`; vet `03e8db17ff2a2378dd65f7ff1b2036dbe40e8a32c3c2b1c241560af2cfa08dab`; build `fe0b0a6d881d188a311485a602fd68e0b4d762f6aa86c2bb408b3cae39aa9088`; smoke `08621bae0d6393b5556fbec2a0b0e943ce4da16ec4661a9fe07e5ed3f2ac6c69`. Earlier corrected full run executed the uncached package tests; confirmation used Go's test cache. A prior home-directory invocation failed before tests (`No rule to make target`); it is not counted as a test result. |
| Factory | linux/amd64, Go 1.27.1; `TMPDIR/GOTMPDIR/SQLITE_TMPDIR=/tmp`, `GOMAXPROCS=1`, `GOFLAGS=-p=1`, `nice -n 19`; full test/vet/build/smoke | **PASS**, each exit 0. Full test 06:15:24–06:18:08 UTC, log SHA-256 `cdcf3255c46933790faa33ba08c6a948050b70efc9cba731004d9404b3f2a25d`; vet `03e8db17ff2a2378dd65f7ff1b2036dbe40e8a32c3c2b1c241560af2cfa08dab`; build `fe0b0a6d881d188a311485a602fd68e0b4d762f6aa86c2bb408b3cae39aa9088`; smoke `08621bae0d6393b5556fbec2a0b0e943ce4da16ec4661a9fe07e5ed3f2ac6c69`. Existing `/tmp` was a 4-GiB tmpfs mount with 3,453,743,104 bytes free before the suite and 3,453,739,008 after. The per-user limit was not independently verified in this rerun. |
| Linux | linux/amd64, Go 1.27.1; `go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1`; exact `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice` and `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`; native vet/build/smoke | **PASS**, each exit 0. Focused log `ed824da43abdc4f45512f49d1ed6d17c4eb37385d1bd98e97fac75edd4d57c3b`; P025 `68f19836cf234463a7020697c532205eaa4661cac61f7bff2d3622e118eabcf1`; P101 `aab72b38c828f0d902a2a97dfd9dbe30763f1c2c4d75e8c5c443e34912f5b5e3`; vet `ea105d600441ee01024daa62416f82064db2d3cd45bdb6523aaeac537316da91`; build `1b46940323a3207ceca21ad2bde10aa3c58bc808a5250c76e33ac0f7c0cf6e7b`; smoke `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Full Linux suite remains **NOT RUN** under the amended assignment; historic interrupted runs remain **NOT PASS**. |

Corrected `runner-lane` operator hashes built from `ad41b56` with Go 1.27.1:
Darwin/arm64 `fd37b461abb788c3372f7afde4fc05873c9aefe22ee1edfb2fae3f452faf08b4`,
Linux/amd64 `283923dfd8e8ea2b296a56d125f4204a95ebaefc92c086dc3115af87619b9ab4`,
Linux/arm64 `922f677705630a477528c848226925c00459af2b707db939ffd292f283f11ef1`.
The accompanying read-only hold-checker hashes are Darwin/arm64
`edbaa374d8600e5ad822c462ce0666bd73dd27369f68fa6ab609c8708cab947f`,
Linux/amd64 `4c242a0625870563a78c20c5ccd5df37b89aabbc176402d67eeed5467ae25332`,
and Linux/arm64 `d3b288d43422dcc0f20f7d105c1366acc228e4ea9536440484215e0b6669d336`.
All temporary operator copies are task-owned under `/tmp/cat06-operator-ad41b56`;
the installed Runner bundles were not rebuilt.

### Live PRD state and expired cohort evidence

The stopped PRD operation is `c2f074a84bc864830266a209943319eb`, targeting
the unchanged Dev-accepted release above. Its stage is `switching`, and the
supported receipt is synchronized byte-for-byte on Mac, Linux, Sandbox, and
Factory at SHA-256
`030679481366de192adf4a37bf06e4b66177f2bda216e8a56139df939b82f3a1`. Read-only
hold checks on all four hosts return exactly this operation ID with
`error=false`; fresh service checks report all PRD services stopped. Linux
alone points to
`/home/ubuntu/projects-runtime/runner-releases/cat-20261022-7a42f3b/linux-host`
with bundle SHA-256
`cbff1c52fceb11ce76b42519de54702d3b2b628df3b134560ddaeb0cb7c1d582`. Mac,
Sandbox, and Factory remain `unselected`. No PRD service was started. The
legacy `prd-preflight` command refuses Linux after catalog selection with
`legacy PRD installer cannot modify a catalog-managed service`; it did not
mutate state. The corrected supported cohort sync then validated Linux's
matching selector journal and local state.

The earliest covered host observation is Factory preflight
`2026-10-23T05:31:13.326679771Z`, so the inclusive 60-minute cohort boundary
was `2026-10-23T06:31:13.326679771Z`. A read-only inspection at 06:32 UTC
found the cohort evidence expired. In accordance with the explicit stop-on-
expiry instruction, no additional selector mutation was attempted. The
operation remains held and partially switched; PRD acceptance is **NOT PASS**.
Before any new selector effect, use supported recovery for Linux's recorded
selector journal, synchronize and preserve the full operation history, and
collect a wholly fresh cohort evidence set. Do not reuse or backdate these
preflights.

QAS remains active on all four hosts and selects `cat-20261021-b84caf0`; all
eight mailbox paths remain assigned to QAS. No QAS pause/restart or mailbox
configuration change occurred. No mailbox request or ACK was published. The
QAS→PRD mailbox handoff and QAS alignment remain pending; normal project
publishers must not resume based on this partial PRD result.

## Control-tool checkpoint — 2026-10-23 01:32 UTC

The owner-approved one-time order remains Dev PASS → PRD deployment and
acceptance → QAS→PRD mailbox handoff → QAS alignment. The frozen service
candidate remains `cat-20261022-7a42f3b` (source
`7a42f3bb6712e5186f855df4bc50493041404841`); its Dev acceptance is unchanged
and PASS. QAS acceptance for this candidate is DEFERRED, NOT PASS. The native
per-host bundle bytes, Dev selectors/receipt, QAS selectors/services and QAS
mailbox ownership have not changed.

### Why an exact-artifact recovery command was needed

The documented Linux/Mac `--recover-stalled` installers build and install from
the synchronized source checkout. That is incompatible with this frozen
promotion because it would rebuild during promotion and could install bytes
different from the Dev-accepted per-host bundles. Calling the candidate's
`runnerd recover-stalled` or `runner-locald recover-stalled` directly is also
not an operator-supported path. The new `runner-lane catalog-prd-recover`
control command closes this gap without modifying the service bundles: it
validates the sealed catalog release and expected per-host hashes, the fresh
owner-only backup and manifest digest, schema/read bounds, unchanged backup
targets and work identities, the stopped service, unselected selector, absent
cohort hold, exact lost-pair count, and post-backup operating headroom. It
executes only the host-native recovery binary in the sealed bundle. It then
requires the exact recovered count, unchanged schema/configuration/release,
empty work inventory, and a still-stopped service and unselected selector.
The command neither installs nor starts service binaries, runs stored scripts,
edits SQLite directly, or restores a database. The ordinary support installers
remain unchanged.

The public `catalog-select` parser also required a PRD-specific correction:
first adoption has an empty expected selector target, but the common CLI
argument guard rejected that valid state. PRD now permits the empty previous
target; Dev/QAS continue requiring an explicit target. Regression coverage
executes the public command dispatcher with disposable PRD-shaped fixture
state, including pre-adoption recovery, initial adoption/completion,
interrupted switching, reconciliation, reverse rollback and synchronized
receipts. Negative cases reject a mismatched bundle identity and an incomplete
lost-pair set; fixture history and the database sentinel remain unchanged, and
the replay counter remains zero.

The first focused validation run is preserved as a failure: the public
subprocess sequence reached `catalog-select` for `linux-host` and the CLI
returned `invalid or inconsistent catalog selector operation` because it
required a nonempty expected target even for PRD's unselected legacy state.
The same run also exposed overbroad assumptions in two new unit-test inputs;
the malformed-ID test now uses an actually invalid path-bearing value, and
recovery-summary parsing now rejects partial/already-recovered counts itself.
After correction, Mac Go 1.27.1 `go test ./src/internal/laneinstall
./src/cmd/runner-lane` passed. Full Mac `make test`, `make vet`, `make build`,
and `make smoke` also pass on `darwin/arm64` with Go 1.27.1. The first
`make test` attempt was blocked when the sandbox denied an existing test's
test-owned `/bin/ps` child; an isolated retry without explicit cache settings
was separately denied access to the default user Go cache before tests began.
The full suite passed after rerunning with normal test-owned process-inspection
permission. The new recovery preflight additionally refuses a database schema
that would require migration by the offline recovery binary; its regression
passes for the current-schema case and rejects older, newer, or inconsistent
schema bounds. No service bundle was rebuilt. Ubuntu host gates, Git delivery,
and live recovery remain PENDING.

The 2026-10-23 01:09–01:13 UTC PRD inventory still reports seven exact
terminal-lost pairs (Mac 3, Linux 1, Sandbox 3, Factory 0), no other active or
resumable work, and no recovery disposition. PRD service state remained Mac
inactive and Ubuntu workers active. The last Sandbox disk reading was
899,325,952 bytes free, only 50,120,463 bytes above the historical
849,205,489-byte pre-backup threshold; refresh it before any write. QAS remains
active on all four hosts with all eight mailbox paths assigned there and one
retained Mac QAS reservation. No production write, backup, service stop,
recovery, selector mutation, mailbox publication or ACK occurred in this
checkpoint.

Next: deliver the scoped control-tool changes through primary Gitea, exact
GitHub mirror verification and clean Ubuntu fast-forward pulls. Run Sandbox's
full Linux/ARM64 checks, Factory's full Linux/AMD64 checks and Linux's focused
CAT-06/P025/P101 plus native checks on that exact revision. Keep the candidate
release frozen. Then refresh every PRD
preflight and ownership marker, capacity-check each write, create/validate
consistent backups, stop only selected PRD services, and run exact-pair
recovery through `catalog-prd-recover`. Create fresh empty-work preflights and
the cohort hold only after recovery. QAS stays serving; its one active
reservation is preserved.

## Recovery-control source gates and delivery — 2026-10-23 02:08 UTC

The control-tool change is delivered at Mac commit
`ce9ae8929c8bfefaf989f1a0733eec90770af5bb` on `dev`. Primary Gitea accepted
the push; GitHub `dev` was verified at the same SHA. All three Ubuntu
`/home/ubuntu/projects/remote-session-runner` checkouts were clean on `dev`
before fast-forward and are clean at this exact commit. The unrelated
untracked `src/cmd/catalog-holdcheck/` directory remains untouched. This
control-only change is not part of the sealed service bundles; the frozen
candidate `cat-20261022-7a42f3b`, its hashes and prior Dev acceptance remain
unchanged. QAS acceptance for that candidate remains DEFERRED, NOT PASS.

| Machine | Platform/toolchain | Commands and result |
| --- | --- | --- |
| Mac | darwin/arm64, Go 1.27.1 | Focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane`; full `make test`, `make vet`, `make build`, and `make smoke` — PASS. The first sandboxed full-suite attempt was blocked by denial of the existing test-owned `/bin/ps` child; the full suite passed after normal test process-inspection permission was granted. A direct package attempt without the configured shared cache was denied before tests by the sandbox. |
| Sandbox | linux/arm64, Go 1.27.1; `TMPDIR=/tmp`, `GOMAXPROCS=1`, `GOFLAGS=-p=1`, `nice -n 19` | Full `make test`, `make vet`, `make build`, and `make smoke` — PASS. Test log SHA-256 `81054ccadfb376a90c55208196e9b303ab9f78b67fcba299f1fbce9a0eb79c65`; native-gate log SHA-256 `a852bfb96177adebe19e5196a18ead37b16ed1feccdbb8d53049d59bb011e230`. Minimum observed disk free space during the full suite was 638,640,128 bytes; it stayed above the 500,000,000-byte operating buffer. |
| Factory | linux/amd64, Go 1.27.1, `GOAMD64=v1`, `CGO_ENABLED=1`; `/tmp` hard and soft per-user quota verified by `quotactl_fd` at 2,147,483,648 bytes, not inferred from `df`; `TMPDIR/GOTMPDIR/SQLITE_TMPDIR=/tmp`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `nice -n 19` | Full `make test`, `make vet`, `make build`, and `make smoke` — PASS. Test log SHA-256 `e407e7775a2b41c6a4d2123d9b6e1d278d50beba41c99a0aba67dff25cad5f9b`; native-gate log SHA-256 `a852bfb96177adebe19e5196a18ead37b16ed1feccdbb8d53049d59bb011e230`. |
| Linux | linux/amd64, Go 1.27.1, `GOAMD64=v1`, `CGO_ENABLED=1`; `TMPDIR/GOTMPDIR/SQLITE_TMPDIR=/tmp`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `nice -n 19` | `go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1`; exact P025 `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`; exact P101 `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`; `make vet`, `make build`, and `make smoke` — PASS. The P025/P101 verbose output shows both named tests ran. Log SHA-256 `bc25e128a917523a84e616f4271b6a44784dede3d4112312bce716e249b750ba`. The full Linux suite remains NOT RUN under the amended assignment; earlier interrupted runs remain NOT PASS. |

No Runner service was restarted, no PRD database or selector was changed, and
QAS remained active with all eight mailbox paths assigned to it. No mailbox
request or ACK was published. The next action is the owner-approved PRD leg:
refresh per-host PRD work/ownership/capacity, verify exact retained pairs and
compatibility, then create/validate fresh backups and run only supported
recovery/adoption controls. Keep QAS serving until PRD acceptance passes; only
then prepare the QAS→PRD mailbox return, coordinate client pause/drain, settle
results/ACKs including #348, and proceed to QAS alignment.

## Current checkpoint — 2026-10-23 — Dev accepted; one-time order advances PRD next

The owner approved this release-specific order:
`Dev acceptance → PRD deployment and acceptance → supported QAS→PRD mailbox
handoff → QAS alignment and acceptance`. Dev acceptance is **PASS** for
`cat-20261022-7a42f3b`, source revision
`7a42f3bb6712e5186f855df4bc50493041404841`. The sealed manifest SHA-256 is
`b2c0ec9d47b3ebed966e3ebd115a994865910142170358af95ad9857e8bb4e7c`; per-host
bundle hashes and the complete seven-route Dev evidence are in
[`CAT-04.md`](CAT-04.md). No build occurred during promotion.

The candidate source gates passed against this exact revision using Go 1.27.1:

| Machine | Required source checks | Result |
| --- | --- | --- |
| Mac (`darwin/arm64`) | Full `make test`, `make vet`, `make build`, `make smoke` | PASS |
| Sandbox (`linux/arm64`) | Full `make test`, `make vet`, `make build`, `make smoke` | PASS |
| Factory (`linux/amd64`) | Full `make test`, `make vet`, `make build`, `make smoke` | PASS |
| Linux (`linux/amd64`) | Focused CAT-06/changed-package checks, the exact P025/P101 regressions, native vet/build/smoke checks | PASS |

Earlier interrupted Linux full-suite attempts remain **NOT PASS** and are not
used as qualification evidence; the amended assignment requires Factory's
full Linux/AMD64 suite and Linux's focused/native checks. The prior
`cat-20261022-b637d63` release remains preserved unchanged. The candidate
source revision was delivered through Mac primary Gitea, verified at the exact
GitHub mirror SHA, and fast-forwarded to the three clean Ubuntu `dev`
checkouts before candidate acceptance.

This exception replaces QAS acceptance as a prerequisite for this candidate's
PRD deployment and retained-work recovery only. CAT-05 acceptance for this
candidate is **DEFERRED, NOT PASS**. All CAT-06 PRD protections remain: fresh
per-host inventory and ownership, supported exact-pair recovery with accepted
artifacts, verified consistent backups, candidate/schema/configuration
compatibility, one unambiguous selected-lane admission hold, capacity, tested
rollback, ordered worker-then-Mac switching, and complete PRD postflight.
Do not restore an older database automatically.

QAS is confirmed still active on `cat-20261021-b84caf0` across all four hosts;
it was not restarted or changed during Dev acceptance. The 00:55 UTC read
found one active command slot/reservation on Mac QAS and zero work on the three
worker QAS inventories. Preserve that work. Keep all eight mailbox paths
assigned to QAS while PRD is deployed and after PRD acceptance; do not copy
configs or databases to perform handoff. Only after PRD acceptance should the
supported mailbox return package be validated, clients paused/drained, results
and ACKs settled (including a fresh `#348` disposition check), and ownership
transferred. Resume users on PRD only after handoff postflight, then align and
accept the same frozen release in QAS. The owner must explicitly request any
later mailbox return. No mailbox requests or ACKs were published by the
operator; filesystem-mailbox acceptance remains **NOT RUN**.

The Dev receipt `f64a50ed3593c66fc217d6d431f963a9` is `complete` with 15
transitions and identical SHA-256
`258614552cda7cb70646043eb14d4b500fbfb4d72d83776ab461de792e3bc575` on Mac,
Linux, Sandbox and Factory. Final Dev services were active with empty work
inventories on all four hosts; the four public hold checks returned the exact
operation ID and no error. QAS was active. PRD remains untouched. The latest
Sandbox free-space observation was 929,722,368 bytes; calculate and recheck the
exact requirement immediately before every PRD/Sandbox write.

## Dev recovery and timing correction — 2026-10-22 23:06 UTC

The expired Dev candidate attempt was rolled back through the supported
procedure. Its final rollback-complete receipt for operation
`cb3c172eaba53c87844cd1f82a3c5602` has identical bytes on Mac, Linux, Sandbox,
and Factory (SHA-256
`badbd8c5a13f17a2ad65983e7fefb4e8959e25c892bd829de964b363ad9e629c`). Fresh
read-only checks using the synchronized `b1fce21204c512bc8a68c2e7fb1dcee8eff73755`
operator source reported, for each host: Dev service inactive, selector
restored to `cat-20261021-b84caf0`, schema 40, and all seven work-inventory
counts zero. All four receipts have identical `rolled_back` histories; no
selector, service, database, QAS or PRD state changed during this check.

The source change extends the maximum age of whole-cohort
preflight/fence evidence from 10 to 60 minutes while preserving the separate
two-minute latest-local-preflight-to-selector-mutation limit. The local bound
is now checked before the selector effect as well as while recording the
receipt transition, so an expired local observation fails closed before
mutation. Tests cover both inclusive boundaries and rejection immediately
after expiry. The focused cohort regressions passed on Mac. The first Mac full
suite attempt failed only because sandbox policy denied `/bin/ps` to an
existing test's test-owned child; the failure was preserved in
`/private/tmp/cat06-runnerlocald-mac-20261022-2315Z.log`. Rerunning with normal
process-inspection permission passed full `make test`, `make vet`, `make build`,
and `make smoke` on Go 1.27.1 darwin/arm64. Ubuntu source gates and Git
delivery, a replacement sealed candidate, and Dev acceptance are pending.
Existing `cat-20261022-b637d63` and its sealed bytes remain unchanged and
unselected.

Sandbox had 1,252,507,648 bytes free at the most recent check, compared with the
recorded 849,205,489-byte minimum for the previous operation; this is not a
future-operation guarantee. Recheck the exact capacity formula immediately
before each write. QAS/PRD remain unchanged; all eight mailbox paths remain
assigned to QAS. QAS acceptance for the next candidate has not been performed.
No mailbox request or ACK was published.

## CAT-06 refreshed work inventory — 2026-10-22 10:19 UTC

The supported current-source preflight and bounded read-only database queries
confirm the same seven unreleased terminal-lost pairs, with no queued, running,
resumable, or pending-finalization work. Owner markers still match the
recorded process identities and have no durable cleanup proof; Mac process
groups are absent, while Linux and Sandbox each contain only the recorded
zombie shells. PRD service state is unchanged (Mac inactive, three workers
active). Exact IDs, marker hashes, per-host inventories and query methods are
in the [refreshed evidence](CAT-06-legacy-compatibility-2026-10-22.md#fresh-prd-work-and-ownership-refresh--2026-10-22-1012-1019-utc).

Sandbox capacity at `10:19:01 UTC` is `810,663,936` bytes available on
disk-backed `/dev/sda1` for `/` and `/tmp`; it is 38,541,553 bytes below the
849,205,489-byte current pre-operation threshold. No new backup, stop,
recovery, selector, or database operation is started. Keep caches, retained
work and backups. QAS and all eight mailbox assignments remain unchanged.
CAT-06 remains blocked on the capacity gate and supported exact-pair recovery,
then candidate sealing and Dev → QAS → PRD acceptance.

## CAT-06 backup and Sandbox capacity update — 2026-10-22 10:10 UTC

Supported consistent schema-40 PRD backups were completed and verified on
Mac, Linux, Sandbox, and Factory between 09:59 and 10:05 UTC. Exact paths,
creation times, manifest/database hashes, file counts and verification notes
are in the [backup evidence](CAT-06-legacy-compatibility-2026-10-22.md#consistent-prd-backups-and-fresh-sandbox-capacity--2026-10-22-0959-1010-utc).
The Mac/Linux/Sandbox/Factory manifests are respectively
`b8e4904ee044c91399716e545025697a09141d1772c0453b09d1603ef2a5035e`,
`4a65814ad2f4ab8bd30b00950a9eb7cbf83e151bad5a6f7ea8c2bb621b06836b`,
`311b9da765f6d0dd39783aa271db8475ea919b822e69e2fa66ca9f2243a83be7`, and
`8b6a2d52c704a062b550986eb6d88a229716edf7c02cbc84025bfd9f03b9ec37`.

The owner reported Sandbox `1002M` free. Direct SSH at `10:10:08 UTC`
measured `813,338,624` bytes on `/dev/sda1` for both `/` and `/tmp`, which is
35,866,865 bytes below the current 849,205,489-byte backup/staging plus
500,000,000-byte buffer and 64 MiB selector-reserve threshold. Existing
backups are intact; no new backup, service action, recovery, or selector
change is started under this reading. Recheck before any later disk-writing
operation. No PRD services/databases/configuration/selectors or QAS routing
changed; no mailbox request or ACK was published. CAT-06 remains blocked on
supported retained-pair recovery and its guards, sufficient capacity,
candidate sealing, and successive Dev → QAS → PRD acceptance.

## CAT-06 source gates and compatibility recheck — 2026-10-22 09:30 UTC

The required source gates and read-only legacy compatibility checks now pass
at the current delivered source. Source implementation revision is
`638e3512bb22e5347eea3228239f88f730a1c424`; the synchronized Mac and three
Ubuntu `dev` checkouts were at `812a742290e2f0584791cd08f5746518f8e73310`,
with clean tracked trees. The unrelated untracked
`src/cmd/catalog-holdcheck/` remains preserved on Mac. All hosts used Go
1.27.1 and reused their existing Go caches.

| Host | Platform | Required source checks and result |
| --- | --- | --- |
| Mac | Darwin/arm64 | Full `go test ./... -count=1`, `make vet`, `make build`, and `make smoke` passed. The first sandboxed full-suite run could not inspect a test-owned child with `/bin/ps` (`operation not permitted`); the same full suite then passed with the required process-inspection permission. This was an execution-environment restriction, not a test assertion failure. |
| Sandbox | Linux/arm64 | `nice -n 19 make test`, `nice -n 19 make vet`, `nice -n 19 make build`, and `nice -n 19 make smoke` all exited 0 at 09:19:47–09:23:50 UTC. Environment: `/tmp`, `GOMAXPROCS=1`, `GOFLAGS=-p=1`; sampled steal peaked at 15% while the suite progressed. |
| Factory | Linux/amd64 | `make test`, `make vet`, `make build`, and `make smoke` all exited 0 at 08:40:46–08:44:24 UTC. Environment: `/var/tmp`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `GOAMD64=v1`, `CGO_ENABLED=1`; sampled steal peaked at 58% while packages progressed. |
| Linux | Linux/amd64 | Focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1`, exact P025 `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`, exact P101 `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`, `make vet`, `make build`, and the single retried `make smoke` all passed. The smoke retry ran 09:25:08–09:27:20 UTC with `/tmp`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`; steal peaked at 94%, but the command exited 0. The full Linux suite remains historically INTERRUPTED / NOT PASS; the owner reassigned that full-suite evidence to Factory, so it is not relabeled or repeated. |

All four current-source
`catalog-compatibility-check --host HOST --legacy-compatibility-file FILE`
commands returned `read_only_no_backup`, schema 40, and exit 0. The outputs
bind the tested legacy bundle/configuration to current read-only host state:

| Host | Legacy bundle SHA-256 | Configuration SHA-256 | Service active | Work-inventory SHA-256 |
| --- | --- | --- | --- | --- |
| Mac | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | no | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Linux | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | yes | `5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d` |
| Sandbox | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | yes | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Factory | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | yes | `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b` |

At 09:29:42 UTC Sandbox had 1,030,074,368 bytes free on `/dev/sda1`,
180,868,879 bytes above the current 849,205,489-byte pre-backup requirement.
This is a live point measurement, not reserved capacity; remeasure immediately
before every backup, recovery, or selector operation. No backup, receipt,
hold, selector, service, database, mailbox or retained-work state changed
during these source and compatibility checks. QAS and its eight mailbox paths
remain in service. The source gate is complete, but CAT-06 remains blocked on
fresh exact retained-pair disposition/ownership evidence, supported recovery,
backup and rollback gates, a sealed cohort, and successive Dev → QAS → PRD
acceptance. Mailbox requests and ACKs remain unpublished; filesystem mailbox
acceptance is NOT RUN.

## Owner source-test assignment amendment — 2026-10-22 08:17 UTC

The owner approved using **existing Factory for the complete Linux/AMD64
source suite** and retaining focused and host-specific verification on the
CPU-starved Linux host. Only the `linux-host` full-suite source-test requirement
is reassigned. Historical Linux timeouts/interrupted suites remain **NOT PASS**;
do not retry them merely to obtain a host-level full-suite PASS label.

The [current source-test matrix](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md#owner-source-test-assignment-amendment--2026-10-22-0817-utc)
requires a full Factory test/vet/build/smoke result on the actual candidate's
delivered source, exact Go version and recorded build settings. Earlier full
Factory passes at `9113b44` precede the capacity-policy correction `638e351`;
the later focused capacity tests do not relabel those earlier full results.
Mac and Sandbox full-source requirements are unchanged.

Linux retains focused PRD/catalog/changed-package and public-command fixtures,
the exact P025/P101 regression cases named in the plan, and native
vet/build/smoke plus applicable already-required host checks. Record which
tests actually ran and investigate any real assertion failure. Record
platform, `GOAMD64`, `CGO_ENABLED` and relevant flags; do not infer unrun race
or host-specific coverage from the representative Factory suite.

The revised source gate can close after Factory's representative suite and
Linux's retained native checks pass; Linux's interrupted full run is no longer
an independent sealing prerequisite. This does not close the retained-work,
compatibility, backup, capacity, rollback or four-host release-acceptance gates.
The separate operating buffer remains **500,000,000 bytes** plus the measured
backup/selector allowance. Existing users stay on QAS and all eight mailbox
paths remain assigned to it until the owner requests their return.

At this documentation amendment no executable test, candidate seal, service,
selector, recovery, database, mailbox or ACK operation was performed. Current
candidate-source tests and live deployment remain pending their actual
results. The unrelated untracked `src/cmd/catalog-holdcheck/` is preserved.
Documentation verification is `git diff --check` plus amendment consistency
and local-link checks; delivery follows the primary/mirror/clean-worker route.

## Owner capacity-policy amendment — 2026-10-22 08:08 UTC

The owner explicitly reduced the separate operating buffer to **0.5 GB =
500,000,000 bytes**. This supersedes the former 2 GiB operating-buffer rules
in the dated records below. The source guard and its regressions now use the
new amount; the native selector reserve remains **64 MiB = 67,108,864 bytes**.
The `catalog-host-preflight` flag still takes that selector reserve, not the
operating buffer. No backup, installer, selector, service or mailbox operation
is authorized or performed by this policy-only change.

Read-only Sandbox file-size inspection explains the prior 349,205,489-byte
allowance:

| Component | Bytes | Meaning |
| --- | ---: | --- |
| Existing DB | 118,636,544 | Persistent runtime data, not source code |
| WAL + SHM | 4,432,960 | SQLite sidecars |
| Twice DB + sidecars | 246,139,008 | Conservative consistent-backup budget |
| Installed compiled binaries | 35,951,479 | Six existing native executables |
| Configuration, credentials and service definition | 6,138 | Preserved rollback inputs; contents not read |
| Selector reserve | 67,108,864 | Space kept free, not a file copy |
| **Backup allowance plus selector reserve** | **349,205,489** | Existing estimator, unchanged |
| New operating buffer | 500,000,000 | Owner-selected additional reserve |
| **Required pre-backup availability** | **849,205,489** | Freshly remeasure before live operations |

The twofold database term is conservative budgeting, not a claim that two
permanent database backups are written. The backup creates one consistent
snapshot and publishes it by hard link. The catalog is already sealed; no
second executable staging copy is needed for selector activation. No database
shrink, cleanup or backup-policy change is included here.

The read-only Sandbox observation found 903,520,256 bytes available, leaving
54,314,767 bytes above the revised total. This is an observation-time margin,
not reserved space or deployment acceptance. Linux's full-suite gate, exact
retained-pair recovery, fresh backups and candidate Dev/QAS acceptance remain
separate. QAS continues serving the eight handoff paths.

Mac verification passed for both affected packages:
`go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1`
(28.125s and 3.386s), using Go 1.27.1 and the existing shared caches.
`go vet` passed for the same two packages; `gofmt` and `git diff --check` pass.
Regression coverage retains arithmetic/overflow and the unchanged selector
reserve; the observed Sandbox case now requires exactly 849,205,489 bytes.
This does not relabel any interrupted Linux full suite PASS or establish
acceptance for a newly sealed release.

Policy source `638e3512bb22e5347eea3228239f88f730a1c424` was committed on Mac,
pushed to primary Gitea `origin/dev`, verified at the identical automatic
GitHub mirror SHA, and fast-forwarded into all three clean Ubuntu `dev`
checkouts before focused worker tests. On each host the explicit Go 1.27.1
toolchain, normal shared caches, `/tmp` and `GOMAXPROCS=2` ran:

```text
go test ./src/internal/laneinstall -run '^TestPRDCatalog(PreBackupCapacitySeparatesBackupBufferAndSelectorReserve|HostPreflightRejectsOperatingBufferAsSelectorReserveBeforeBackup)$' -count=1 -v
```

Both capacity-policy tests passed on Linux (0.200s), Sandbox (0.034s) and
Factory (0.006s). Worker verification is intentionally focused: no full suite,
vet/build/smoke gate or live deployment at the new source is claimed. Existing
Linux starvation/interruption evidence remains unchanged. The implementer
must rebuild the operator/candidate from this updated source and complete the
remaining required release gates; old compiled tools retain their old policy.
No installed service, mailbox, ACK, backup, database or selector was changed,
and the unrelated untracked `src/cmd/catalog-holdcheck/` remains untouched.

## CAT-06 checkpoint — 2026-10-22 07:56 UTC — compatibility inputs validated; Linux full suite interrupted; PRD remains blocked

Revision `9113b44fc436014c4017522ca2c2432ed66c79b9` remains the clean source
revision on Mac `dev`, primary Gitea, the exact GitHub mirror, and all three
Ubuntu `dev` checkouts. The untracked `src/cmd/catalog-holdcheck/` remains
untouched. The source contains the public-command subprocess harness and
read-only compatibility-input checker.

The corrected Linux P025 test and the previously slow P101 test both pass at
this revision. One full `make test` attempt started at 07:46:04 UTC; the
command packages and `runner-lane` passed, but no further package output
arrived before host load reached 19.67 and CPU steal 93%. SIGINT was sent only
to task-owned Go PID 4133338. The make target exited 2 and remains
**INTERRUPTED / NOT PASS**. No assertion failure is established. Linux vet,
build, and smoke did not run after the test gate stopped the sequence. At
07:50:13 UTC all three Runner services were active, no test process remained,
and the synchronized checkout was clean at `9113b44`. Detailed output and
previous attempts remain in the [semantic test evidence](CAT-06-legacy-semantic-test-evidence-2026-10-22.md).

The exact command was `TMPDIR=/tmp GOTMPDIR=/tmp SQLITE_TMPDIR=/tmp
GOMAXPROCS=2 GOFLAGS=-p=1 make test`. The start samples showed CPU steal of
7%, 0%, and 18%. The observed package results were `cmd/catalog-probe-once`
(cached), `cmd/lane-preflight` (cached), `cmd/runner` (no test files), and
`cmd/runner-lane` (38.402 seconds). The suite then produced no further package
result before the 93% steal observation at 07:48:59 UTC. At 07:50:13 UTC,
`/tmp` had 15,610,396,672 bytes free, all Runner services were active, the
checkout remained clean, and no test process remained.

Mac, Sandbox, and Factory current-revision full gates passed; Mac's focused
CAT-06 tests also passed. Four mode-0600 compatibility inputs bound to the
reviewed evidence SHA-256
`99ccfc61ae6756895d253889135d8b11d6214f3787772f62524836bde0a10f33` passed
`catalog-compatibility-check` on all four hosts in `read_only_no_backup` mode.
Their per-host hashes and paths are in the
[compatibility record](CAT-06-legacy-compatibility-2026-10-22.md#compatibility-inputs-and-refreshed-retained-work--2026-10-22). No operational preflight or backup was run.

Sandbox had 906,756,096 bytes free at 07:56:08 UTC versus 2,496,689,137 bytes
required for the measured backup/staging estimate plus the separate 2 GiB
operating buffer, a 1,589,933,041-byte shortfall. The selector argument stays
67,108,864 bytes (64 MiB); the 2 GiB buffer is separate. Do not start a
backup or selector operation until the measured total fits.

Fresh read-only inventories still show seven retained lost pairs (Mac 3,
Linux 1, Sandbox 3, Factory 0), with no durable stop, cleanup, or release
proofs. Their current identities, markers, and process observations remain
linked above. Do not manually free their capacity; the supported recovery
installer also installs binaries and must be treated as an installation.
QAS and its eight mailbox paths remain in service. No retained work was
recovered/replayed, no PRD backup/selector/receipt/service/database changed,
and no mailbox request or ACK was published. No new candidate was sealed and
Dev→QAS acceptance is not complete; PRD remains blocked.

## CAT-06 checkpoint — 2026-10-22 07:45 UTC — public harness and three platform gates pass; PRD remains blocked

Source revision `9113b44fc436014c4017522ca2c2432ed66c79b9` is on Mac `dev`,
primary Gitea `origin/dev`, the exact automatic GitHub mirror `github/dev`,
and all three clean Ubuntu `dev` checkouts. The untracked
`src/cmd/catalog-holdcheck/` remains untouched. The public-command subprocess
harness and read-only legacy-compatibility validator are part of this
revision.

At this exact revision, Mac's focused CAT-06 tests passed; its full test, vet,
build, and smoke gates had passed in the preceding validation. Sandbox and
Factory each passed full `make test`, vet, build, and smoke, with restricted
concurrency and existing caches. Linux's corrected P025 and P101 targeted
tests now pass, but its full-suite gate remains incomplete: repeated fresh
`vmstat` samples reached 77–99% CPU steal with all three services active. One
current-revision full suite has not been started during those intervals. The
exact commands, package progress, prior interruptions, and current samples are
in the [semantic test evidence](CAT-06-legacy-semantic-test-evidence-2026-10-22.md).

Four canonical mode-0600 compatibility inputs are generated outside Git and
bound to the exact installed bundle/configuration hashes and semantic evidence
SHA-256 `ea976ed988edd0cbe49e82b6bb6e3e75cdfde515ecf1e34826bdb820c2ae21c7`.
Their current owner-only file hashes and read-only validation results are in
the [compatibility record](CAT-06-legacy-compatibility-2026-10-22.md#compatibility-inputs-and-refreshed-retained-work--2026-10-22). The
Mac, Sandbox, and Factory inputs passed `catalog-compatibility-check` in
`read_only_no_backup` mode. Linux's revised input is not yet validated. No
`catalog-host-preflight` was invoked; that command creates a backup.

Sandbox had 935,182,336 bytes free after its read-only compatibility check.
The current estimate for backup/staging plus the separate 2 GiB operating
buffer is 2,496,689,137 bytes, leaving a 1,561,506,801-byte shortfall. The
CLI's `--required-free-bytes` remains exactly 67,108,864 bytes for the selector
reserve; the 2 GiB buffer must not be passed to that flag.

A fresh read-only inventory again found three retained lost pairs on Mac, one
on Linux, three on Sandbox, and none on Factory. All seven still lack durable
stop, cleanup, and release proofs; Linux/Sandbox owner groups have only zombie
`bash` entries, and Mac's recorded groups were absent. Exact IDs, owner-marker
hashes, and state are linked in the compatibility record. No pair was
recovered, released, or replayed. No PRD backup, selector, receipt, service,
database, configuration, mailbox, or ACK changed. QAS and its eight mailbox
paths remain in service. CAT-06 deployment and release sealing remain blocked.

## Linux full-suite retry — 2026-10-22 06:02–06:15 UTC

The clean Linux `dev` checkout was at
`6ff1d0252358d2fb1d46d2a887b5bd4d7832631c`. PRD, Dev, and QAS `runnerd`
services were active before the run. With Go 1.27.1 and existing caches, the
bounded command used `GOMAXPROCS=2 GOFLAGS=-p=1 TMPDIR=/tmp GOTMPDIR=/tmp
SQLITE_TMPDIR=/tmp make test`. The initial `/tmp` backing filesystem had
15,322,692 KiB available.

`src/internal/dispatcher` passed in 421.356 seconds and
`src/cmd/runner-lane` passed in 0.058 seconds. The run then entered
`src/internal/execution`. `vmstat` at 06:10 UTC reported run queue 68 with 86%
CPU steal; its next sample was run queue 13 with 84% CPU steal. All three
Runner services remained active. To limit additional pressure on the starved
PRD host, SIGINT was sent to the exact task-owned `execution.test` process, then
to the exact task-owned `go test ./...` process. The execution package reported
`signal: interrupt`; `make test` exited 2. This run is **INTERRUPTED / NOT
PASS**, not a test assertion failure. The earlier separate Linux full-suite
timeout in `src/internal/localapi` remains a distinct non-passing result.

After all test processes exited, the task-owned `/tmp/go-build889206804` was
absent. The final read-only check at `2026-10-22T06:15:15Z` found all three
services active, `/tmp` backing storage with 15,319,676 KiB free, clean Git
state at the exact revision, and no test process. No Runner service, work,
source, database, selector, or receipt changed.

## Sandbox source gates — 2026-10-22 05:50–05:57 UTC

On `sandbox-host`, the clean `dev` checkout was synchronized at
`4305b6e7300b9a4f195ce35764589a91347dbe77`; its Runner source content is
unchanged from `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`. The explicit Go
toolchain was Go 1.27.1 (`linux/arm64`), and `TMPDIR`, `GOTMPDIR`, and
`SQLITE_TMPDIR` were `/tmp`, which is disk-backed on `/dev/sda1`. Existing Go
caches were reused.

The first `make test` call was issued from the home directory by mistake and
returned `make: No rule to make target 'test'`; it ran no tests. The corrected
call ran from `/home/ubuntu/projects/remote-session-runner` and passed the full
`go test ./...` suite with exit 0, ending at `2026-10-22T05:54:36Z`. Then
`make vet`, `make build`, and `make smoke` each passed at the same checkout
revision. Smoke built each command under its task-owned `/tmp` directory and
removed it after completion. The final read-only check at
`2026-10-22T05:57:03Z` found `/dev/sda1` available bytes `1,137,782,784` (about
1.06 GiB); no test process remained and the checkout was still clean.

The owner's agreement to continue under tight disk headroom applies to these
source gates only. Sandbox remains below the 2 GiB buffer required before
backups or selector operations. No cleanup, backup, release, receipt, service,
selector, database, mailbox, or ACK operation occurred. This closes the
Sandbox source-test, vet, build, and smoke gates; it does not pass the Linux
full-suite gate, the public `runner-lane` subprocess sequence, retained-work
recovery, typed legacy-compatibility inputs, Dev/QAS acceptance, or any PRD
deployment gate.

## Current capacity check — 2026-10-22 05:33 UTC

A fresh strict-host-key SSH observation on `sandbox-host` at `2026-10-22T05:33:50Z`
reported 1,215,143,936 bytes free on `/dev/sda1`, below the required 2 GiB
buffer by 932,339,712 bytes. `ulimit -f` was unlimited; `quota` is unavailable,
so no per-user quota was established. The prior 861 MiB reading is superseded.
No Sandbox mutation or test ran. See the [capacity and legacy compatibility
record](CAT-06-legacy-compatibility-2026-10-22.md#sandbox-capacity-recheck--2026-10-22-0533-utc).

## Retained-work refresh — 2026-10-22 05:37–05:42 UTC

The latest read-only PRD inventories still show seven retained lost pairs:
Mac 3, Linux 1, Sandbox 3, Factory 0. No queued/running/resumable work or
pending recovery-finalization rows were found. Mac recorded PIDs are absent;
Linux and Sandbox recorded process groups contain only their zombie `bash`
entries. Durable stop, cleanup and release timestamps remain empty, so the
pairs still require the supported exact-pair recovery path. Worker PRD services
remain active; the two Mac unsuffixed LaunchAgents are not loaded. No recovery,
service change, backup, selector mutation, mailbox request or ACK occurred.
Full IDs, inventory hashes, owner-marker results and command details are in
the [refreshed compatibility and work record](CAT-06-legacy-compatibility-2026-10-22.md#fresh-prd-retained-work-and-service-recheck--2026-10-22-05370542-utc).

## Current CAT-06 checkpoint — 2026-10-22 05:26 UTC — Factory gates pass; Linux full test gate fails; PRD remains blocked

The Linux and Factory checkouts were clean on `dev` at
`40a62d93d7eaa8bc567482aa3bfbab0dfb93befd` before validation. The Factory
full source gates now pass at this exact revision. The Linux full suite has a
new, separate timeout; its CAT-06 packages passed, and the non-test gates
passed:

| Host | Exact-revision checks | Result |
| --- | --- | --- |
| Factory | `TMPDIR=/var/tmp GOTMPDIR=/var/tmp SQLITE_TMPDIR=/var/tmp make test`, then `make vet`, `make build`, `make smoke` | **PASS** |
| Linux | `make test` (`go test ./...`) | **FAIL** — `src/internal/localapi` timed out after 10 minutes in unrelated `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`, while fixture `store.Open` verified migration checksums. `src/internal/laneinstall` and `src/cmd/runner-lane` passed in that run. After recording the failure, only the remaining task-owned `go test ./...` process was interrupted to limit CPU load on the PRD host; `make test` exited 1. |
| Linux | `make vet`, `make build`, `make smoke` | **PASS** |

The 05:26 source gate remains incomplete because Linux `make test` did not
pass. The previous Linux full-suite timeout in the execution package remains
historical and distinct. No production service or retained Runner work was
stopped. Mac full gates passed at code commit `93e229d`; this evidence commit
changes documentation only. Sandbox's focused PRD/Catalog tests plus vet,
build, and smoke passed at the same source bytes; its full suite remains
unrun. The owner's latest Sandbox reading is 861 MiB free on `/dev/sda1`, below
the requested 2 GiB buffer. No additional Sandbox gates, backups, or selector
operations were attempted.

The current fixture evidence exercises PRD receipt construction, ordered
fences, the underlying reconciliation/rollback/synchronization APIs, and
launcher/wrapper restoration while preserving a database sentinel. A complete
`runner-lane` subprocess sequence for initial legacy adoption, successful
completion, interrupted switching, and reverse rollback on disposable
host-path fixtures has **NOT RUN**. Do not count the API-level fixtures as
that public-command acceptance.

The corrected seven-pair inventory and exact installed config/bundle hashes
remain in
[`CAT-06-legacy-compatibility-2026-10-22.md`](CAT-06-legacy-compatibility-2026-10-22.md).
The typed per-host compatibility inputs still need to be generated and
validated. All seven retained reservations remain without durable stop,
cleanup, or release timestamps. There is no supported recovery result yet.
No new release was sealed, no PRD backup/receipt/hold was created, and no
service, selector, database, mailbox, or ACK was changed. QAS remains serving
with the eight mailbox paths assigned to it.

## Current CAT-06 checkpoint — 2026-10-22 04:45 UTC — corrected compatibility evidence; Sandbox headroom and retained work still block

The owner supplied a fresh Sandbox reading of **861 MiB available** on
`/dev/sda1`. This remains below the requested 2 GiB operating buffer. Do not
start Sandbox source gates, PRD backups, or selector operations until a fresh
measurement meets the required headroom. No data, shared cache, mailbox,
backup, journal, service, selector, receipt, or database was changed in this
checkpoint.

The delivered source correction remains at `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`;
the preceding evidence update is at `40a62d93d7eaa8bc567482aa3bfbab0dfb93befd`.
Both were pushed to primary Gitea, the exact GitHub mirror SHA was verified,
and the three clean Ubuntu `dev` checkouts were fast-forwarded to the evidence
commit. The unrelated untracked `src/cmd/catalog-holdcheck/` directory remains
untouched. The rollback-semantic fixture passed on Mac, Linux, Sandbox, and
Factory at `40a62d9`; this was not a live rollback.

The compatibility evidence and corrected retained-work identities
are recorded in
[`CAT-06-legacy-compatibility-2026-10-22.md`](CAT-06-legacy-compatibility-2026-10-22.md).
That record binds the fresh per-host config and legacy-bundle hashes to schema
40, a read-only inspection with the installed source revision, and the
four-host launcher/wrapper/database-preservation fixture result. It is not a
live rollback, backup, or installed-service acceptance. The operational
`CatalogLegacyRollbackCompatibility` input has not yet been generated and
validated for each host; do not treat the Markdown record as that input.

The corrected inventory still contains seven unreleased terminal-lost
reservations. Their current command/session/job states are lost, but no durable
slot-stop, reservation-cleanup, or release timestamps exist. Mac process IDs
were absent and Linux/Sandbox recorded PIDs were zombies; those observations
are not the supported cleanup proof. The exact recovery path is not yet run.
Do not create the PRD receipt or claim an empty work inventory until the
supported recovery records the required disposition. QAS remains serving, all
eight mailbox paths remain assigned to QAS, and no mailbox request or ACK was
published.

No new release was sealed. Candidate qualification remains blocked on Sandbox
headroom, supported disposition of the seven retained pairs, the per-host
operational compatibility inputs and fresh backups. No Dev/QAS/PRD service,
selector, receipt, hold, database, or mailbox state changed.

## Current CAT-06 checkpoint — 2026-10-22 04:23 UTC — arm64 fixture correction delivered; PRD still blocked

The Sandbox run at the previous source exposed a **test-fixture platform
selection defect**: on Linux/arm64 the test selected the `linux-host`
`linux/amd64` plan, and the expected fixed-platform guard rejected it before
checking the isolated backup snapshot. The production code and installed PRD
were not changed. The regression now selects `sandbox-host` on Linux/arm64,
`linux-host` on Linux/amd64, and `mac-control-plane` on Darwin/arm64.

The correction is commit `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb`. Mac
primary Gitea `origin/dev` and GitHub `github/dev` both resolve to this SHA.
The `linux-host`, `sandbox-host`, and `factory-sandbox` checkouts were clean on
the predecessor before an explicit-identity `pull --ff-only`; each is now clean
at this exact SHA. Mac's unrelated untracked `src/cmd/catalog-holdcheck/`
directory remains untouched and unstaged.

| Host | Validation at `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb` | Result |
| --- | --- | --- |
| Mac | `make test`, `make vet`, `make build`, `make smoke`, Go 1.27.1 `gofmt`, `git diff --check` | **PASS** |
| Linux | `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run '^(TestPRD|TestCatalog)' -count=1` | **PASS** |
| Sandbox | Same focused PRD/Catalog test command; `make vet`, `make build`, `make smoke` | **PASS** |
| Factory | Same focused PRD/Catalog test command | **PASS** |

Factory's full `make test`, vet, build, and smoke passed at predecessor
`a43a862`; Linux's vet, build, smoke, and focused tests passed there. Linux's
full `make test` at that predecessor remains **FAIL** at Go's 10-minute timeout
in unrelated `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice` during
fixture SQLite migration. The full Linux suite was not rerun at the test-only
correction. Sandbox full `make test` was not run; the CAT-06 focused suite and
all three non-test build gates above passed. These limits are not PASS claims.

Sandbox capacity observations: the owner's pasted `df -h` showed 861 MiB free.
Strict-host-key-verified SSH at `2026-10-22T04:14:06Z` measured 880,996 KiB
free on `/dev/sda1`; `/tmp`, `/var/tmp`, and `/home/ubuntu` share that ext4
filesystem, and `quota` is not installed. After the focused PRD/Catalog tests,
vet, build, and smoke, the latest reading at `2026-10-22T04:21:23Z` was
1,280,228 KiB free. The shared Go cache and all retained data were preserved.
This remains below the previously requested 2 GiB operating buffer. No Sandbox
backup, candidate, selector, receipt, hold, or service operation was attempted.

No new release was sealed. The exact legacy binary/configuration compatibility
report and tested data-semantic evidence remain outstanding; schema 40 alone
does not establish compatibility. The exact seven retained pairs and their
ownership markers from the prior inventory have not been refreshed in this
checkpoint, and supported recovery has not run. No production backups, receipts,
admission holds, selectors, services, databases, mailbox contents, or ACKs were
changed. QAS remains serving, all eight mailbox paths remain assigned to QAS,
and existing `cat-20261021-b84caf0` and historical `ebc4446` are unchanged.

## Current CAT-06 checkpoint — 2026-10-22 00:51 UTC — delivered source `a43a862`

The PRD bootstrap/reconciliation source correction and platform-aware fixture
fix are delivered on `dev` at `a43a862ba27e818f0ece0cc3a2a5a481723d40f7`.
Primary Gitea and the automatic GitHub mirror resolve to that exact SHA; all
three Ubuntu `dev` checkouts are clean at that SHA. The untracked
`src/cmd/catalog-holdcheck/` directory remains untouched and unstaged.
No CAT candidate was sealed, no selector or service was changed, and no PRD
backup, receipt, or admission hold was created.

Source validation at this exact source tree:

| Host | Required checks | Result |
| --- | --- | --- |
| Mac | `make test`, `make vet`, `make build`, `make smoke`, `gofmt`, `git diff --check` | **PASS** |
| Factory | full `make test`, `make vet`, `make build`, `make smoke` | **PASS**; temporary test files used disk-backed `/var/tmp` because this host mounts `/tmp` as tmpfs |
| Linux | focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run '^(TestPRD|TestCatalog)' -count=1`, `make vet`, `make build`, `make smoke` | **PASS** |
| Linux | full `make test` (`GOFLAGS=-p=1`) | **FAIL** — Go's 10-minute `src/internal/execution` timeout in unrelated `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`, while fixture `store.Open` applied migrations (`modernc.org/sqlite` B-tree insert). No assertion failure. Remaining task-owned test process was stopped after this failure; only its unique `/tmp/go-build91547467` was removed after process exit. |
| Sandbox | source tests and build gates | **NOT RUN** — fresh `/dev/sda1` availability was 443,699,200 bytes. The measured PRD backup requirement is 349,205,489 bytes, leaving only 94,493,711 bytes; do not risk filling this filesystem with Go tests. |

The owner previously pasted a Sandbox observation with 1.9 GiB available; a
fresh direct measurement on the same `oracle-gustaw-janecki-ubuntu-flex-02`
now reports 443,699,200 bytes free. No cache, retained record, backup, mailbox,
or journal data was removed. Recheck before any future operation.

### Fresh PRD baseline and capacity

The four installed native bundles report build revision
`00c1ca6e7694842c35d398583b3f26fea59e3613`; that commit exists locally and is
an ancestor of the current source. Every listed service binary reported that
revision. All four databases report schema 40. The hashes identify installed
bytes only and do not establish compatibility by themselves.

| Host | PRD service | Config SHA-256 | Legacy bundle SHA-256 | Available / required bytes | Canonical work counts `(sessions, slots, reservations, queued, running, resumable, finalizations)` | Inventory SHA-256 |
| --- | --- | --- | --- | ---: | --- | --- |
| Mac | stopped | `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 147,109,834,752 / 392,873,782 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Linux | active | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 15,768,182,784 / 126,470,672 | `(0,1,1,0,0,0,0)` | `5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d` |
| Sandbox | active | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 443,699,200 / 349,205,489 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Factory | active | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 34,951,086,080 / 108,012,935 | `(0,0,0,0,0,0,0)` | `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b` |

Fresh PRD preflight used the Go 1.27.1 source command and its read-only SQLite
opener on Mac, Linux, and Factory. Sandbox hashes were freshly recomputed from
the config and exact binary files without writing to its nearly full disk;
its exact work rows were read in a compatible SQLite read transaction. The
host's system `sqlite3` library rejected the Linux schema's `STRICT` table
syntax, so Linux work details were instead read using the repository's Go
1.27.1 `modernc.org/sqlite` driver, `mode=ro`, `query_only=ON`, and one
consistent transaction. No normal `store.Open`, migration, checkpoint, or
database write was used.

### Refreshed retained-work pairs

**Correction:** the session-to-command/job associations in the table below
were transcribed incorrectly. Keep this historical snapshot for audit, but do
not use it for recovery. The corrected four-host inventory, owner identities,
marker digests, and process observations are in the linked
[`2026-10-22 compatibility evidence`](CAT-06-legacy-compatibility-2026-10-22.md#fresh-retained-work-and-ownership-observations).

All seven commands, sessions, and jobs remain terminal `lost`; command output
is incomplete, teardown is `lost`, and every command-slot stop/release and
session-reservation cleanup/release timestamp is unset. There are no queued or
running commands, resumable jobs, or pending recovery finalizations. The
owner-only runtime markers remain mode 0600 with one link and no recorded
lost-recovery cleanup confirmation. These are retained ownership records, not
authorization to release capacity.

| Host | Session → command → job | Runtime generation; owner PID/process observation |
| --- | --- | --- |
| Mac | `sess-cf91a7902aceebd6ef20daaf6f090514` → `cmd-a66ce02cbaddf294e13afb3a409c317c` → `job-c40088894069e76f12650ea3bb0376bc` | `c1e43518ad81e68495c65e69b996ec02`; PID 54385 absent; marker SHA-256 `d47128d18d241ab9c0364a08df609c4f89657a33cd5fe5b6c4c189926bfb4631` |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` → `cmd-9e2033c5d6106b2e88f2cff200d7dbfe` → `job-2b4d2e3c9682d54e89342f7a486b5ddd` | `91e709684a1f4b02c35ab53c9d73b30c`; PID 12701 absent; marker SHA-256 `582aa7f474104a738a03368664630d2d453855e896f944b8d9d419a7693e9c91` |
| Mac | `sess-a68baa8f1dd9e66c2871e128002240ef` → `cmd-b524b94074ee26855f1778d52c9655cf` → `job-d46393c75fc5a21dc2fa38a58329d2bc` | `337ab8c2bd8e79f6f369ecf3e934cf2a`; PID 22367 absent; marker SHA-256 `02862e07e173fa4d75071b821179bd3c48c552904d593d83af4a9ca4972ced93` |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` → `cmd-6618114b0f1100e4d50479cf0e57ea2c` → `job-a73371770d4d29e2fc22fafd135396e9` | `linux-6783b82a3e9f846e79ca28e4c3085e96`; PID/PGID 118705 is a zombie whose start identity matches the marker; marker SHA-256 `7f4fe1a17bc04bf9bb50b94f917d4c10d21ae4b606bfb6c693d967ec2ef86b2e` |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` → `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` → `job-9b7cfd9b07a1edc1a2b1d8ffcf6c6cfe` | `linux-c63e12bbd7246fd7c41773c188c8a958`; PID/PGID 2851086 zombie; marker SHA-256 `c932f38efb1695dfdc8f34e2e4663ad7ffcb2f74ea857a8443dbb4b86597ef49` |
| Sandbox | `sess-5abb5d808cd5623f3dbd49202f15822c` → `cmd-8cf6db5bb3871e721133825520367598` → `job-77ea4fda0582a7e9d3e49d269830d6ba` | `linux-9e7b4ccbe132f0fce46d51f1636fe92f`; PID/PGID 3969302 zombie; marker SHA-256 `e060c9e338cb8d59da47bcae7dd1ec106098da6c2e89598072dbdb84fa4c8177` |
| Sandbox | `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` → `cmd-a06764547029ef15e4726ebd56a03cdf` → `job-3cb15f466008a37dc2bb719d83a573d4` | `linux-aefd261855c13ca0b8461d575cefb174`; PID/PGID 3253996 zombie; marker SHA-256 `a49c560f83ebe81c1bf65e7b156f051f268dc60982bc06398c1a5955b7f8d0d5` |
| Factory | none | No unreleased work rows or ownership markers |

Mac's absent process IDs and Linux/Sandbox zombie states are observations, not
the persisted supported stop proof: the DB timestamps remain unset. The
supported recovery must revalidate exact process-group ownership and write
its own cleanup/stop evidence. No result, history, workspace, marker, or ACK
was changed.

### Deployment gates still open

The legacy compatibility report has **not** been generated or accepted. It
must bind each exact old bundle/configuration hash above to a private,
consistent database backup and tested data semantics; schema 40 is not that
evidence. Sandbox needs more disk-backed headroom before its source suite and
any backup. The sealed candidate and final per-host hashes do not exist yet.
Dev qualification and then QAS qualification remain required; QAS users are
active, so the owner must coordinate their pause and drain before any QAS
restart. Fresh selected-lane guards, supported retained-work recovery,
consistent backups, synchronized PRD receipt/hold, cohort switching and
postflight remain unrun. Existing b84caf0 and historical ebc4446 releases are
unchanged; all eight mailbox paths remain assigned to QAS. No mailbox request
or ACK was published.

## Current engineering update — 2026-10-21 23:03 UTC: bootstrap and rollback-source work implemented; no candidate sealed

Source work based on Mac `dev` predecessor `b5683e69507a58a93827c57500da4a315e1b6c3d` now adds a supported initial PRD cohort path. `catalog-host-preflight --lane prd` captures a read-only PRD snapshot, writes a consistent online backup, then verifies that the schema and canonical seven-field work inventory in the backup match the live preflight. The coordinator can create a PRD receipt without a pre-existing PRD hold; four ordered `fence-host` transitions stop and record each selected unsuffixed service, and only then may the receipt enter `held`. The public `preflight` action requires that exact synchronized hold.

The catalog and PRD recovery paths now hash one canonical work-inventory representation covering active sessions, command slots, reservations, queued and running commands, resumable jobs, and pending recovery finalizations. PRD local receipt validation distinguishes an unselected legacy selector, selected catalog state, partial switching, and recorded rollback. Rollback reconciliation checks the matching selector journal, recorded rollback digest, restored previous selector, preserved configuration/schema, and restored legacy launcher/wrapper bytes from the consistent backup; it does not restore an old database.

Fixture coverage exercises PRD receipt creation and ordered fences, selected/partial-switch reconciliation, reverse rollback with synchronization after each host, rollback completion synchronized to all four fixture hosts, and restoration of legacy launcher/queued-wrapper bytes while preserving a database sentinel. The canonical inventory/backup match and mismatched identity/journal rejection also have regressions. These are source and disposable-fixture results; they do not establish a live PRD backup, exact legacy-binary compatibility, retained-work recovery, service deployment, or functional acceptance.

Mac source gates on this working tree: focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1` **PASS**; `make test` first failed in unrelated `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` because sandboxed `/bin/ps` returned `operation not permitted`, then the full rerun with process inspection **PASS**; `make vet`, `make build`, and `make smoke` **PASS**. `git diff --check` **PASS**. The exact source commit is pending the required Git delivery. No source or service changes were made on Ubuntu, and no PRD/Dev/QAS service, selector, database, backup, receipt, mailbox, or ACK was changed in this turn.

Validation follow-up — 2026-10-21 23:39 UTC: delivery of `140b0e94cd1be817695c77219208bea455133401` exposed a cross-platform fixture bug. Its `TestPRDWorkSnapshotUsesCanonicalFullInventoryAndVerifiesBackup` selected the Mac PRD plan even on Linux; the production platform guard correctly rejected it. The Mac-only focused test passed. The source correction now selects `mac-control-plane` on Darwin and `linux-host` on Linux; that change is not yet delivered or validated on Ubuntu.

The synchronized `make test` run on Factory exited 2 on that one fixture failure. On `linux-host`, the full suite hit Go's 10-minute package timeout first in `src/internal/execution` at `TestP026I04TeardownFailurePreservesCommandOutcomeAndStableRetry`, inside SQLite `fsync` during test fixture `store.Open`; later package timeouts occurred in `src/internal/laneinstall` while creating a fixture schema and `src/internal/localapi` while opening another fixture. The live `ps` snapshots showed these processes under the task-owned `/tmp/cat06.emvA2N/go-build...` tree. The run was terminated after repeated timeouts; its process group exited and the task-owned temporary tree was confirmed removed. The host still had 15,446,192 KiB available on `/dev/sda1` afterward. The evidence does not establish why these unrelated full-suite SQLite fixtures were this slow. The full Linux and Factory test gates are **NOT PASS**; no vet/build/smoke run followed those `set -e` test failures. Sandbox host gates were not started because `/dev/sda1` had only 532,760 KiB available and its existing 4.3 GiB Go cache was preserved. Focused CAT-06 tests and remaining host gates still need validation against a delivered correction.

No new release was sealed: the existing accepted `cat-20261021-b84caf0` and historical `ebc4446` artifacts remain untouched. All eight mailbox paths remain assigned to QAS under the active handoff; QAS was not restarted or reconfigured. Mailbox request/ACK publication and mailbox acceptance remain **NOT RUN**.

Validation update — 2026-10-21 23:43 UTC: the platform-aware fixture correction is present in the Mac working tree but not yet delivered. On that exact working tree, full `make test`, `make vet`, `make build`, `make smoke`, `git diff --check`, and the Go 1.27.1 `gofmt` check all **PASS** on Mac. The test command compiled the pre-existing untracked `src/cmd/catalog-holdcheck/` directory; it remains untouched and unstaged.

Fresh host preflight on 2026-10-21: Linux host `/tmp` is on `/dev/sda1` with 15 GiB available; Factory `/tmp` is a 4 GiB tmpfs (3.4 GiB available), while its disk-backed `/var/tmp` is on `/dev/sda1` with 33 GiB available. Do not put task temporary files on Factory tmpfs; use its disk-backed temporary area for source checks. Sandbox `/tmp` is on `/dev/sda1` and has only 489 MiB available, so do not start its tests or backups yet. This fresh measurement supersedes the earlier 532,760 KiB reading and differs from the owner's pasted 1.5 GiB snapshot. No shared cache or host data was removed.

Before a candidate can be sealed or PRD can change, complete Git delivery and the host source gates, generate and verify host-specific legacy rollback compatibility evidence bound to each exact PRD binary-set/configuration hash and tested data semantics, refresh exact retained-work identities and ownership/stop evidence, and prepare fresh consistent backups/capacity. The owner reports QAS users are active; coordinate their pause and drain before any QAS restart required for qualification. No external publisher pause was inferred from mailbox or directory counts.

## Current status — 2026-10-21 22:01 UTC: PRD migration blocked; mailbox handoff active

CAT-06 PRD catalog migration remains **BLOCKED** on its existing bootstrap,
retained-work and compatibility gates. The separate owner-approved mailbox
handoff is active: eight existing paths are assigned to QAS, while Mac PRD is
stopped with an empty mailbox map and disabled ingress. Its original installed
binaries, database and keys remain preserved; workers' PRD services are
unchanged. This routing change does not complete PRD adoption or promotion.

Dev and QAS now accept the same immutable `cat-20261021-b84caf0` bundles, with
released four-host operations and passing scoped/ordinary API routes. See
[the handoff record](MAILBOX-HANDOFF-2026-10-21.md) for current identities,
focused validation scope and preservation evidence. Clients remain paused.
No filesystem mailbox request or ACK was published; filesystem-mailbox
acceptance remains **NOT RUN**.

## Historical status — 2026-10-21 18:56 UTC: PRD blocked; Dev/QAS independently opened

Dev and QAS now permit normal API use on the unchanged accepted
`cat-20261021-ebc4446` release. Both four-host operations are `released`; all
seven ordinary routes per lane passed, and the final postflight found all
eight host/lane services ready with empty work inventories and zero error
counters. See [Dev/QAS admission opening](CAT-Dev-QAS-admission-open-2026-10-21.md).
PRD process identities, configuration and binary hashes are unchanged. The
CAT-06 bootstrap, retained-work and legacy-compatibility blockers below remain
unresolved; opening Dev/QAS does not satisfy those PRD gates.

No external publisher control was operated; the existing publisher pause
instruction remains in force. No mailbox request or ACK was published, and
filesystem-mailbox acceptance remains **NOT RUN**.

## Historical PRD preflight — 2026-10-21 17:44 UTC

**Status — 2026-10-21 17:44 UTC: BLOCKED before PRD mutation.** The frozen
release `cat-20261021-ebc4446` remains accepted in Dev and QAS. No PRD service,
selector, database, backup, receipt, journal, mailbox, or ACK was changed in
this check. Filesystem-mailbox acceptance remains **NOT RUN**.

## Read-only production baseline

The current PRD preflight used the synchronized source checkout at
`363ef81c4f03dcad3cd5d9c0483845f590e36dc8`; its tracked `src/` bytes match the
accepted ebc4446 source. Mac used the Makefile's Go 1.27.1 toolchain and its
shared Go caches. Worker preflights used the explicit Go 1.27.1 toolchain in
their clean `dev` checkouts and strict SSH host-key verification. The commands
were `runner-lane prd-preflight --host HOST` on each host. This command calls
`ReadPRDPreflight`, which uses the read-only restart-preflight SQLite opener;
it does not use `store.Open`, run migrations, or write the database.

| Host | Service active | Schema | PRD config SHA-256 | Legacy binary-set SHA-256 | Free bytes / preflight estimate | Retained command slots / reservations |
| --- | --- | ---: | --- | --- | ---: | ---: |
| `mac-control-plane` | yes | 40 | `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 146,793,422,848 / 396,106,654 | 3 / 3 |
| `linux-host` | yes | 40 | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 16,201,306,112 / 126,462,432 | 1 / 1 |
| `sandbox-host` | yes | 40 | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 1,401,548,800 / 347,526,129 | 3 / 3 |
| `factory-sandbox` | yes | 40 | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 35,415,683,072 / 108,004,695 | 0 / 0 |

The current exact PRD binary hashes are also present in the emitted preflight
JSON. No installed source revision was inferred from them. All four services
remain on their unsuffixed legacy installation; PRD selectors remain
unselected. Dev and QAS were not changed.

The owner-provided Sandbox transcript shows `sudo rm -rf *` in
`/var/log/journal` and about 1.5 GB free afterward. Runner did not run that
command or modify journal files. A later read-only SSH measurement at
`2026-10-21T17:37:43Z` reported `/dev/sda1` and `/tmp` with 1,422,868,480 bytes
available (98% used). The lower preflight measurement above is the conservative
capacity input. The estimated PRD backup fits at that observation, but no
backup was created. Re-measure the actual requirement immediately before any
backup, stage, or selector operation.

The preflight counters show no active sessions, queued commands, running
commands, resumable jobs, or pending recovery finalizations. They do show
unreleased terminal work reservations: Mac 3, Linux 1, Sandbox 3, Factory 0.
The last exact read-only pair inventory classified all seven retained pairs as
`lost` in command, job, session, and teardown state; their stop/release and
cleanup/release timestamps were unset. The current preflight reconfirmed the
counts but did not re-enumerate pair identities, so exact identities and
process ownership must be refreshed immediately before supported recovery.
No scripts or payloads were read, replayed, deleted, or acknowledged.

The current turn did not independently verify the external publisher controls.
The owner’s pause instruction remains in force; the exact publisher-control
evidence must be captured before PRD admissions are fenced or services are
stopped.

## CAT-06 execution blocker

The accepted ebc4446 operator has no supported way to create the initial PRD
cohort receipt and its host preflights:

- `runner-lane catalog-cohort --action create` rejects PRD at
  `src/cmd/runner-lane/main.go:336-340`.
- `NewCatalogCohortReceipt` accepts only Dev/QAS at
  `src/internal/laneinstall/catalog_cohort.go:171-177`, despite the receipt
  validator supporting PRD receipts.
- `catalog-host-preflight` rejects PRD at
  `src/cmd/runner-lane/main.go:867-870`, and cohort `preflight` rejects PRD
  at `src/cmd/runner-lane/main.go:489-492`.
- `BeginCatalogPRDMaintenance` requires that the same PRD operation already
  be recorded as the unique local admission hold
  (`src/internal/laneinstall/catalog_prd.go:73-82`). This leaves no supported
  command path to produce and synchronize the first PRD hold after consistent
  backup and quiescence checks. The host-local `catalog-prd-backup` command
  does not create or synchronize the four-host cohort receipt.
- For an unselected legacy PRD selector, `readPRDCatalogProof` deliberately
  sets `readSchemaMax = 0` until tested cohort evidence exists
  (`src/internal/laneinstall/catalog_prd.go:440-445`). The old binaries have no
  catalog manifest declaring their read compatibility. A source change must
  bind the exact legacy bundle identity to actual compatibility evidence; it
  must not fill this field from the database schema number alone.

The seven terminal-lost reservations are a separate work guard. The existing
supported recovery procedure can dispose only freshly revalidated exact pairs
after selected services are stopped. No stop or recovery was attempted here.

## Required correction and sequence

Before selecting PRD, add and test a supported PRD host-preflight and
coordinator receipt path. It must preserve the current PRD restriction on
ordinary installers; require explicit publisher-pause evidence; use fresh
service/work/ownership facts; bind exact legacy binary and configuration
identities; create verified consistent backups; and record tested
previous-binary/current-data compatibility. Receipt creation and synchronization
must remain supported APIs; no manual receipt editing is acceptable.

Because that is a source change, it requires source gates and Git delivery,
then a **new sealed release identity** and renewed full acceptance in Dev and
QAS before PRD. The ebc4446 candidate remains unchanged and must not be
re-labeled to include the correction. After those gates, refresh exact retained
work identities and publisher controls, use only supported recovery, recheck
Sandbox capacity, create and synchronize the PRD hold, then follow the fixed
worker order and final Mac switch. The prior installation/restart authorization
remains scoped to this approved catalog migration, conditional on those gates.

At this evidence point no PRD backup, hold, service stop/restart, release
publication, selector change, functional probe, request, or ACK was performed.

## Fresh PRD/QAS pre-write guard — 2026-10-23 02:19–02:23 UTC

The delivered source checkout is `4223285e5f59a46fde6e633803ec826f7b2b32a2`
on Mac and on all three clean Ubuntu `dev` checkouts. The unrelated untracked
`src/cmd/catalog-holdcheck/` directory remains untouched. The accepted service
candidate is still `cat-20261022-7a42f3b`, source
`7a42f3bb6712e5186f855df4bc50493041404841`, release manifest SHA-256
`b2c0ec9d47b3ebed966e3ebd115a994865910142170358af95ad9857e8bb4e7c`; Dev
acceptance remains PASS and QAS acceptance remains DEFERRED, NOT PASS. Fresh
`catalog-inspect` checks matched the existing per-host hashes recorded in
CAT-04. No service bundle was rebuilt.

Read-only `prd-preflight`, selector inspection and compatibility checks passed
on all four canonical hosts. PRD remains unselected on every host and schema
40. The fresh state is:

| Host | PRD active | Legacy bundle SHA-256 | Config SHA-256 | Unreleased slots / reservations | Other work | Preflight free / required bytes |
| --- | --- | --- | --- | ---: | --- | ---: |
| Mac | no | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | 3 / 3 | No active sessions, queued/running commands, resumable jobs or pending finalizations | `141602156544 / 392873782` |
| Linux | yes | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | 1 / 1 | All other work categories zero | `15394877440 / 126470672` |
| Sandbox | yes | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | 3 / 3 | All other work categories zero | `685555712 / 349205489` |
| Factory | yes | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | 0 / 0 | All work categories zero | `34227179520 / 108012935` |

The three current owner-only compatibility files matched the exact installed
legacy bundle/configuration identities on Linux, Sandbox and Factory; the Mac
compatibility check also passed with schema 40 and no backup. These checks are
read-only and do not create a backup or stop a service.

A fresh read-only SQLite snapshot reconfirmed exactly seven retained
terminal-lost pairs, with no complete-output claim and no slot/reservation
release or cleanup timestamps. The query selected identities, terminal state
and ownership fields only; it did not read scripts, output payloads or
credentials. Linux's system SQLite CLI rejected its unsupported `-json` option
before opening the query, so the exact Linux inventory was read with a
temporary Go 1.27.1 helper using SQLite `mode=ro`, `PRAGMA query_only=ON`, and
one read transaction; the helper was removed after exit. Mac and Sandbox
read-only queries returned the same retained set.

| Host | Session → command | Fresh terminal state |
| --- | --- | --- |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` → `cmd-9e2033c5d6106b2e88f2cff200d7dbfe` | lost / `command_lost`; output incomplete |
| Mac | `sess-a68baa8f1dd9e66c2871e128002240ef` → `cmd-a66ce02cbaddf294e13afb3a409c317c` | lost / `command_lost`; output incomplete |
| Mac | `sess-cf91a7902aceebd6ef20daaf6f090514` → `cmd-b524b94074ee26855f1778d52c9655cf` | lost / `command_lost`; output incomplete |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` → `cmd-6618114b0f1100e4d50479cf0e57ea2c` | lost / `command_lost`; output incomplete |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` → `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` | lost / `command_lost`; output incomplete |
| Sandbox | `sess-5abb5d808cd5623f3dbd49202f15822c` → `cmd-a06764547029ef15e4726ebd56a03cdf` | lost / `command_lost`; output incomplete |
| Sandbox | `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` → `cmd-8cf6db5bb3871e721133825520367598` | lost / `command_lost`; output incomplete |

Every retained pair's current marker remains a regular mode-0600, singly linked
file owned by the service UID. Its recorded session, generation, PID/PGID and
start identity match the current row; none has a cleanup proof. A bounded
process-group read found no recorded group members on Mac, Linux or Sandbox.
This is fresh absence evidence, not a substitute for the sealed recovery
binary's final ownership and cleanup guards. Factory has no retained pair.

QAS was not stopped, reconfigured or written. Read-only status on Mac, Linux,
Sandbox and Factory reports all four QAS services active on
`cat-20261021-b84caf0`. Their configuration hashes remain Mac
`9856c54900e26c335c4ea7adb2d02951847d76349abdd49dc6b261f419f11f6a`, Linux
`3084e76eb6068d2d45fe82d4f259c2eec765628fc8cc15af2670743312bbe91b`, Sandbox
`f69e5df67057c81588c6e553bec9c9d701495b71e878ae52021f4ad0af39cb41`, and
Factory `9dfcadaebf7bfeb9cd1d7b56fc2517ef3e693970a5f5e006d4c228db0694729`.
Mac QAS still has one retained command-slot/reservation and no active session;
the three worker QAS inventories are empty. The supported
`runner-mailbox-handoff status` reports operation
`1f96911399ff6a678345f214ada69b49` active with eight mailbox definitions.
The existing QAS mailbox assignment is preserved; this read-only check did
not pause clients or move ownership.

The latest direct Sandbox capacity sample at `2026-10-23T02:23:51Z` measured
`668127232` bytes free on `/dev/sda1` (also `/tmp`). The current pre-backup
requirement is `849205489` bytes: measured backup allowance plus the separate
`67108864`-byte selector reserve, and the approved `500000000`-byte operating
buffer. Sandbox is short `181078257` bytes. No backup, PRD admission hold,
service stop/restart, recovery, selector change, install, mailbox publication
or ACK was attempted. Capacity is the immediate write blocker; increase safe
Sandbox free space to at least the measured requirement and recheck it directly
before the first backup. Preserve retained work, existing backups and shared
caches.

Next action after capacity passes: collect fresh per-host PRD backups and
preflights, synchronize the supported PRD admission hold, recover only these
seven exact pairs through the sealed candidate, then continue the authorized
PRD cohort switch and acceptance. Keep QAS serving and all eight mailbox paths
assigned there until PRD acceptance and the separately coordinated supported
handoff.

## CAT-06 compatibility correction — 2026-10-23 13:40 UTC — Mac gates pass; host delivery pending

The source worktree at starting revision `4be93fac8bd8394ce1e7e581bdc92be2b7fcfa55`
implements the owner-approved compatibility correction. The changes use one
configuration-aware policy binding for probe preparation and all service
guards; bind the exact environment and loaded configuration SHA-256 into the
one-time scope; retain strict V3 environment/pin/SAN checks; use shared
trusted-CA, hostname-verifying TLS 1.3 client defaults for legacy direct TLS;
and treat a result as accepted only when success, exit zero, complete
non-truncated output, a final event and confirmed teardown are all present.
Failure results are emitted before the command returns nonzero.

The PRD replacement path now has explicit replacement-preflight and
replacement-fence commands for an exact fully switched `verifying` predecessor.
It requires a stopped service, fresh successor backup, unchanged selector,
configuration, schema and empty work inventory, and binds the new rollback
baseline to the exact predecessor bundle. A dual receipt hold is recognized
only for the matching predecessor/successor pair. Supported per-host
supersession evidence and final synchronization retain both histories and keep
admissions closed until finalization. The public-command subprocess fixture
exercises adoption and completion, interruption/reconciliation, reverse
rollback, a verifying predecessor replacement, partial supersession across a
fixture process restart, synchronization, a second interrupted switch, and
rollback to the selected predecessor without changing database bytes.

Mac Go 1.27.1 gates:

| Gate | Result |
| --- | --- |
| Focused replacement subprocess and probe binding/result/TLS regressions | **PASS** |
| `make test` (full `go test ./...`) | **PASS**, exit 0 on the authorized rerun. The first sandboxed run exited 2 because the existing `runnerlocald` process-settlement test could not execute its read-only `/bin/ps`; `runnerlocald` passed outside the sandbox and the complete suite then passed. |
| `make vet` | **PASS**, exit 0 |
| `make build` | **PASS**, exit 0 |
| `make smoke` | **PASS**, exit 0 |
| `git diff --check` | **PASS** |

The Mac source, design, plan and evidence changes are not yet committed or
delivered; the final source commit is pending. The focused config-binding
regressions cover legacy V1/V2 resolution, strict V3 lane names, and rejection
of environment/configuration-hash drift. Factory full Linux/AMD64 gates,
Sandbox full Linux/ARM64 gates, and Linux focused CAT-06/P025/P101 plus native
gates are **NOT RUN** for this correction. Host sync, new release sealing, Dev
acceptance, PRD replacement/deployment/acceptance, mailbox handoff and QAS
alignment are also **NOT RUN**. No live service, selector, database, backup, receipt, mailbox assignment,
request or ACK was changed in this source-gate checkpoint. Disposable test
fixtures exercised receipt transitions and were confined to test temporary
directories. The untracked `src/cmd/catalog-holdcheck/` directory remains
untouched. The old sealed `cat-20261022-7a42f3b` remains unchanged.

The correction candidate uses the owner-approved one-time order: Dev acceptance
→ PRD acceptance → coordinated QAS→PRD mailbox handoff → QAS alignment and
acceptance. QAS acceptance remains DEFERRED, NOT PASS, until alignment. Keep
QAS serving with all eight mailbox paths assigned there and publish no mailbox
requests or ACKs. Before live PRD work, recheck current work and ownership,
per-host capacity, exact predecessor selector journals, backups/configuration/
schema identities, and supported exact-pair recovery evidence. The earlier
PRD inventory and Sandbox capacity numbers above are historical, not current
preflight results.

## CAT-06 corrected candidate sealed; fresh Dev/QAS baseline — 2026-10-23 14:33 UTC

After source-gate delivery `3976a476a34399d0ff6048736e1d7820261f06c6`, one
new candidate was built from frozen runtime source
`c01e6339e9330ff6bf7dbde042b1cd5287c55c4e` with Go 1.27.1 and published using
the supported `runner-lane catalog-manifest` / `catalog-publish` commands.
The new immutable release is **`cat-20261023-c01e633`**, with release manifest
SHA-256 `7a9f705484906c7889c4776da0ef64769662fd0c5360e4d3767ca8021a5c4c81`.
All four hosts report that same release digest and source revision. Per-host
bundle identities are:

| Host | Native platform | Bundle SHA-256 | Catalog location |
| --- | --- | --- | --- |
| Mac | Darwin/arm64 | `a338761463afe7dbe8f558f7166dd10f0d3890a0e9e151c1d906e955221067ee` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261023-c01e633/mac-control-plane` |
| Linux | Linux/amd64 | `9a98537d38c98bcd9e9834ceb6ad23544dc85e8f038aed74f123bad1491df9cb` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261023-c01e633/linux-host` |
| Sandbox | Linux/arm64 | `43a92d91a43c954fd20efc6c5f8e5d5d01fca6487bf0a5da8929c3c73caed049` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261023-c01e633/sandbox-host` |
| Factory | Linux/amd64 | `652b3c428c82c19b8a982e8fe6d0acb71f2734a87dfcfaf480db5530a22799d4` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261023-c01e633/factory-sandbox` |

Each bundle manifest and binary set was locally published, then checked with
`catalog-inspect` against the release manifest's host entry. Linux and Factory
were built natively from the same source but have distinct host-bound bundle
digests. The existing `cat-20261022-7a42f3b` release was not modified. The new
candidate is published but **not selected** in any lane.

Read-only `catalog-status` and `catalog-work-inventory` observations on all
four hosts were captured at 2026-10-23 14:29:42–14:30:21 UTC. All eight lane
databases reported schema 40, and all eight services were active at observation
time. Dev remained selected on `cat-20261022-7a42f3b` with empty work on every
host. QAS remained selected on `cat-20261021-b84caf0`; Linux, Sandbox and
Factory QAS inventories were empty. Mac QAS had one active command slot and
one session reservation, with no active session, running/queued command,
resumable job or pending finalization. That retained QAS state is preserved;
QAS was not stopped, modified, or used for candidate acceptance.

| Host | Dev config SHA-256 | QAS config SHA-256 |
| --- | --- | --- |
| Mac | `2a256f44cf14031b8463854aa77c190ba265a419b624a0538a6c42ba130395d9` | `9856c54900e26c335c4ea7adb2d02951847d76349abdd49dc6b261f419f11f6a` |
| Linux | `842a17572ba218ee770b39d8cf95c442c4885204d479d5b34ef6d2e4e5ff82f5` | `3084e76eb6068d2d45fe82d4f259c2eec765628fc8cc15af2670743312bbe91b` |
| Sandbox | `9a9adcd3236d8a92f2d405ef59f1199180310a03df6c9d246b784a702653d82a` | `f69e5df67057c81588c6e553bec9c9d701495b71e878ae52021f4ad0af39cb41` |
| Factory | `7af6d8ea68764f703b7aebd40966bbc7ff8b86523a2cb948d5f60e68659cd727` | `9dfcadaebf7bfeb9cd1d7b56fc2517ef3e693970a5f5e006d4c228db0694729` |

No selector, service, configuration, database, mailbox assignment, request,
ACK, PRD retained pair, or receipt changed during candidate construction and
these observations. The task-owned temporary bundles and manifests remain
outside Git. `src/cmd/catalog-holdcheck/` remains unrelated, untracked, and
untouched.

Operator command corrections during sealing: the initial Mac build refused an
already-created empty immutable output directory; after confirming it was
empty, only that task-owned directory was removed and the build succeeded with
the destination absent. The initial Linux build was launched from the wrong
working directory and failed before compilation with `go.mod file not found`;
rerun from the synchronized checkout succeeded. The first post-publish
verification script expected a nested `identity` in `catalog-publish` output;
the supported publication had succeeded, and a corrected read-only validator
then matched publication output, inspection, manifest and bundle digests on
all workers. Mac's sandboxed catalog write returned `operation not permitted`
with no release directory created; the supported publication was retried with
the required sandbox escalation and `catalog-inspect` passed. These command
failures did not change any selected service or prior release.

The next gate is a fresh supported Dev cohort operation: apply the Dev-only
publisher/admission fence, prepare and verify host backups/preflights, switch
workers in the planned order then Mac, and complete direct, queued-SSH,
Mac-local, lifecycle, limits, capacity and isolation acceptance on the exact
sealed bytes. PRD recovery/deployment and QAS handoff have not started. QAS
acceptance remains **DEFERRED, NOT PASS**; filesystem mailbox requests/ACKs
remain **NOT RUN**.

## CAT-06 Factory source gates — 2026-10-23 14:17 UTC

The owner resolved the Factory temporary-path choice: use `/tmp` despite its
tmpfs mount. On `factory-sandbox`, the clean `dev` checkout was at
`1d6473301aa677109d6671655b8c6ec152285fa3`; the runtime source tree is
unchanged from `c01e6339e9330ff6bf7dbde042b1cd5287c55c4e` (the intervening
commit adds evidence only). The primary Gitea `origin/dev` and automatic
GitHub `github/dev` both resolve to `1d6473301aa677109d6671655b8c6ec152285fa3`.
The Factory checkout was clean before the gates. The declared Makefile
toolchain exists and reports `go version go1.27.1 linux/amd64`.

All Factory gates used `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `GOTOOLCHAIN=local`,
and `TMPDIR=GOTMPDIR=SQLITE_TMPDIR=/tmp`:

| Gate | Command | Result |
| --- | --- | --- |
| Full source suite | `make test` | **PASS**, exit 0; full Linux/AMD64 suite |
| Vet | `make vet` | **PASS**, exit 0 |
| Build | `make build` | **PASS**, exit 0 |
| Smoke | `make smoke` | **PASS**, exit 0 |

Immediately before `make test`, `make vet`, and `make build`, `/tmp` reported
3,379,322,880 bytes available of 4,294,967,296. The same availability was
observed before smoke and after all gates. No shared cache, retained data,
service, selector, database, backup, receipt, mailbox assignment, request or
ACK was changed by these source checks. `quota -v` still cannot report a
per-user limit on the tmpfs mount (`Cannot stat() mounted device tmpfs`); the
owner-approved `/tmp` path was used, and the available-byte measurement is not
represented as a per-user quota measurement.

With Factory complete, all assigned source gates for the correction now pass
at `c01e633` runtime source: full Go 1.27.1 suite/vet/build/smoke on Mac,
Sandbox and Factory; Linux changed-package tests, exact P025/P101 regressions,
and native vet/build/smoke. Linux's full suite remains **NOT RUN** under the
amended assignment, and prior interrupted full-suite attempts remain
**NOT PASS**. The source correction is ready for the next candidate-sealing
and Dev-acceptance gates; no release has yet been sealed from `c01e633`.
Dev/PRD command-level acceptance, PRD replacement/deployment/acceptance,
QAS→PRD mailbox handoff and QAS alignment remain **NOT RUN**. QAS continues
serving with mailbox ownership unchanged; QAS acceptance remains DEFERRED,
NOT PASS. The unrelated untracked `src/cmd/catalog-holdcheck/` directory
remains preserved and unstaged.


## CAT-06 source delivery and Ubuntu host gates — 2026-10-23 14:06 UTC — Linux/Sandbox PASS; Factory scratch-path decision pending

The correction source and initial plan/design/evidence commit is
`c01e6339e9330ff6bf7dbde042b1cd5287c55c4e` on Mac `dev`. The primary Gitea
`origin/dev` and automatic GitHub `github/dev` both resolved directly to that
exact SHA. The clean Ubuntu `dev` checkouts were fast-forwarded from GitHub
using the configured explicit identity and verified host keys; each ended at
that same SHA with a clean worktree before tests:

| Host | SSH profile / identity | Checkout | Result |
| --- | --- | --- | --- |
| Linux | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | `c01e6339e9330ff6bf7dbde042b1cd5287c55c4e` | clean `dev` |
| Sandbox | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | `c01e6339e9330ff6bf7dbde042b1cd5287c55c4e` | clean `dev` |
| Factory | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | `c01e6339e9330ff6bf7dbde042b1cd5287c55c4e` | clean `dev` |

Linux on `remote-session-runner` passed the changed-package suite with
`GOMAXPROCS=2`, `GOFLAGS=-p=1`, and
`TMPDIR=GOTMPDIR=SQLITE_TMPDIR=/tmp`:

```sh
go test ./src/cmd/catalog-probe-once ./src/cmd/runner-lane ./src/internal/laneinstall ./src/internal/localapi ./src/internal/runnerclient ./src/internal/runnerd ./src/internal/runnerlocal ./src/internal/runnerlocald -count=1
```

It also passed exact `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`
and `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`, each with
`-count=1 -v`. Native `make vet`, `make build`, and `make smoke` each exited
0. The full Linux suite remains **NOT RUN** under the owner-amended assignment;
prior full-suite interruptions remain **NOT PASS**. An initial `vmstat 1 3`
sample showed 7% steal, and one sample during serial tests showed 19%; no test
timeout or assertion failure occurred.

Sandbox on `sandbox.env` passed full `make test`, `make vet`, `make build`, and
`make smoke` on Linux/ARM64 with Go 1.27.1, `GOMAXPROCS=2`, `GOFLAGS=-p=1`,
and `TMPDIR=GOTMPDIR=SQLITE_TMPDIR=/tmp`. `/tmp` was disk-backed on
`/dev/sda1`: 1,143,054,336 bytes available immediately before the full suite,
1,879,957,504 bytes after it, 1,854,418,944 before `make build`, and
1,852,334,080 before `make smoke`. No caches or retained files were deleted.
All four gate commands exited 0.

Factory remains **NOT RUN** pending resolution of the requested temporary-path
constraint. Its fresh `df -B1 /tmp` reported 3,379,322,880 bytes available of
a 4,294,967,296-byte mount. `findmnt` identifies `/tmp` as `tmpfs` with
`usrquota`; `ulimit -f` is `unlimited`. `quota -v`, `quota -f /tmp`, and
`quota --filesystem=/tmp -u ubuntu` could not report the effective per-user
limit (`Cannot stat() mounted device tmpfs` / `not found or has no quota
enabled`). `/var/tmp` is disk-backed on `/dev/sda1` with 33,798,340,608 bytes
available. The user instructed both “use `/tmp`” and “do not use tmpfs”; these
conflict on the current Factory mount. No Factory build/test scratch writes
were started while awaiting direction. This does not establish the prior
reported 2 GiB user limit.

These are source gates only. No candidate was sealed; Dev/PRD command-level
acceptance, live PRD replacement/deployment, QAS handoff and QAS acceptance
remain **NOT RUN**. No live service, selector, database, backup, receipt,
mailbox assignment, request or ACK changed. QAS remains serving and its eight
mailbox assignments were not touched. The unrelated untracked
`src/cmd/catalog-holdcheck/` directory remains unstaged and preserved.
## Dev probe failure and recovery checkpoint — 2026-10-23 15:42–15:55 UTC

The immutable compatibility-correction candidate remains
`cat-20261023-c01e633`, source `c01e6339e9330ff6bf7dbde042b1cd5287c55c4e`,
manifest `7a9f705484906c7889c4776da0ef64769662fd0c5360e4d3767ca8021a5c4c81`.
The previous release `cat-20261022-7a42f3b` is unchanged. Source gates remain
PASS per the source-gate sections above; this checkpoint records live Dev
acceptance and recovery only.

### Failed functional check

Dev operation `b707f52d55a6ee547541a97b08fc4ac8` reached `verifying` after
switching Linux, Sandbox, Factory and Mac to the same sealed release. Its
Factory direct-mTLS probe failed before submitting a request: helper exit 1,
`direct readiness failed: HTTP 0`. The operation was then blocked with the
failure evidence SHA-256
`a09491ecca458f9220b74c9d09487cfddf4474286a181d8d4c5606437b905193`.
The owner-only source record remains outside Git at
`/private/tmp/rsr-cat06-candidate-cat-20261023-c01e633/factory-direct-failure-evidence.json`.
The attempted capability expired at `2026-10-23T15:32:30.011432085Z`; a
second prepare was refused because that one-time scope already exists. No
capability was replayed, deleted, or edited.

Factory host diagnosis found that `runnerd` listened on `10.0.0.30:8444`,
while the existing INPUT chain rejected the connection and the Dev allowlist
contained stale source `81.95.203.65`. The observed Mac SSH source was
`31.61.247.154`. The Factory Dev TCP/8444 allowlist was narrowed to
`31.61.247.154/32`; after the correction, TCP connected and the candidate's
configured V3 direct-fence TLS and unauthenticated-admission denial check
passed. This route check is not the fixed-command direct functional probe and
does not satisfy Dev acceptance. PRD and QAS firewall routes were not changed.

### Fresh stopped-state, selector and receipt observations

Read-only `catalog-work-inventory` observations on Mac, Linux, Sandbox and
Factory at 15:47:54–15:49:53 UTC report `service_active=false`, schema 40,
zero active sessions, command slots, reservations, queued/running commands,
resumable jobs, and pending recovery finalizations. All four selectors still
point to `cat-20261023-c01e633` and each configuration hash matches its
preflight/journal:

| Host | Candidate bundle SHA-256 | Configuration SHA-256 | Selector journal | Previous selector |
| --- | --- | --- | --- | --- |
| Mac | `a338761463afe7dbe8f558f7166dd10f0d3890a0e9e151c1d906e955221067ee` | `2a256f44cf14031b8463854aa77c190ba265a419b624a0538a6c42ba130395d9` | `2cf405adee79c460bbfff9c039004bce` | `cat-20261022-7a42f3b` |
| Linux | `9a98537d38c98bcd9e9834ceb6ad23544dc85e8f038aed74f123bad1491df9cb` | `842a17572ba218ee770b39d8cf95c442c4885204d479d5b34ef6d2e4e5ff82f5` | `a18ff2ad6c3cf7de6944f48b52136d3a` | `cat-20261022-7a42f3b` |
| Sandbox | `43a92d91a43c954fd20efc6c5f8e5d5d01fca6487bf0a5da8929c3c73caed049` | `9a9adcd3236d8a92f2d405ef59f1199180310a03df6c9d246b784a702653d82a` | `721c2224bfc49e55c9d8d95f41af5d23` | `cat-20261022-7a42f3b` |
| Factory | `652b3c428c82c19b8a982e8fe6d0acb71f2734a87dfcfaf480db5530a22799d4` | `7af6d8ea68764f703b7aebd40966bbc7ff8b86523a2cb948d5f60e68659cd727` | `7a439f7cd1b3e312b7bc42939d96dabe` | `cat-20261022-7a42f3b` |

The blocked receipt is byte-identical across all four staging roots, SHA-256
`1bbd682bf2d64e92153183035b2e428ecdb8308603a6432270aa14f5146d773f`, stage
`blocked`, 11 transitions. The supported `catalog-holdcheck` reports exactly
operation `b707f52d55a6ee547541a97b08fc4ac8` and no error on Mac, Linux and
Factory. The Sandbox check could not be refreshed: verified SSH connected to
`132.226.205.205:22` but timed out during banner exchange, exit 255. A
temporary hold-check binary transfer was interrupted; inspect and clean only
that task-owned temporary path after SSH is restored. Because all four-host
hold evidence is not fresh, rollback has not begun.

Sandbox had 1,552,470,016 bytes free on `/dev/sda1` at 15:49 UTC; its Dev
database file was 761,856 bytes and `remote.db-wal` was zero bytes. Continue to
recheck exact available bytes immediately before each rollback backup or
selector write, keeping the 500,000,000-byte operating buffer, measured backup
allowance and separate 64 MiB selector reserve. The prescribed reverse order
is Mac → Factory → Sandbox → Linux, with receipt synchronization after every
host. The existing selector journals bind their prior-release backups; their
contents must still pass supported integrity checks at the rollback operation.

QAS was not changed. Fresh QAS observations show all four services active on
`cat-20261021-b84caf0`; Mac retains one active command slot and one session
reservation, which remain untouched. All eight mailbox paths remain assigned
to QAS. No filesystem mailbox request or ACK was published. QAS acceptance is
DEFERRED, NOT PASS.

**Disposition:** candidate Dev acceptance is **BLOCKED / NOT PASS**. Factory's
queued-SSH probe and the Linux direct/queued, Sandbox queued, Mac-local, and
negative admission-fence checks previously passed for this attempt; the
Factory direct functional probe did not. The failed operation remains held and
its receipt history is preserved. No rollback transition or selector rollback
has been started. Next, regain verified SSH access to Sandbox, verify its exact
hold and fresh stopped/empty state, validate the recorded backups, and use
supported rollback in the order above. Then create a fresh Dev cohort for the
same sealed candidate and rerun fresh functional acceptance. PRD deployment,
mailbox handoff and QAS alignment have not started.
