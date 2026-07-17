# Security Model

This document describes the threat model of the demo AHP host, the
controls it implements, and — just as importantly — the controls it
**deliberately omits** because it is a local-only teaching artifact. Read
the ["Do not deploy this as-is"](#do-not-deploy-this-as-is) section before
running it anywhere other than `localhost`.

---

## 1. What this is

A minimal, hand-built [Agent Host Protocol](https://microsoft.github.io/agent-host-protocol/)
(AHP) **host** plus two clients built on Microsoft's official (client-only)
Go SDK. The host owns authoritative session/chat state, sequences every
mutation through a monotonic `serverSeq`, and fans changes out to
subscribers over JSON-RPC 2.0 on a WebSocket.

## 2. Threat model

**Assets.** Session/chat state, the ordering guarantee (`serverSeq`), and
the audit trail.

**Trust boundary.** The WebSocket endpoint. Everything on the far side of
a connection is **untrusted**: any client may send any bytes, in any
order, at any rate. Multiple mutually-distrusting clients may share the
same host simultaneously (that is the whole point — multi-client sync).

**Adversaries we defend against in this demo:**

| # | Threat | Control in this codebase |
|---|--------|--------------------------|
| T1 | Client invokes an unexpected / privileged method | **Default-deny allow-list** (`host/validation.go`). Any method not explicitly listed returns JSON-RPC `MethodNotFound` and is audited. Adding a capability is a deliberate, reviewable edit. |
| T2 | Client sends a malformed / hostile channel URI (e.g. `file:///etc/passwd`) | `validChannelURI` rejects anything that is not a well-formed root/session/chat URI with `InvalidParams` **before** any state is touched. |
| T3 | Client dispatches an action to the wrong channel family | `validateActionForChannel` echoes the action back with a `rejectionReason` so the optimistic client rolls back, rather than corrupting state. |
| T4 | Client targets a nonexistent session/chat | The action is silently ignored (there is no state to mutate) and recorded as rejected in the audit log — no phantom state is created. |
| T5 | Malformed JSON frame | Replied to with a null-id `ParseError`; the connection stays open (a bad frame is not fatal to the socket). |
| T6 | Slow/hostile consumer stalls delivery and breaks ordering | Per-connection bounded write queue; if it overflows the host closes the connection so the client must reconnect and replay (`host/server.go`). This preserves ordered delivery for everyone else. |
| T7 | Tampering with the action history after the fact | **Hash-chained, append-only audit log** (`host/audit_log.go`): each line commits to its predecessor via `sha256(prevHash ‖ payload)`, so any edit/reorder/deletion breaks the chain from that point and is detectable by re-walking the file. |
| T8 | Unbounded memory from a giant frame | `SetReadLimit(32 MiB)` per connection. |

## 3. Reconciliation & ordering guarantee

The host is the single source of truth for order. Every accepted action is
stamped with the next `serverSeq` and appended to an in-memory replay
buffer **before** it is broadcast (write-ahead). On reconnect a client
sends the last `serverSeq` it saw; the host replays everything after it, or
falls back to fresh snapshots if the gap exceeds the buffer window. This is
what lets a dropped socket recover without losing or double-applying a
committed action. See `host/reconciliation.go` and the
[reconciliation guide](https://microsoft.github.io/agent-host-protocol/guide/reconciliation).

## 4. Do NOT deploy this as-is

This demo **intentionally skips authentication** for local-only
simplicity. The single demo agent declares **no** `protectedResources`, so
the host performs **no** `authenticate` handshake and **no** authorization
whatsoever. Consequences:

- **Any** client that can reach the port can create/dispose sessions, read
  any session's state, and dispatch actions into any chat. There is **no
  session isolation between users** — only structural isolation between
  channels.
- There is no transport encryption (plain `ws://`, not `wss://`).
- There is no rate limiting, quota, or per-client resource accounting
  beyond the single read-limit and write-queue bound above.

**Therefore: bind only to `127.0.0.1` and never expose this on an
untrusted network.** To make it network-safe you MUST first add the AHP
authentication flow:

1. Declare `protectedResources` on the agent's `AgentInfo` using
   [RFC 9728](https://datatracker.ietf.org/doc/html/rfc9728) (OAuth 2.0
   Protected Resource Metadata) semantics.
2. Require clients to obtain a bearer token from the declared authorization
   server and push it via the `authenticate` command
   ([RFC 6750](https://datatracker.ietf.org/doc/html/rfc6750) bearer token
   usage).
3. Reject `createSession` / `subscribe` / `dispatchAction` with
   `AuthRequired (-32007)` until a valid token is presented, and scope each
   connection to only the sessions its principal owns.

See the AHP [authentication spec](https://microsoft.github.io/agent-host-protocol/specification/authentication).

## 5. Audit log in production

The hash chain makes tampering **evident**, not **impossible**: a writer
with filesystem access can still recompute the whole chain. In production
the log MUST live on write-once storage so the append-only property is
enforced by the platform, not by convention:

- **GCS** bucket with retention/Bucket Lock, or
- **S3 Object Lock** in compliance mode.

Additionally, seed `prevHash` from the last line already on disk at
startup (this demo restarts the chain from genesis each boot) and treat any
`Append` write error as fatal — a gap in the chain defeats its purpose.

## 6. Supply-chain controls

The build and release pipeline (`.github/workflows/ci.yml`,
`Dockerfile`, `Makefile`) implements defense-in-depth for the artifact
itself:

- **Static, minimal runtime image.** `CGO_ENABLED=0` static binary on a
  distroless base with no shell and no package manager; runs as a non-root
  user; compatible with a read-only root filesystem. Pin the base **by
  digest** in production (the Dockerfile documents the `crane digest`
  fallback).
- **Vulnerability scanning.** Trivy fails CI on fixable HIGH/CRITICAL CVEs
  in the image.
- **SBOM.** Syft (`anchore/sbom-action`) emits a CycloneDX SBOM that ships
  with each release for downstream vuln management.
- **Keyless signing.** `cosign` signs the pushed image using the CI job's
  OIDC identity — no long-lived keys to leak or rotate.
- **Provenance.** `actions/attest-build-provenance` attaches a signed SLSA
  provenance attestation binding the image digest to the exact
  workflow/commit that produced it.

Consumers can then verify both the signature and the provenance against the
image digest before running it.

## 7. Reporting

This is a demonstration repository, not a supported product. For issues in
the protocol itself, see the upstream
[SECURITY.md](https://github.com/microsoft/agent-host-protocol/blob/main/SECURITY.md).
