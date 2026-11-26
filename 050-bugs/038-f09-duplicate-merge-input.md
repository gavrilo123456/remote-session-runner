# BUG-038 — Retire the conflicting Turn 10 merge input

**Status: RESOLVED — exact duplicate retired on 2026-11-26.** The newer JSON
and ready marker are archived outside the inbox. The original acknowledged
success and all seven events are unchanged, and Mac PRD is ready on the same
release. See [the live evidence](../040-implementation-evidence/BUG-038.md).

## Incident

On 2026-11-26 a second, different merge request reused an acknowledged successful request ID in the `gpu-services` Mac mailbox. Runner kept its original result and left the newer JSON and ready marker in the inbox. The lifecycle query reported `publishable_pair`, `terminal_acknowledged` and action `none`. The merge slot stayed reserved. The separate API-host preflight has its own recovery record.

The affected Mac PRD bundle is `rr-20261119-bug034035-7b11dbc`. The mailbox owner and account are `gpu-services` and `tomasz.walczuk`. The input ID is `req-gpu-t10-f09-merge-dev-20261126-terra01`. The SHA-256 of the newer input is `c215fa6028f0c583eca5a78ba6284cfba9a8ee45df59d4eb539cbd81993769ee`. The successful original response is revision 2 and advertises seven events for `cmd-363f44aa877e76e25b4a959b3c32f981`.

## Incident operator

[`mailbox-retire-f09-duplicate`](../src/cmd/mailbox-retire-f09-duplicate/main.go) accepts only this incident. It opens the selected authority read-only, compares the retained script with the conflicting script, and verifies the original terminal response, command, owner and event stream. It checks the exact published artifacts against their SHA-256 hashes. Unexpected or expired evidence causes a refusal.

On the Mac, build and test the standalone operator from a reviewed Runner checkout using the repository's pinned Go toolchain and shared caches. Keep the executable in a private maintenance directory. Use the no-argument invocation for inspection. It returns `ready` for the original conflict, `partial` for a recognizable interruption and `retired` for an already completed withdrawal.

```sh
# Mac control plane, owner account, from the private directory containing the reviewed binary
./mailbox-retire-f09-duplicate
```

For a live retirement, coordinate the Mac PRD service/outbox interruption with the other users of that lane. Quiesce writers to this mailbox, including agents publishing files directly. Use the supported Mac PRD stop procedure for both services. The apply mode takes the catalog, service lifecycle and publisher locks, checks the stopped services and sockets, and revalidates the original authority. The command does not stop or start services.

```sh
# Mac control plane, owner account, only inside the coordinated maintenance window
./mailbox-retire-f09-duplicate --apply --publishers-stopped
./mailbox-retire-f09-duplicate
```

The operator preserves the exact two files inside `~/projects-runtime/runner-release-operations/maintenance/gpu-services-f09-duplicate-merge-20261126/retired-input/`. Its parent must be owner-only (`0700`) and on the inbox filesystem. It syncs the archived files before withdrawing the ready marker, then the JSON. It rechecks the original outbox and events and returns `retired` only after the verification succeeds. If interrupted, retain both locations and repeat the guarded procedure. An unexplained missing file, changed contents or a different inode at a republished name requires investigation.

After successful verification, restore the same services, verify readiness and return the retirement evidence to the Git slot holder. Terra rechecks both Git tips before using a fresh request ID and idempotency key.

## Disposable verification

On a development host with Go 1.27.1, run the following commands from its clean Runner source checkout.

```sh
go test ./src/internal/mailboxretire ./src/cmd/mailbox-retire-f09-duplicate -count=1
go vet ./src/internal/mailboxretire ./src/cmd/mailbox-retire-f09-duplicate ./src/internal/laneinstall
go build -o /tmp/mailbox-retire-f09-duplicate ./src/cmd/mailbox-retire-f09-duplicate
```

A private Mac fixture also exercised the exported retirement function with byte-for-byte copies of the four incident artifacts. Inspection returned `ready`, apply returned `retired`, and repeat returned `retired`. The fixture preserved the original outbox hash `5bceb42a6650dbff812fe35605877c2105e0e967cf9eaeb13bb8df34afc25709` and event-stream hash `7179a02c40ce22ce8fc10ffd5632cb598c05a4dec51600062a60d3dc6f097909` at each step. Live retirement and the Git slot decision are separate operational outcomes.

## Live retirement — 2026-11-26

The owner confirmed PRD users paused and authorized the shared maintenance.
An earlier Mac command finished successfully before the fresh complete
inventory confirmed no remaining live work. The maintainer used the supported
Mac PRD stop, the tested `003c912` operator, and the supported start of the
unchanged `rr-20261119-bug034035-7b11dbc` release.

Apply and both subsequent inspections returned `retired`. The exact newer
JSON and marker retain their original inodes in the private `retired-input`
archive; neither name remains in the inbox. The original response and event
hashes are unchanged. The live lifecycle endpoint reports request input
`absent`, durable state `terminal_acknowledged`, and `acknowledged: true`.
Both services passed readiness; no capacity or recovery finalizations remain.
Configuration, selectors, schema and peer lanes were preserved.

The owner-requested notices were queued once each to Astra and Terra. The CLI
could not automatically resume their threads, so queueing is not proof of
receipt or project resumption. Terra retains the Git-tip recheck, fresh-ID
submission and project booking decisions. The separate API preflight remains
on hold. This closes this exact incident; it does not add automatic retirement
of future conflicting request IDs to the installed service.
