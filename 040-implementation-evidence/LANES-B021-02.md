# LANES-B021-02 — installed timeout acceptance blocker

**State:** BLOCKED — the source unit is complete, but the installed five-second deadline acceptance has not been authorized or run.

## Preflight and dependencies

- Source predecessor LANES-B021-01 is complete at Source S2 c3ebe96367437ce17dc5e6edd166912aa0d0ac2f; see [LANES-B021-01.md](LANES-B021-01.md).
- LANES-PREP-00 remains BLOCKED and LANES-L0 remains BLOCKED / NOT STARTED.
- The required producer-maintenance package A08 is incomplete. LANES-B024-02 remains BLOCKED at blocker blob 532d557496b427250093ba8642868c1607207be5, commit f816fc9e09bf8f84fe0cd7116f0677b4a381ffe7.
- A separately authorized interactive Mac administrator/operator route for a production service update has not been supplied. The owner’s source, build, test, and Git-delivery authorization does not approve this service change or a live defect probe.
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

## Exact action required before B021-02

1. Complete A08: identify the human owner and actual writer/operator for every affected Mac mailbox ingress; provide each tested pause/fence action and paired pre/post ingress/root snapshots; inventory active, queued, retained, and uncertain work; and provide the exact candidate, continuity/abort-or-repair procedure, rollback handles, named operator, and maintenance window.
2. Include explicit supported disposition for the new unACKed lost item above and the retained BUG-024 work before any affected Mac service refresh. Preserve all response/event evidence and the original held ACK.
3. Provide an authorized interactive Mac administrator/operator and approve the exact candidate update after a fresh read-only serving-binary/config inventory. Source S2 is known, but the exact currently serving Mac and remote bridge revisions/configuration must be freshly established before choosing the installation scope. Candidate binary/config digests and previous rollback digests must be recorded.
4. Explicitly approve one bounded B021-02 acceptance on the affected route recorded in BUG-021: a fresh remote-session-runner-workspace request selected for factory-sandbox-dev / remote factory-sandbox, requesting a five-second limit for the documented 40-second controlled finite hold. It must stop before the natural-success marker within the design’s finalization grace, report truthful termination and complete terminal events, close teardown, and leave zero active/queued/reserved work. Archive the exact response/event prefix before ACKing that fresh terminal request.
5. If preflight finds active/uncertain work, the route differs, a gate fails, the timeout is not enforced, output is incomplete, or teardown is lost, stop and retain evidence. Do not repeat, replay, cancel, recover, restart, install, or ACK uncertain work without the relevant supported disposition and approval.

Until A08, the administrator route, and the exact one-operation approval are present, B021-02 remains BLOCKED. BUG-021 remains NEW. BUG-022-01 and BUG-023-01 remain behind this unit in the prescribed serial sequence.
