# Operations and health metrics

Runner processes expose `GET /health/live` and `GET /health/ready`. The Mac ingress and local executor expose `GET /metrics` on their owner-only Unix sockets. Linux exposes `GET /metrics` on its owner-only Unix socket and its public direct HTTPS listener; the HTTPS listener requires TLS 1.3 and a mapped client certificate (mTLS).

`/metrics` returns bounded JSON counters and gauges. The same metric object is included in successful doctor output and readiness reports. It contains no session or command IDs, user labels, paths, scripts, output, or credentials.

| Metric | Meaning | Warning threshold |
| --- | --- | ---: |
| `active_session_slots` | Session capacity reservations whose runtime cleanup is not confirmed | 16 |
| `active_command_slots` | Command execution slots whose stop is not confirmed | 4 |
| `queued_commands` | Authoritative commands still queued | 16 |
| `queued_intents` | Local intents recorded, dispatching, or uncertain | 32 |
| `dispatch_attempts_total` | Sum of recorded local-intent dispatch attempts | Reported; no absolute-total warning |
| `reconciliation_age_seconds` | Age of the oldest intent with uncertain delivery | 300 seconds |
| `event_lag_events` | Final remote event sequence minus the last locally mirrored sequence | 32 |
| `event_gaps_total` | Durable remote event gaps | 1 |
| `output_truncations_total` | Durable output truncation events | 1 |
| `storage_errors_total` | SQLite engine failures observed by this daemon process | 1 |
| `cleanup_failures_total` | Runtime or mailbox cleanup failures observed by this process | 1 |
| `mailbox_backlog` | Accepted mailbox exchanges; on Mac ingress, also safe regular ready markers not yet imported | 32 |

The daemons sample operational metrics at startup and every 30 seconds, and health requests also check thresholds. A warning is emitted when a threshold is first reached and an informational record is emitted when the value falls below it. Repeated samples on the same side of the threshold do not repeat the log. Each daemon reports SQLite failures and cleanup failures observed by its own process; those counters reset when that daemon restarts. Durable counts and queue ages are read from SQLite. `dispatch_attempts_total`, event gaps, and output truncations reflect retained SQLite history, so retention cleanup can lower them.
