# BUG-039 — QAS deployment closeout, 2026-11-26

## Result and scope

**QAS deployment and acceptance passed on all four existing hosts.**
Operation `4b14322a41e74799908bbc5385184d4b` is `complete`, with
`branch_sync: complete`, open admissions and no rollback. The owner confirmed
**“QAS is unused; proceed.”**

QAS uses the exact sealed release already accepted in Dev. There was no
service-bundle rebuild or new source qualification: the existing four-host
full native qualification remains valid. Controller checkout
`a0cc3ddc69eff1cf8ac1f44ddbfe2e6d845c35f4` differs from frozen source `54920f5`
only in four Markdown evidence/bug files. See the
[Dev qualification and deployment](BUG-039-dev-deployment-2026-11-26.md).

Dev and PRD were not promoted, restarted, reconfigured or rerouted. Their
`stable-identity-v1` peer evidence is identical in the initial and final QAS
plans. No project mailbox or historical request/ACK was changed. BUG-038's
original exchange/archive and the separate GPU API preflight hold remain
outside this rollout.

## Immutable release and prerequisites

- Release: `rr-20261126-bug039-54920f5`.
- Frozen source: `54920f54853b8aa60febcf074bd5958fb397a670`.
- Build: `5336cde96c82c6e1ef85dc0ec6797744`, already published and inspected.
- Release digest: `ad869797bf9c4ac1ef8eb206fca9680e9c216ac66c473415199f8855668880cd`.
- Provenance digest: `d80842225b9a0432d5290a5844d54afc9dbe12a2e23c34c14de6b6aa404cccc0`.
- Required passed Dev operation: `7e073fcee4434762814089379e59fbe5`.

| Host | Selected bundle SHA-256, identical to Dev |
| --- | --- |
| Linux | `d5e9e044881fbbbd50b7eb90022c9099173dfb6d1bb04489bcda5b7bbf35be29` |
| Sandbox | `a33e5165fdbeea4829670b3cb0b7da46491c52d291184ef53f9ee4b499605cf9` |
| Factory | `4a351e151dbf3eb74077ae0b9b52471b5c54c5d20b649218c951ab3abf490113` |
| Mac | `9b8526dc460f087cc2a4c109b801a9850e7aa238ded66903817db685b2c840c7` |

The initial supported read-only plan found all four QAS services on
`rr-20261119-bug034035-7b11dbc`, schema 40, empty active-work inventories and
sufficient capacity, with no existing admission hold. Sandbox had
2,097,541,120 bytes free. The operating buffer remained 500,000,000 bytes.

`promote-to qas all` took fresh consistent backups, applied QAS holds, stopped
only that lane, switched Linux → Sandbox → Factory → Mac, synchronized receipts
and accepted the cohort before releasing admissions. No historical recovery,
administrative capacity release or guard bypass was needed.

## Acceptance, release and branch synchronization

All eight `catalog-functional-v1` gates passed: capacity, direct mTLS,
isolation, lifecycle, limits, Mac-local, queued SSH and teardown.
QAS acceptance evidence:
`0dd71927a8550756153fbaec5fd4ae48bd6dabae13860540389c6cd197d40273`.

- Cohort accepted: **21:11:32 UTC / 22:11:32 Warsaw**.
- Cohort released: **21:14:59 UTC / 22:14:59 Warsaw**.
- Branch-only closeout completed: **21:15:37 UTC / 22:15:37 Warsaw**.

The initial command truthfully returned `runtime_accepted_branch_sync_pending`:
Gitea accepted the QAS source marker, while the automatic GitHub mirror had
not yet caught up. The supported `runner-release branch-sync --lane qas`
retry verified synchronization and set the operation/branch statuses to
`complete`. It did not repeat service or selector changes. Historical pending
event 14 remains preserved; there is no additional branch-sync completion
event. The exact `qas` source marker is the frozen source above.

## Dedicated live mailbox regression

Only QAS's service-root `default` mailbox was used, explicitly selecting
`mac-workstation-qas` → `local/mac-workstation`. Ordinary running ingress
handled both publications without maintenance retirement or an intervening
restart.

Original fresh request:
`req-bug039-qas-conflict-20261126-32598f0a7a7f`.

- Command: `cmd-7cfba2978515994b0e0409f9b72d4793`.
- Session: `sess-2be2e5b874b1803c237d3b237ccbb594`.
- Job: `job-5efaed47bc0d65cf08d25772e81ec43b`.
- Original result: succeeded, exit 0, complete/untruncated expected output,
  closed teardown, response revision **2**, advertised/final cursor **4**.
- Its exact response and four contiguous events were preserved before its
  exact ACK was published marker-last. Lifecycle became
  `terminal_acknowledged`.

The controlled conflicting publication reused only this test ID, with a
different fresh idempotency key and harmless script. Its input SHA-256 is
`c8b287b83d2fcc6bad897868a3eed7667495556e46cf9c154a1537e1e37834d9`.
Normal ingress archived its exact bytes and marker, recorded
`request_id_conflict` / `accepted:false` / `executed:false`, removed the exact
newer pair from the inbox, wrote `retired` and removed its pending-index entry.

The original response/event bytes, identities, revision/cursor and acknowledged
lifecycle remained unchanged. No ACK was published for the conflict diagnostic.
The regression passed at **21:17:46 UTC**. Native-client early retained-ID
refusal remains covered by the existing source tests; a separate live native
client invocation was **NOT RUN**.

## Final postflight and preserved history

The 21:17:18 UTC supported four-host postflight found every QAS selector on the
exact release, services active, schema 40, empty active-work inventories and
sufficient capacity. Sandbox had **2,070,867,968 bytes free**. Dev/PRD peer
identities matched the initial plan exactly.

Both Mac QAS components returned HTTP 200, `live`/`ready`, and actual running
revision `54920f54853b8aa60febcf074bd5958fb397a670`; cohort discovery confirmed
`admission_held:false`. Final post-mailbox checks at **21:18:08 UTC** found zero
active session/command slots, queued commands, retained pairs/finalizations,
mailbox backlog, storage errors and cleanup failures.

All 272 pre-existing local accepted-intent identities remained preserved;
the inventory now contains 273 including this test's intent. These delivery
records do not by themselves prove terminal execution or cleanup and were not
replayed, reclassified or deleted. The diagnostic inventory's
`safe_to_resume=not_established` and `recovery_authorized=false` remain intact.
Ordinary QAS readiness is established by released admission, acceptance and the
specific postflight/regression evidence, not by recovery authorization.

## Evidence and delivery

Owner-only evidence:
`~/projects-runtime/runner-release-operations/maintenance/bug039-qas-20261126/`.
It preserves publisher confirmation, `qas-plan/`, `qas-promotion/`,
`qas-branch-sync/`, `qas-postflight-plan/`, `postflight-before-mailbox/`,
`mailbox-regression/`, `qas-mailbox-probe/` and `postflight-after-mailbox/`.

The controller checkout `a0cc3dd` was already delivered through primary Gitea,
the exact GitHub mirror and clean worker fast-forwards before this operation.
This closeout changes documentation only; its Git delivery does not rebuild or
select runtime bytes. PRD deployment and acceptance for this release remain
**NOT RUN**.
