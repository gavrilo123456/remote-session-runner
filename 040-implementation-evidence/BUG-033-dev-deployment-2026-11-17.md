# BUG-033 — Dev deployment — 2026-11-17

## Scope and owner controls

The owner authorized Dev deployment and explicitly confirmed **Dev is unused;
proceed**. The release is built once and promoted using the supported toolkit.
QAS and PRD promotion, historical work replay, mailbox publication and ACKs are
outside this task.

The initial single-IP firewall proposal was not applied. The owner first chose
to keep the firewall unchanged; the maintainer preserved that decision and
left Dev on its predecessor. The owner subsequently explicitly selected
IP-independent Runner access with mandatory certificate authentication. That
new authorization applies to Dev TCP 8444 on Linux and Factory only. Both live
and persistent rules were backed up, exact one-line candidates passed host
syntax checks, and the guarded application verified all other rules unchanged.
No PRD/QAS port, service configuration, certificate or key changed.

Actual selected-runtime and source audits established TLS 1.3, trusted client
certificates, mapped controller identities and V3 certificate/SPKI/URI pins.
After the firewall update, all three worker routes returned authenticated
HTTP 200/ready. Fresh connections without a client certificate were rejected
at TLS (`certificate required`) on all three; these checks submitted no work.
Raw host policy files and credentials remain outside Git.

## Qualification and initial failures

Initial full native qualification at `9ef7408` is **NOT PASS** on all four
hosts. The repository scanner found the transitive D-12 violation
`localapi -> laneinstall -> runtime`. Mac also encountered private-layout
fixture failures because the maintainer's temporary parent inherited GID 0.
The task-owned Mac test parent was corrected to UID 502/GID 20; the same
ownership assertions passed without weakening any guard.

Correction `fd20b1ebb5b551a1e9d143082101ae5d8843dd7b` introduces a narrow
`catalogprobe` contract, removes the API-to-installer dependency, and preserves
the concrete guard, wire header/error identities and held/unheld typed-nil
behavior. Independent review, focused tests and vet passed. The unchanged
import-boundary test then passed in fresh full native suites on all hosts.

Mac committed and pushed the correction to primary Gitea `origin/dev`, verified
the automatic GitHub mirror at the exact SHA, then all three workers verified
clean `dev` worktrees and fast-forwarded from the mirror with explicit keys
before native validation. All four used Go 1.27.1, GOMAXPROCS=2 and sequential
package compilation with the existing shared/account caches. The preserved
worker and Mac invocation scripts record those environment controls; the Mac
qualification JSON separately records its platform, CGO and cache paths. Full tests were
uncached (`go test -count=1 ./...`, after Makefile toolchain validation); vet,
build and smoke used the normal Makefile gates. Sandbox smoke performed the
same twelve help/version checks one binary at a time to limit temporary space.

| Native host | Full tests | Vet | Build | Smoke |
| --- | ---: | ---: | ---: | ---: |
| Mac | PASS 368.936s | PASS 16.871s | PASS 32.387s | PASS 42.652s |
| Linux | PASS 609.978s | PASS 34.927s | PASS 44.170s | PASS 36.663s |
| Sandbox | PASS 867.525s | PASS 27.692s | PASS 35.656s | PASS 30.085s |
| Factory | PASS 479.354s | PASS 30.380s | PASS 34.381s | PASS 27.930s |

There was no Factory substitution for Linux. Earlier failures remain preserved.
Native source checks used CGO enabled; release build settings retain the
existing Linux CGO=0 and Mac CGO=1 policy. Source receipts bind platform,
exact toolchain executable hash, final source, actual completion time and raw
qualification/log hashes. Four canonical owner-only `native-full-v1` receipts
were accepted by the build workflow.

## Immutable release

- Source/controller: `fd20b1ebb5b551a1e9d143082101ae5d8843dd7b`.
- Build: `1391d85143b789f6bceb62e9ab27f5b5`, `ready_without_activation`.
- Release: `rr-20261117-bug033-fd20b1e`.
- Release SHA-256: `38127d62b469cddcb87968d86615f3c212d1e06de42c79862bf07d24ef1425e8`.
- Provenance SHA-256: `0d116cc90cbd61fad5f027170c550b278076112a366c238b7695d52856c15e89`.
- Native build and verified four-host publication completed in 668.010 seconds.

| Host | Bundle SHA-256 |
| --- | --- |
| linux-host | `d9042be80f966fb1c55aef5c62882b9399f0d791c7eb3af2a77d8c80fdd0b4be` |
| sandbox-host | `28d65c8b623a4a609dc654656e86e8f480518a1895ca1f565f4aef3b644dc590` |
| factory-sandbox | `979e27c3db919976a78c23a6410f79e8813b2efd0e74d8472053d01a1b2b548c` |
| mac-control-plane | `a9fceaf3f1bdf3a5ad851675cce58efd97eb47423bf9f48856bb1ecde507c60d` |

The build record confirms publication on all four existing catalogs. It did
not activate a lane. Source/build staging used a measured source allocation
bound plus explicit staging/compiler allowances and a 500,000,000-byte
operating buffer. Sandbox had 1,039,798,272 free bytes against a 921,905,408-byte
pre-build budget. Monitoring during and after construction collected 38 read-only samples
(27 during the build and 11 afterward). The observed construction minimum was
818,810,880 free bytes. The first sample was about 83 seconds after build start;
the report also records a 77-second transition gap and makes no continuous-
coverage claim. Promotion takes fresh capacity
and backup evidence separately.

