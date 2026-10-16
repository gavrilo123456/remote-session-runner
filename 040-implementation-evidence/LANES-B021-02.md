# LANES-B021-02 — installed timeout acceptance blocker

**State:** BLOCKED — the bounded recovery source prerequisite is implemented and tested; producer pause, final candidate approval, Mac recovery/refresh, factory bridge refresh, and the installed five-second acceptance remain unapproved and unrun.

## Preflight and dependencies

- Source predecessor LANES-B021-01 is complete at Source S2 c3ebe96367437ce17dc5e6edd166912aa0d0ac2f; see [LANES-B021-01.md](LANES-B021-01.md).
- LANES-PREP-00 remains BLOCKED and LANES-L0 remains BLOCKED / NOT STARTED.
- `LANES-B024-02` remains BLOCKED. A08 preparation now has a refreshed producer/root inventory and concrete stop controls below, but the live producer pause has not been exercised or attested and the maintenance window is not approved.
- The Mac user account `tomasz.walczuk` is available and owns the per-user LaunchAgents and service root. The documented refresh does not require sudo or a separate administrator credential. The owner’s source, build, test, and Git-delivery authorization does not approve this production service change or a live defect probe; the concrete operational approval is still outstanding.
- No service installation, restart, configuration change, defect probe, or ACK was performed for B021-02.

## Fresh retained work to disposition

The B021-01 test-route attempt created one separate Mac one-off request. It ran tests only; it was not the B021-02 timeout probe:

- Inbox: remote-session-runner-workspace, root /Users/tomasz.walczuk/projects/remote-session-runner/runner/mailbox-
- Request/key: req-lanes-b021-01-mac-20261016-01 / lanes-b021-01-mac-20261016-01
- Job/session/command: job-5897c7285e7a530cbc79d374b374a7fd / sess-d76592166342428ba1b7b703fc87bb35 / cmd-6817a947ceec8c3e54fbcdc2b8f2fb30
- Terminal result: response revision 5, event cursor 25, command failed with exit 2, job phase lost, teardown_outcome lost, output complete and not truncated.
- Response SHA-256: af381ad1fafa6bc41c15405de024a9d4830218d01c040592d9c9895eeaa3b0
- Event-prefix SHA-256: 1c63b0644bc5ad95d7c29f46d18663f4e1850babeb187a9f10b8a995b8e02d04

