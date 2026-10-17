# Release record payload schemas, version 1

These closed JSON Schemas define the V1 release payloads for L3-02 and L3-03:
host build attestations, candidate and host-management manifests, promotion
authorizations and attestations, lane acceptance, ingress directives and
attestations, capacity measurements and policies, and PKI certificate
requests. The Go release package embeds the schemas and applies fixed-contract
checks that JSON Schema cannot express: exact host component subsets, ordered
validity intervals, cross-record identity binding, complete host-specific
ingress surfaces, deterministic capacity digest recomputation, measured policy
validation, and host-bound CSR/SAN validation.

Callers must verify each record envelope's signature, signer role, and host
binding against the active trust state before passing it to cross-record
validators. The payload validators verify reference IDs and payload bindings;
they do not make a signature trusted by themselves.

The candidate matrix has 16 lane-runtime tuples. The host-management matrix has
20 tuples and includes the complete Mac-only authority row plus the four
host-global components on every Ubuntu host. The host-management transport
contract binds CA inventory, each host's trust assignment, leaf host/URI/IP
identity, and the active peer certificate and SPKI allowlist. L3-03 uses four
promotion results per lane acceptance and exact surface inventories: Mac
`local_api`, `local_intent`, `mailbox_ack_importer`, and
`mailbox_request_importer`; Ubuntu `direct_https` and `queued_ssh_bridge`.
PKI tests use temporary Ed25519 keys and CSR material only.
