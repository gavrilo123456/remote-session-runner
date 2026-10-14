# P167 — Factory direct-mailbox replacement and legacy retirement

**Status:** PASS — the controlled replacement, logical retirement, and
separate physical removal of the legacy Factory mailbox are complete. All
three Ubuntu services and the Mac services are running the delivered
implementation revision after their guarded installed-service transitions.

## Scope

P157 originally accepted the Factory mailbox
`agentic-software-factory-runner` at
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp/mailbox-`.
That historical acceptance remains recorded in
[P157 factory-sandbox](P157-factory-sandbox.md); this record does not rewrite
it as proof for a new path.

The replacement is the separately registered
`agentic-software-factory-runner-direct` mailbox at
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-`.
It retains the `agentic-software-factory` alias, defaults to
`ubuntu-factory-sandbox`, and permits `mac-local` as its only override. This
record covers only retirement and removal of the old ID/root; it does not alter
the accepted Factory host, queued bridge, or direct mTLS endpoint.

## Delivered source and host state

The implementation revision used for the controlled transition was
`2802f00c93eb51a80f23b9380238c309545d6329` on `dev`. The Mac primary Gitea
`origin/dev` and automatic GitHub `github/dev` mirror both resolved to that
exact SHA before host work began. All three Ubuntu source checkouts were then
fast-forwarded cleanly through their GitHub-mirror `origin` to the same
revision.

The Factory checkout's `origin` was corrected to its explicit SSH GitHub
remote before that fast-forward, and `make` was installed there so its normal
installer gate could run. The installed-service result was deliberately
per-host:

| Host | Source checkout | Installed-service result |
| --- | --- | --- |
| Mac | `remote-session-runner` | Controlled installer completed successfully. |
| Linux | `remote-session-runner` | Service installer completed successfully. |
| Factory | `factory-sandbox` | Service installer completed successfully. |
| Sandbox | `sandbox.env` | The guarded exact-pair recovery released two retained lost slots; the installer then completed its zero-work gate and restarted the service. |

The Sandbox preflight initially reported `0` active sessions, `0` running
commands, `2` unreleased slots, and `0` unfinished jobs. The runtime markers
named the two retained sessions but did not record command IDs, so the
commandless-session recovery correctly refused without releasing capacity and
left the service stopped. Runner's own authorization audit log then supplied
the exact submitted pairs:

| Session | Command |
| --- | --- |
| `sess-1ccdb0971ec4d69673c18f7539752eb2` | `cmd-e6077e1f4dca875ca926950ba41e33c4` |
| `sess-87d6d449e73736ee5cb295f5d7ed7745` | `cmd-28f586e122dd5bd393a5b968fb49d62f` |

The supported guarded pair recovery accepted precisely those two identities,
released both pairs, passed the installed P128 zero-work gate, and started
`runnerd.service`. Its read-only postflight reported active/ready build
`2802f00c93eb51a80f23b9380238c309545d6329` with zero active session/command
slots, queued work, and mailbox backlog. The failed commandless attempt did
not replay work or release a slot.

## Mac mailbox cutover and fresh acceptance

The controlled Mac installer activated the candidate that retains only the
direct Factory mailbox. The active configuration no longer contains
`agentic-software-factory-runner`; it contains
`agentic-software-factory-runner-direct` at the direct root. The direct
mailbox parent was confirmed owner-only at mode `0700`.

Logical retirement of the exact legacy ID/root completed through the reviewed
fence and registry tombstone. The retirement record was validated before any
physical filesystem deletion.

After the cutover, a newly named native direct-mailbox request completed the
terminal-response, event, and ACK path:

| Request | Result |
| --- | --- |
| `req-p157-factory-sandbox-18de88a4e0463660` | Terminal response and event were observed; the matching ACK was consumed. |

This is fresh evidence for the direct configured root. It does not reuse the
old mailbox path or replay the P157 historical request.

## Separate legacy-tree removal

Immediately before deletion, a fresh check of the exact retired legacy root
found its ingress, ACK, and diagnostic directories empty and found zero open
descriptors for the tree. The fence and tombstone had already been validated.

Only the exact legacy root was then removed:

```text
/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp/mailbox-
```

Its parent directory remains intact. The Factory checkout's local ignore entry
for the removed legacy tree was deleted, while the ignore entry for the active
direct mailbox was retained. No parent directory, retained historical P157
record, or Runner database was removed or rewritten.

## Boundary

P167 proves the replacement root's fresh native response/event/ACK exchange,
the legacy registry retirement, and removal of that exact legacy filesystem
root. It also records the separate Sandbox retained-capacity recovery and
installed-service restart. It does not accept a different host or extend the
existing software-process-crash evidence to physical power-loss recovery.