The exact response and event prefix remain in the mailbox. No ACK was written, and no recovery, capacity release, restart, replay, or cleanup was performed. The response and event hashes are also recorded in [BUG-024 evidence](BUG-024.md#additional-unacked-test-run). This fresh pair is separate from the original BUG-024 held response/ACK and from the four fresh BUG-024 route requests whose ACKs are already recorded as consumed. Do not alter those historical states.

### Recovery applicability correction

The original proposed command was not valid for this retained result: the
response says command `failed`, exit 2, with a matching `command_failed`
terminal event, while the then-current `--teardown-pair` store predicate
accepted only `succeeded`/exit 0 and `command_succeeded`. **Do not run the
installer command as originally proposed against the old candidate.** No
installer, recovery, replay, manual slot release, or ACK has been attempted.

The source correction adds only the ordinary failed-command case: nonzero
exit, matching command/job failure state and exit code, complete available
non-truncated output, an intact contiguous event prefix ending in
`command_failed`, a stopped/released command slot, the exact lost one-off
session/job identity, the complete idle authority, and exact runtime ownership
cleanup proof. Timeout/cancel/lost/unknown or contradictory evidence remains
ineligible. Capacity release still applies only to the retained session
reservation after runtime cleanup proof; command result, event prefix, output,
and request history remain unchanged. The corrected source is delivered at
`a2c2352750e7ba65cfdfb6d6e75063e29ea5940d`; installation still requires the
final candidate frozen below and the exact operational approval.

### Corrected recovery source gates

The source fix was committed on Mac `dev` as
`a2c2352750e7ba65cfdfb6d6e75063e29ea5940d`, pushed to primary Gitea, verified
at the same SHA on the automatic GitHub mirror, and fast-forwarded to the clean
`dev` checkouts on all three Ubuntu hosts before validation. The Ubuntu Go
toolchain was Go 1.27.1. The added regressions cover an ordinary failed
nonzero command with its matching `command_failed` event, unsafe or
contradictory evidence, preservation of the original failure/events without
replay or manual release, and retry after finalization failure.

| Machine | Validation | Result |
| --- | --- | --- |
| Mac (`mac-control-plane`) | Focused store/execution/runnerlocald tests; `make test`, `make vet`, `make build`, `make smoke`; focused race tests for the same packages | PASS. The first restricted-shell full-suite attempt was denied its existing `/bin/ps` test operation; the authorized Mac-shell rerun passed. Race command: `go test -race ./src/internal/store ./src/internal/execution ./src/internal/runnerlocald -run 'CompletedTeardown|BUG024FailedCommand' -count=1`. |
| `linux-host` | `make test`, `make vet`, `make build`, `make smoke`; focused race command `GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/store ./src/internal/execution ./src/internal/runnerlocald -run 'CompletedTeardown|BUG024FailedCommand' -count=1` | PASS at the exact source SHA. Race package results: store 330.535s, execution 113.797s, runnerlocald 70.609s. |
| `sandbox-host` | Same full make gates and focused race command as `linux-host` | PASS at the exact source SHA. Race package results: store 267.948s, execution 66.446s, runnerlocald 35.656s. |
| `factory-sandbox` | `make test`, `make vet`, `make build`, `make smoke`, with `GOMAXPROCS=1 GOGC=20 GOMEMLIMIT=256MiB GOFLAGS=-p=1` | Those gates passed at the exact source SHA. Race was unavailable: this checkout reports `CGO_ENABLED=0`, and no `cc` executable is installed. This is recorded as unavailable, not as a race PASS. |

The host gates used the configured explicit SSH identities and ran only in the
clean synchronized checkout. They did not edit source on Ubuntu, touch the
installed services, publish mailbox work, or alter any retained response or
ACK. Candidate builds and installed acceptance are separate, later gates.

## Fresh read-only serving inventory and preparation candidates

Collected on 2026-10-16 after source S2 delivery. These observations used the
owner Mac shell and the configured explicit SSH identity for `factory-sandbox`.
They did not send Runner requests, ACKs, or defect probes and did not change
installed files, service state, configuration, storage, or credentials.
The Runner route was not used because the separate test request has unresolved
lost teardown and is being preserved unACKed. The owner-authorized Mac shell
and SSH fallback were used instead.

Commands included:

~~~sh
# Mac — owner shell; installed identity and per-user readiness guard
id -un
id -u
stat -f '%Su:%Sg %Sp %N' "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin" "/Users/tomasz.walczuk/Library/LaunchAgents/com.remote-session-runner.local.plist" "/Users/tomasz.walczuk/Library/LaunchAgents/com.remote-session-runner.locald.plist"
stat -f '%Su:%Sg %Sp %N' "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml" "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
shasum -a 256 "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner" "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-local" "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-locald" "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml" "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner" --version
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-local" --version
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-locald" --version
for label in com.remote-session-runner.local com.remote-session-runner.locald; do
  launchctl print "gui/$(id -u)/$label" 2>/dev/null | rg '(^[[:space:]]*(state|program|pid|last exit code|path|label) =|arguments =)' || true
done
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-locald" preflight-restart --config "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml"
sqlite3 -readonly -separator '|' "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db" "SELECT r.session_id,r.reserved_at,r.cleanup_confirmed_at,r.released_at,s.state,s.target_profile,s.environment FROM exec_capacity_reservations r LEFT JOIN exec_sessions s USING(session_id) WHERE r.released_at IS NULL; SELECT job_id,session_id,command_id,phase,command_state,teardown_state,teardown_reason,ingress FROM exec_jobs WHERE job_id='job-5897c7285e7a530cbc79d374b374a7fd'; SELECT session_id,state,target_profile,environment FROM exec_sessions WHERE session_id='sess-d76592166342428ba1b7b703fc87bb35';"
sqlite3 -readonly -separator '|' "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db" "SELECT mailbox_id,client_request_id,operation,request_state,resource_id,response_revision,available_event_sequence,acknowledged_at,execution_selection_state,resolved_environment,resolved_target_profile FROM mailbox_exchanges WHERE client_request_id='req-lanes-b021-01-mac-20261016-01';"

# Mac — build exact candidates from clean dev at E1, shared caches
cd /Users/tomasz.walczuk/projects/remote-session-runner
source_sha=744fafd5e4a4cc0728107ed647f241f0b074a448
candidate_dir=/private/tmp/lanes-b021-02-candidates/mac-744fafd
go_bin="/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go"
mkdir -p "$candidate_dir"
for name in runner runner-local runner-locald; do
  GOTOOLCHAIN=local GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache "$go_bin" build -ldflags "-X remote-session-runner/src/internal/buildinfo.SourceRevision=$source_sha" -o "$candidate_dir/$name" "./src/cmd/$name"
done
shasum -a 256 "$candidate_dir/runner" "$candidate_dir/runner-local" "$candidate_dir/runner-locald"
rollback_dir=/private/tmp/lanes-b021-02-candidates/rollback/mac-77910358
mkdir -m 700 -p "$rollback_dir"
for name in runner runner-local runner-locald; do
  cp -p "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/$name" "$rollback_dir/$name"
done
shasum -a 256 "$rollback_dir/runner" "$rollback_dir/runner-local" "$rollback_dir/runner-locald"

# factory-sandbox — SSH identity /Users/tomasz.walczuk/.ssh/factory-sandbox,
# IdentitiesOnly=yes, BatchMode=yes, StrictHostKeyChecking=yes
cd /home/ubuntu/projects/remote-session-runner
systemctl show -p ActiveState -p SubState -p MainPID -p User -p FragmentPath -p ExecStart runnerd.service
/home/ubuntu/.local/share/remote-session-runner/bin/runnerd --version
sha256sum /home/ubuntu/.local/share/remote-session-runner/bin/runnerd /home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge /home/ubuntu/.local/share/remote-session-runner/bin/runnerd-entrypoint.sh
sha256sum /home/ubuntu/.local/share/remote-session-runner/config/linux.yaml /home/ubuntu/.local/share/remote-session-runner/config/ssh-controller-map.yaml /home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
deploy/ssh/install-queued-bridge.sh preflight
deploy/ssh/install-queued-bridge.sh status
GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go build -o /tmp/lanes-b021-02-candidates/runner-ssh-bridge ./src/cmd/runner-ssh-bridge
rollback_dir=/tmp/lanes-b021-02-candidates/rollback/bridge-2802f00
mkdir -m 700 -p "$rollback_dir"
cp -p /home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge "$rollback_dir/runner-ssh-bridge"
cp -p /home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge-forced.sh "$rollback_dir/runner-ssh-bridge-forced.sh"
cp -p /home/ubuntu/.local/share/remote-session-runner/config/queued-ssh-bridge.manifest "$rollback_dir/queued-ssh-bridge.manifest"
sha256sum "$rollback_dir/runner-ssh-bridge" "$rollback_dir/runner-ssh-bridge-forced.sh" "$rollback_dir/queued-ssh-bridge.manifest"
~~~

The Mac candidate command ran once for each of `runner`, `runner-local`, and
`runner-locald`; the factory command ran from the clean synchronized checkout.
`status` exited 1 because the installed bridge manifest is older than the
checked-out source. The initial Mac artifact path probe omitted the `bin/` and
`config/` directories and returned “No such file”; LaunchAgent `ProgramArguments`
and a directory inventory established the correct paths. The first process
inspection attempt was blocked by sandbox `Operation not permitted` for `ps`;
filtered per-user `launchctl print` provided the service state and PIDs. The
first remote config-path inspection used unavailable `rg`; it was rerun with
system `grep` and completed successfully. The first read-only SQLite path
guess omitted `state/` and failed to open; `find` located the correct database
and the targeted `-readonly` queries completed. No source file was copied
outside Git.

### Mac control plane

The installed `runner`, `runner-local`, and `runner-locald` all report build
revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58`. Their installed SHA-256
values are respectively:

| Artifact | Installed path | SHA-256 |
| --- | --- | --- |
| `runner` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner` | `9fc0af3f8439451f0e8ed570be9462b93a99746db01ee5c3ba6ae10ffbcd9b3c` |
| `runner-local` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-local` | `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455` |
| `runner-locald` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-locald` | `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13` |

The earlier BUG-024 record's `b71aaef0859bd0266c343e638e62e268999a388b`
build is a historical observation. The current attested revision is later and
contains the BUG-024 recovery changes; the source diff from this installed
revision to candidate `744fafd5e4a4cc0728107ed647f241f0b074a448` is the B021
timeout normalization/forwarding and test-fixture change recorded in the
source unit.

Both LaunchAgents are loaded and running: `com.remote-session-runner.local`
(PID 48708) and `com.remote-session-runner.locald` (PID 48627). The active
`config/mac.yaml` and staged `config/mac.next.yaml` are both mode 0600 and have
the same SHA-256, `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711`.
No configuration delta is proposed.

The direct, read-only guard
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/runner-locald preflight-restart --config '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml'`
returned exit 1:

~~~text
runner-locald preflight-restart: local execution is not quiescent (active_session_slots=1 active_command_slots=0 queued_commands=0 resumable_jobs=0)
~~~

The read-only database queries correlate that slot to the exact new unACKed
test request above:

| Record | Read-only result |
| --- | --- |
| Capacity reservation | Session `sess-d76592166342428ba1b7b703fc87bb35`, reserved `2026-10-16T12:34:28.388843Z`; `cleanup_confirmed_at` and `released_at` are NULL |
| Session | `lost`, target `mac-workstation`, environment `mac-dev` |
| Job/command | `job-5897c7285e7a530cbc79d374b374a7fd` / `cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`; job phase `lost`, command state `failed`, teardown `lost`, reason `runtime_cleanup_unconfirmed`, ingress `local_executor` |
| Mailbox exchange | `req-lanes-b021-01-mac-20261016-01`, state `complete`, response revision 5, event cursor 25, `acknowledged_at` NULL, resolved `mac-dev` / `mac-workstation` |

This confirms the active slot is the separately retained, unACKed failed test
request; it is not an unrelated active job. No recovery, capacity release,
ACK, replay, restart, or cleanup has been performed. The Mac installer stops
ingress and uses candidate `runner-locald` for its exact-pair recovery path.
It must not run until A08 pause/fence controls and explicit owner approval are
in place. A normal refresh without that recovery would fail the observed
quiescence guard.

The exact candidate was built from clean Mac `dev` source commit
`744fafd5e4a4cc0728107ed647f241f0b074a448`, using Go 1.27.1, the Makefile's
shared caches, and the installer's `buildinfo.SourceRevision` linker flag.
The code tree is unchanged from tested S2
`c3ebe96367437ce17dc5e6edd166912aa0d0ac2f`. Candidate binaries remain outside
Git at `/private/tmp/lanes-b021-02-candidates/mac-744fafd/`:

| Candidate | SHA-256 |
| --- | --- |
| `runner` | `7b07fc0b94484e52c3651b2aa006315b5127a0ff3db08804ec5ed039dc8954b0` |
| `runner-local` | `1afefda79e7f0ae81a8d4acffa75182bcfd2349db7bae7c6b94c53479c0a37ca` |
| `runner-locald` | `6de0e12274722913994802b637a1070559b0da77b0675bfbcc713536ca06c558` |

Each candidate's `--version` reports commit
`744fafd5e4a4cc0728107ed647f241f0b074a448`. The previous installed binaries
are preserved, with matching verified hashes, at
`/private/tmp/lanes-b021-02-candidates/rollback/mac-77910358/`. The exact Mac
installer refresh replaces all three binaries and both LaunchAgents; the
active config hash above is to remain unchanged.

These are preparation artifacts, not approved installation inputs. The Mac
installer rebuilds and embeds the current clean `dev` HEAD. This evidence
update will advance that HEAD, so before any approved refresh the candidates
must be rebuilt from the then-current synchronized commit, their final hashes
verified, and those digests presented for the operational approval. Do not
install the E1 binaries above after the checkout advances.

### Factory-sandbox queued bridge

The target is `oracle-waclaw-dziwulski-ubuntu-micro-01`, account `ubuntu`.
`runnerd.service` is active/running as `ubuntu`, with MainPID 50302 and
ExecStart `/home/ubuntu/.local/share/remote-session-runner/bin/runnerd-entrypoint.sh`.
The entrypoint selects `config/linux.yaml`. Its installed `runnerd` reports
revision `2802f00c93eb51a80f23b9380238c309545d6329`, binary SHA-256
`fafb7d9ba5d343d425fa1752c21a4859a3f42c344d5eca5a43ee5f3dfc0ee374`. Its
entrypoint script SHA-256 is
`83e5e56d300eeea4a56a336c13706ed7de7697f324e22278e9e3fbdf6be9f2f3`. The
current `linux.yaml`, `ssh-controller-map.yaml`, and `client-principals.yaml`
hashes are `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699`,
`5d0583470ef09a4df1a047913f068783f4422de8bc0baa64368c1a536d5da20f`, and
`41ff3ecbc6dd5bc0ea2eb3afc1d0f20f4fd0fd6d6aef801afe2bb01582e999ad`.

The queued bridge manifest currently binds the bridge to source commit
`2802f00c93eb51a80f23b9380238c309545d6329`; installed bridge and wrapper
hashes are `3a13bff38320ab0098cecea38e349e4868c9ab9702a75f2f3ff88cbaad7ce42c`
and `85947f4d76b08c415ab7c98b4e698f9317bdb0dc24c02b041fece6a0ad5d0809`.
The installer's read-only `preflight` passed for source
`744fafd5e4a4cc0728107ed647f241f0b074a448` and dispatcher fingerprint
`SHA256:4gu9/DmtT9WpRxbR/hvhz5BDgCuVAqiFfiDNcnHLBVs`. Its read-only `status`
reported that the installed bridge had not been refreshed to the checked-out
source revision. No refresh was attempted.

The exact candidate `runner-ssh-bridge` was built from the clean factory
`dev` checkout at `744fafd5e4a4cc0728107ed647f241f0b074a448`, native
`linux/amd64`, using Go 1.27.1 and the installer's build command. Its SHA-256
is `f274fdacd55bb6f68205b978fea24a0b9d2071afb8aa13db07f6d495b1ebe016` at
`/tmp/lanes-b021-02-candidates/runner-ssh-bridge`. It reports
`build_revision=unattested`, as expected for this bridge build. On an approved
refresh, the installer will generate a new manifest binding this binary SHA
to source commit `744fafd5e4a4cc0728107ed647f241f0b074a448`; no new manifest
was written or validated during preparation.
Previous bridge, wrapper, and manifest bytes are preserved and hash-verified
at `/tmp/lanes-b021-02-candidates/rollback/bridge-2802f00/`; the saved manifest
SHA-256 is `a298ea99c72f7b021079389a1770c762fd1503722da9417d51cd38f2be1ef4b6`.

The documented `deploy/ssh/install-queued-bridge.sh refresh` changes only the
bridge binary, fixed wrapper, and source manifest. It leaves the dispatcher
key, controller map, authorization line, and `runnerd.service` untouched; it
does not restart `runnerd`. A08 still must establish and test the producer
pause/fence and account for work that could be using this bridge before that
refresh is approved. The E1 bridge artifact above is also preparatory; the
installer must rebuild from the then-current clean `dev` HEAD and generate a
manifest for that exact revision before the final digest/approval checkpoint.

## Consolidated maintenance package and owner checkpoint

The producer/root and control inventory is in [LANES-PREP-00.md](LANES-PREP-00.md).
It records an active SlideStud logger-gate stream (`b134` through `b142`,
one-minute publications through 16:02 CEST), five preserved
`agents-work-dispatcher` inbox entries, and the preserved unACKed B021-01
result. Direct workspace publishers have no tested non-destructive global
fence; their stop is an owner-controlled pause. The Mac installer does provide
a tested Runner-ingress/service boundary, but does not stop those direct
writers. The production workflow must remain running until Tomasz approves a
window and suspends the affected publishers.

### Approval scope to present after final source/evidence delivery

The next implementation work is to freeze a clean synchronized `dev` SHA,
build the three Mac binaries and factory queued bridge from that exact SHA,
verify hashes, and place the candidate manifest, rollback handles, and this
approval package outside Git. No final candidate hash is recorded in this
tracked evidence; the earlier E1 digests are historical preparation artifacts
and are not installation inputs.

The proposed window is 30 minutes. Tomasz supplies the exact start time when
approving, after the SlideStud publisher is paused and the fresh inventory is
captured. The single approval should cover these ordered actions:

1. **Guarded recovery plus Mac candidate refresh.** Use the corrected source
   candidate and the exact command below, only after fresh identities,
   complete idle/ownership proof, and the owner producer freeze:

   ```sh
   # Mac — account tomasz.walczuk
   cd /Users/tomasz.walczuk/projects/remote-session-runner
   deploy/macos/install-launchagents.sh --recover-stalled \
     --teardown-pair 'sess-d76592166342428ba1b7b703fc87bb35:cmd-6817a947ceec8c3e54fbcdc2b8f2fb30'
   ```

   The candidate must preserve the original failed exit-2 response, complete
   output and event prefix, request identity, and all held/previous ACK state.
   It must prove the exact runtime cleanup before releasing only this session
   reservation. It must never replay the script or edit/release a slot manually.
2. **Fresh zero-work verification.** Read the active Mac authority after the
   installer succeeds. Require zero active sessions/commands, queued commands,
   resumable jobs, live reservations, and pending recovery finalizations;
   verify installed Mac revision/hashes and both LaunchAgents. Re-read the
   exact B021-01 response/event hashes and confirm no ACK appeared. Preserve
   the original BUG-024 held ACK and all four already consumed fresh BUG-024
   ACK records.
3. **Factory queued-bridge refresh.** On factory-sandbox, after a fresh
   zero-work check and exact current bridge/config/key inventory, run
   `deploy/ssh/install-queued-bridge.sh refresh` from the synchronized clean
   checkout. It atomically replaces only the bridge binary, forced wrapper,
   and manifest; it does not restart `runnerd` or edit controller/key policy.
   Verify the installed manifest and hashes. The script rolls back a failed
   refresh internally. The approval package carries a separately staged,
   hash-guarded restore of the previously verified bridge/wrapper/manifest for
   a later acceptance failure; do not overwrite an unexpected current hash.
4. **One timeout acceptance probe.** Publish one fresh
   `remote-session-runner-workspace` request selected for
   `factory-sandbox-dev` / remote `factory-sandbox`, with
   `timeout_seconds: 5` and the documented controlled 40-second finite hold.
   The command must terminate before its natural-success marker within the
   bounded finalization grace, report truthful terminal command/event state,
   close teardown, and leave zero active/queued/reserved work. Preserve the
   exact terminal response and advertised event prefix outside the mailbox
   before ACK. The approval package preselects a unique request ID; after the
   terminal revision/cursor are observed, the only authorized ACK is the exact
   three-field object copied from that response:

   ```json
   {
     "request_id": "<approved request_id>",
     "response_revision": <terminal response_revision>,
     "available_event_sequence": <terminal available_event_sequence>
   }
   ```

   If the request is nonterminal, loses teardown, reports inconsistent events,
   or leaves capacity, stop. Do not ACK or repeat it until its exact disposition
   is approved.

### Continuity and rollback

- Before maintenance: preserve all existing inbox/outbox/event/ACK/diagnostic
  files; freeze only the explicitly named producers; record paired root
  snapshots and all host zero-work/inventory results. The existing
  `agents-work-dispatcher` residue is retained exactly as observed.
- During the Mac refresh, the Mac-local API and executor are briefly
  unavailable. The installer stops ingress first, waits for both old
  processes/sockets to be absent, then performs exact recovery and candidate
  activation. It restores the prior LaunchAgents only before the candidate
  recovery boundary when its guards prove that safe. After the candidate
  boundary it deliberately leaves both LaunchAgents stopped and the candidate
  installed; do not roll back the authority or start the old binary. Repair or
  retry only with the same exact pair and a fresh inventory.
- The factory `runnerd.service` remains active during the queued-bridge refresh.
  The refresh installer has a lock and transactional rollback for a failed
  three-file handoff. If later acceptance fails, use only the separately
  staged hash-guarded restore, and only if all three files still equal the
  approved candidate hashes and the host has no active bridge execution.
- Resume paused producers only after candidate revisions, fresh zero-work
  inventories, preserved evidence/ACK state, and the timeout acceptance or its
  explicitly approved failure disposition have been recorded.

No live producer pause, exact-pair recovery, candidate refresh, factory bridge
refresh, timeout probe, or new ACK has occurred for this unit. Until the owner
pauses the active publishers, chooses the exact window, and approves the frozen
outside-Git package, B021-02 remains BLOCKED. BUG-021 remains NEW. BUG-022-01
and BUG-023-01 remain behind this unit in the prescribed serial sequence.
