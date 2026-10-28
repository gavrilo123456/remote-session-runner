Historical Dev/QAS assignment: the latest owner decision in `060-messages/989-message.md` retires `129.151.232.40` from Dev, QAS and PRD. Earlier instructions below to preserve that endpoint as an active PRD binding are superseded. Keep completed work as history; use `989-message.md` for the current PRD assignment.

Continue replacing the old Linux worker, then deploy BUG-027 through Dev → QAS using the promotion toolkit. Stop after QAS acceptance; PRD deployment is outside this task.

Context refresh — required:
After completing each numbered point (1–6), reopen and read `/Users/tomasz.walczuk/projects/remote-session-runner/060-messages/990-message.md` in full into your context before starting the next point. Also reread it after context compaction or resuming an interrupted session. Retain completed work and current state from the execution evidence; rereading these instructions must not reset progress or repeat completed operations.

Current checkpoint:
- Mac checkout is clean on dev at 4fdb45ec1c221bc16a6941f4c05aee42a8fca498.
- BUG-027 is implemented, but its new release has not been built or deployed.
- Mac, Sandbox and Factory passed source checks at that revision. Linux qualification was interrupted.
- Only read-only inspection of the replacement has occurred. No provisioning process remains active.

Replacement worker:
- SSH alias: remote-session-runner
- Public IP: 79.72.94.59
- Private IP: 10.0.0.228
- Account: ubuntu, UID 1001
- Identity: ~/.ssh/remote-session-runner
- Ubuntu 26.04, x86_64; noninteractive sudo works.
- Strict SSH verification now works using the Mac’s known_hosts.
- No Runner checkout, Go toolchain or services are installed.
- Missing build prerequisites: gcc, libc6-dev, make, pkg-config.
- /tmp is only about 487 MiB tmpfs. Use task-owned disk-backed staging under ~/.cache/remote-session-runner and normal shared Go caches.

1. Prepare the replacement
Install the missing prerequisites and verified native Go 1.27.1. Clone the verified GitHub mirror into /home/ubuntu/projects/remote-session-runner. Anonymous HTTPS access to that mirror was verified; do not copy private credentials unnecessarily.

Use fresh worker configuration, databases and credentials. Do not transfer old runtime databases, reservations or cohort receipts.

2. Correct the address compatibility
The current source hardcodes the old Linux public/private addresses. Updating SSH configuration alone is insufficient.

Use the explicit addresses already present in V3 profiles. Preserve fixed lane paths, service names, ports, identities and TLS/SSH validation. Do not globally replace constants or break V1/V2 and existing PRD compatibility.

Relevant files:
- src/internal/config/lane_profiles.go
- src/internal/config/v3_runtime.go
- src/internal/laneinstall/simple_configuration.go
- src/internal/laneinstall/simple_queued_ssh.go
- src/internal/laneinstall/direct_tls.go

Require matching Mac/worker URLs and pins, exact lane ports, valid addresses and no duplicate routes. Test new-address bootstrap and unchanged legacy behavior.

3. Handle first-install prerequisites explicitly
Use supported runner-lane first-install commands for separate Dev and QAS namespaces. Do not use the legacy unsuffixed systemd installer as a shortcut.

The promotion planner currently requires readable peer namespaces, including PRD. The replacement has none. Resolve this with a narrowly scoped, tested bootstrap/absence-aware controller path that proves and records complete absence; reject partial, unreadable or unexpected state. Do not fabricate PRD health, bypass guards or deploy PRD merely to satisfy this check.

Use a separate owner-only Dev/QAS host profile pointing to 79.72.94.59. Preserve the existing PRD operator binding and routing.

4. Qualify and deliver the corrected source
Make tracked changes only on the Mac. Follow primary Gitea → exact GitHub mirror verification → clean host synchronization.

The address correction requires a new candidate. Obtain fresh source receipts for its exact commit on all four hosts. Linux must complete its own full tests, vet, build and smoke; Factory substitution was declined. Preserve earlier interrupted/failed runs honestly.

5. Build once, then promote
Use build-release to seal and publish one four-host release without activation.

Use promote-to to deploy and accept that exact release:
Dev all hosts → QAS all hosts.

Dev and QAS were confirmed unused. Recheck inventories and capacity immediately before operations. Preserve peer lanes and use supported backups, receipts, selectors and recovery paths.

6. Verify BUG-027 and close out
Separately install the qualified native runner-lane diagnostic for Dev and QAS; it is not included in service bundles. Verify catalog-work-inventory --details against each selected Mac lane, including complete inventory and correct authority/artifact identity.

Report source qualification, release publication, Dev acceptance and QAS acceptance separately. Update BUG-027 evidence and the register with actual hashes and operation IDs.

Do not deploy or reroute PRD, publish project mailbox requests/ACKs, replay uncertain work, or recover/release #514 as part of this task.
