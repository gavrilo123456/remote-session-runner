# Release record payload schemas, version 1

These closed JSON Schemas define the L3-02 `host_build_attestation`,
`candidate_manifest`, and `host_management_manifest` payloads. The Go release
package embeds the schemas and applies fixed-contract checks that JSON Schema
cannot express: equal source commit IDs, sorted exact component matrices, one
platform per host, sorted evidence references, and field-for-field binding to
the referenced host-build-attestation envelopes.

Callers must verify each host-build envelope's signature, signer role, and host
binding against the active trust state before passing it to the manifest
validators. Those validators verify the referenced record ID and payload
bindings; they do not make a signature trusted by themselves.

The candidate matrix has 16 lane-runtime tuples. The host-management matrix has
20 tuples and includes the complete Mac-only authority row plus the four
host-global components on every Ubuntu host. The host-management transport
contract binds CA inventory, each host's trust assignment, leaf host/URI/IP
identity, and the active peer certificate and SPKI allowlist.
