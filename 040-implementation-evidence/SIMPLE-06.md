# SIMPLE-06 — stable PRD promotion and rollback decision package

**State: PASS for the selected stable-retention path.** Current unsuffixed PRD
is retained unchanged and the optional upgrade is **NOT RUN**. This closeout
records the exact QAS-tested candidate and its production gates; it does not
claim that a PRD upgrade was installed or that a future production operation
has been approved.

## Decision and current production identity

The active owner scope keeps the already installed production version while
Dev and QAS operate in their separate roots. The current installed identities
are:

| Host | Unsuffixed service | Installed build | Production state during this closeout |
| --- | --- | --- | --- |
| Mac `mac-control-plane` | `runner-local`, `runner-locald` | `77910358a149d6e8fcdf4694498ebdf6c1f07f58` | Both processes remain on their original PIDs. One retained session slot remains. Readiness has returned HTTP 503 intermittently; successful service-owned SQLite write probes do not identify the historical/raw cause. |
| `linux-host` | `runnerd.service` | `2802f00c93eb51a80f23b9380238c309545d6329` | Active; PID `149913` during SIMPLE-07 isolation. |
| `sandbox-host` | `runnerd.service` | `2802f00c93eb51a80f23b9380238c309545d6329` | Active; PID `717823` during SIMPLE-07 isolation; one retained session slot preserved. |
| `factory-sandbox` | `runnerd.service` | `2802f00c93eb51a80f23b9380238c309545d6329` | Active; PID `50302` during SIMPLE-07 isolation. |

Installed builds are not inferred from the Git checkout. The Mac PRD
investigation and its preserved retained work are recorded in
[SIMPLE-04](SIMPLE-04.md#s11--fresh-prd-readiness-and-database-diagnosis).
No production service, binary, configuration, database/WAL/SHM, mailbox,
firewall, credential, request, ACK, or reservation was changed for SIMPLE-06.

## Exact tested candidate package

The candidate is the per-host bundle accepted in Dev and then QAS, all from
source revision `2204e18ecd6eddc916ffa39e75ac905d3dbe2851`, Go 1.27.1. These
are exact immutable bundle identities, not rebuilt production candidates.

| Host | Bundle SHA-256 | Manifest SHA-256 | Accepted QAS service component SHA-256 |
| --- | --- | --- | --- |
| `mac-control-plane` | `0cbaff313de36ebe030144dd4192a1138aebb4b0ad54ca6fe93d6d0ff196f757` | `88e82159e2719bb510fe306b540464c94270866cb3fc120e696f0360845b617a` | `runner-local` `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`; `runner-locald` `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13` |
| `linux-host` | `3826c1406efcb9cd240e9dab55df5b09f03035d8537e85036033ad2476d38b4c` | `cbc69ad44795d688326299f00c395c1cbeb1470f83f7e9bc21f28f30dcf33df1` | `runnerd` `acbc42bfc9ce496b49097e98d6de53b02f5a3ac47a56410686341ef3807e14a7` |
| `sandbox-host` | `6ab39974eb78cf78333a048b683a365b954d95894d785d00ef96681cb8f74917` | `bc550e86fa605da09d0ad3841b7ea9192907748c927693bbad6e09b75b3905d8` | `runnerd` `5abbb5d8bcde3f11c3800a99fc1860b489e1ef3ac233c974d16a2178bda56192` |
| `factory-sandbox` | `d1b2399ed1af742afe6b4ffe01fd5d0e103d1efb3db7e12b41837a4e84cab37a` | `06c691b604742b07fe9c8d22c3eb28f8c81967701c067334f53162002b872c9a` | `runnerd` `acbc42bfc9ce496b49097e98d6de53b02f5a3ac47a56410686341ef3807e14a7` |

See [SIMPLE-05](SIMPLE-05.md) for the direct/queued QAS acceptance, mailbox
ACK, timeout, limit, managed-session, and zero-work evidence.

## Prepared production procedure and limits

The production procedure is deliberately conditional. The present instruction
is to retain the stable version, so the procedure terminates before any PRD
mutation:

1. Keep all four unsuffixed services and their current roots, keys, routes,
   configuration and SQLite authorities installed.
2. Do not use `runner-lane` with `prd`: its parser and namespace support only
   `dev|qas`. The legacy Mac/Linux installers build from the synchronized
   checkout and do not accept this exact immutable QAS bundle as a PRD
   promotion. Manual binary replacement is not supported. Therefore there is
   no production installer command to run from this package.
3. If an upgrade is requested later, first deliver a PRD-aware exact-bundle
   installer and rollback path with tests for the existing Mac V2 and worker
   V1/V2 configurations, database schema compatibility, consistent backups,
   and preservation of mailbox/request authority. The current runner-lane
   rollback must not be pointed at PRD.
4. Before that later installation, obtain a separate owner approval naming
   every host, bundle/manifest hash, exact window, affected producer pause,
   supported disposition for retained work, expected interruption, backup,
   rollback command, and postflight acceptance. Recheck all gates immediately
   before the first PRD change.
5. Preserve the original production binaries/configurations and consistent
   authority backups until every accepted request has a verified terminal
   result and postflight passes. If the selected candidate cannot be rolled
   back without replacing a newer database or losing accepted work, leave
   PRD stopped for supported repair; never restore DB/WAL/SHM files manually.

This is a ready decision package for keeping current PRD. A future upgrade is
not executable with today's selected-lane installer and remains a separately
scoped source and approval decision. The active three-lane setup does not
depend on that upgrade.

## Production impact and rollback

SIMPLE-06 caused no PRD interruption and no filesystem or service change. The
stable versions continue independently of the source checkout and accepted
Dev/QAS candidates. There is no rollback to perform because there was no
production activation. For a future upgrade, rollback is not approved or
available until a PRD-aware implementation can prove exact-bundle identity,
schema compatibility, fresh quiescence, and a retained-work-safe path; do not
substitute the Dev/QAS rollback command or a manual copy.

## Evidence and delivery

Evidence sources are [SIMPLE-01](SIMPLE-01.md) for installed identities and
paths, [SIMPLE-04](SIMPLE-04.md) for Dev and PRD diagnosis, and
[SIMPLE-05](SIMPLE-05.md) for the exact QAS candidate and acceptance. No source
tests or service actions are part of this documentation-only unit; the links,
tables, identities, and procedure were reviewed against those accepted
records.

## Git delivery receipt

The decision package and ledger status were committed on Mac `dev` as
`e83d1710d5b1ed2cb1753bf76e4aa741e17e7df4`. The commit was pushed to primary
Gitea `origin/dev`; direct `ls-remote` checks returned the same SHA from Gitea
and automatic GitHub `github/dev`. Each Ubuntu checkout was verified clean on
`dev` at predecessor `2b6bffdee9c780396fa0a3b7f95967d4080fdffc`, then
fast-forwarded from GitHub `origin/dev` with the configured explicit GitHub
identity. Final read-back confirmed all three clean checkouts at
`e83d1710d5b1ed2cb1753bf76e4aa741e17e7df4` and the evidence file present.
`git diff --check` and the SIMPLE-06 relative-link check passed. No runtime
test was required for this documentation-only decision package.