## Dev rollout

Operation `3b02eecc610445438a61f2387457bde5` uses the same sealed release.
A fresh read-only plan passed: all four predecessor selectors were
`rr-20261111-bug031-0b7c031`, schema 40, active services, empty work inventories
and capacity above the selected-lane 500 MB policy. This plan did not create
backups or authorize work disposition. The live command uses the owner's
unused-Dev declaration and supported common preparation, backups, synchronized
receipt, ordered selector changes and release-bound acceptance.

**COMPLETE — Dev accepted and admissions released.** Operation
`3b02eecc610445438a61f2387457bde5` completed without rollback at 2026-11-17 19:09:08 UTC
(20:09:08 Warsaw). The supported CLI took 1332.020 seconds (22m 12.020s),
including fresh baselines, preparation, ordered switching, functional acceptance
and final release. Source qualification, construction and network preparation
are separate. Dev source-marker synchronization is not applicable.

All four consistent backup manifests were verified before switching. Their
private directories remain recorded in the durable operation/milestone summary.
No database restore was performed.

| Host | Backup manifest SHA-256 |
| --- | --- |
| linux-host | `9728c7803d671e170294ecf06c6bb679598bb85604d6c62206095800984b8c6c` |
| sandbox-host | `bf3ec2cae7378ab929a5b6d12d58892a86dec8ea0a2c55fc94ef5208d3e9b26b` |
| factory-sandbox | `d5446db56bc57810cfe56ddba01a275bb044a5c914d485b28384218818d97736` |
| mac-control-plane | `abafa40446ec1c9ca48d8ec4ac0104acdd9d9fe34f4f258c5a0774323c220389` |

| Host | Switch intent → verified | Selector journal |
| --- | ---: | --- |
| linux-host | 58.912s | `b381977902dc0632f02cfa8203d0fd74` |
| sandbox-host | 55.58s | `2a25efd5f131f42a98fd542ab83c4861` |
| factory-sandbox | 56.287s | `48edcaa46fc63fad7b98c714dd3cdf9f` |
| mac-control-plane | 38.442s | `f2507d15f407196b16c2fbb8f1514ad4` |

Host durations cover guarded selection and journal verification, not source
testing or final cohort acceptance. Common preparation took 135.124 seconds.
All selected bundle hashes match the immutable table above.

Release-bound catalog-functional-v1 acceptance passed capacity, direct-mTLS,
queued-SSH, Mac-local, limits, lifecycle, teardown and isolation. Acceptance
was recorded at `2026-11-17T19:05:11.283651Z`, evidence SHA-256
`38dbc2cbf955bc4435a53c5cfa176d70e2cf7697d6ad996e8a9d1c154c7c674b`.

Public cohort status reads returned identical released receipts on all four
hosts: **16 transitions**, canonical/file SHA-256
`931eb12e2cbc5d1ae7977434577718489f985ba920516bcde96de11ea16b9d69`.

Fresh postflight confirmed four active services on the exact release, schema
40, empty work inventories, no admission holds and passing capacity. Sandbox
had **816013312 bytes free** against the 500 MB selected-lane minimum.
QAS/PRD stable identity evidence matched the fresh pre-operation baseline:
peer selectors, configuration and service identities were preserved.

After final release/restart, worker routes again returned pinned authenticated
TLS 1.3 HTTP 200/ready; all three no-client-certificate attempts were rejected
at TLS. Mac Dev ingress/executor returned HTTP 200/ready through owner-only
sockets. These post-restart checks were read-only; functional command
acceptance occurred before final admission release.

Both persistent firewall files match approved candidates and remain root:root
mode 0640. Their netfilter-persistent services are enabled and active with
successful status. No reload/reboot was used to claim boot-time retesting.
Changes are limited to Dev TCP 8444; certificates/application policy are unchanged.


## Verification boundaries and private evidence

The uncertain-dispatch/no-replay mechanism is covered by the disposable
BUG-033 regression fixtures. No live lost-acceptance fault is injected by this
rollout. The historical GPU request is not replayed, replaced or ACKed; its
administrative indeterminate result does not establish historical execution,
success or teardown. No project mailbox or historical hold is modified.
Filesystem-mailbox acceptance remains NOT RUN.

Private raw evidence roots:

- `/private/tmp/rsr-bug033-dev-20261117-lhw7h1zg/` — owner decisions, initial
  Mac failure, focused checks, formal receipts, attested controllers, build,
  network audits/backup/candidates/positive-negative TLS and rollout captures.
- `/private/tmp/rsr-bug033-mac-successor-yrc92g05/` — final Mac native gates.
- `/private/tmp/rsr-bug033-full-native-evidence-20261117-lxnmzm9i/` — original
  failed worker full suites.
- `/private/tmp/rsr-bug033-fd20-native-20261117-ct4d4fby/` — final worker gates,
  Git/source postflights and sampled construction capacity.

## Closeout delivery

Tracked closeout changes evidence/documentation only, without changing source
or sealed service bytes. Validation is Markdown review and git diff --check;
source suites are not rerun for documentation. Mac → Gitea → exact GitHub
mirror → clean three-worker fast-forwards are recorded in the private
`/private/tmp/rsr-bug033-dev-20261117-lhw7h1zg/dev-closeout-git-delivery.json` manifest.

**Dev is verified; QAS/PRD promotion remains unassigned for this release.**
