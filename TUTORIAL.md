# AHP Multi-Client Sync — A Hands-On Tutorial

This tutorial walks through what the **Agent Host Protocol (AHP)** is, the
concepts this demo exercises, and a complete, reproducible run of a
hand-built AHP *host* synchronizing state between two independent clients
built on Microsoft's official Go SDK.

Everything below the [walkthrough](#5-a-real-run-captured-output) is **real,
captured output** from `scripts/run_demo.sh` — not an illustration.

---

## 1. What is AHP?

The **Agent Host Protocol** is an open protocol published by **Microsoft**
for connecting *agent hosts* (surfaces that display and coordinate agent
work — IDEs, chat UIs, dashboards) to *agents* and to each other.

- **Publisher:** Microsoft
- **Status:** Draft / pre-1.0 (the wire format is still evolving; this demo
  negotiates `0.5.2`).
- **License:** MIT
- **Spec & docs:** <https://microsoft.github.io/agent-host-protocol/>
- **Repository:** <https://github.com/microsoft/agent-host-protocol>

The core idea: a **host** owns authoritative, ordered state (sessions,
chats, and the turns within them) and fans mutations out to any number of
subscribed clients over JSON-RPC 2.0 on a WebSocket. Clients apply changes
optimistically and reconcile against the host's monotonic `serverSeq`.

### How it relates to MCP, A2A, and ACP

| Protocol | Question it answers | Relationship to AHP |
|----------|--------------------|----------------------|
| **[MCP](https://modelcontextprotocol.io/)** (Model Context Protocol) | How does a *model* obtain tools/context/resources? | Complementary. MCP feeds a single agent; AHP coordinates the *host surface* that renders many agents' work. |
| **[A2A](https://a2a-protocol.org/)** (Agent-to-Agent) | How do two autonomous agents delegate tasks to each other? | Complementary. A2A is peer delegation between agents; AHP is host↔client state projection. |
| **ACP** (Agent Communication Protocol) | How do agents exchange messages over a shared bus? | Adjacent. AHP focuses specifically on the *host's* authoritative, ordered, multi-subscriber view. |

AHP's distinctive contribution is the **ordered, reconcilable,
multi-client view of session/chat state** — the thing this demo shows.

---

## 2. Core concepts (with spec links)

- **Channels.** State is namespaced by URI. This host exposes three
  families:
  - `ahp-root://` — the root channel; announces sessions appearing and
    disappearing. (`ahptypes.RootResourceURI`.)
  - `ahp-session:/<uuid>` — one session's lifecycle and its chat list.
  - `ahp-chat:/<uuid>` — one chat's turns, deltas, and tool calls.

  See the [resources guide](https://microsoft.github.io/agent-host-protocol/guide/resources).

- **`serverSeq` (ordering guarantee).** The host stamps every accepted
  mutation with the next value of a single monotonic counter. This total
  order is the contract clients reconcile against.

- **Actions.** Discrete state mutations (`chat/turnStarted`,
  `chat/delta`, `chat/toolCallComplete`, …) dispatched by clients via the
  `dispatchAction` notification and re-broadcast by the host with an
  assigned `serverSeq`. See the
  [state & actions guide](https://microsoft.github.io/agent-host-protocol/guide/state).

- **Subscriptions & snapshots.** On `subscribe`, a client receives a
  point-in-time **snapshot** plus the `serverSeq` it is current as of;
  every later action arrives as an incremental broadcast.

- **Version negotiation.** `initialize` carries the client's ordered list
  of supported protocol versions; the host picks the client's most
  preferred version it also supports, or returns
  `UnsupportedProtocolVersion (-32005)`. See the
  [lifecycle guide](https://microsoft.github.io/agent-host-protocol/guide/lifecycle).

- **Reconciliation.** On reconnect a client sends the last `serverSeq` it
  saw; the host replays buffered actions after it, or falls back to fresh
  snapshots if the gap is too large. See the
  [reconciliation guide](https://microsoft.github.io/agent-host-protocol/guide/reconciliation).

---

## 3. What this demo contains

```
host/            A hand-built, spec-compliant AHP HOST (the SDK is client-only)
  server.go            JSON-RPC 2.0 / WebSocket transport + dispatch
  root_channel.go      initialize, subscribe, listSessions, createSession, reconnect
  session_channel.go   disposeSession, unsubscribe, dispatchAction + channel-family validation
  chat_channel.go      createChat + light state application
  reconciliation.go    monotonic serverSeq, write-ahead replay buffer
  validation.go        default-deny method allow-lists + URI validation
  audit_log.go         hash-chained, append-only JSONL audit trail
  main.go              process wiring, env config, graceful shutdown

clients/
  agent/main.go        Official SDK: creates a session+chat, dispatches a scripted turn
  viewer/main.go       Official SDK: subscribes to root, follows the session live
```

The **host** is hand-written because Microsoft's Go SDK
(`github.com/microsoft/agent-host-protocol/clients/go`, packages
`ahp` / `ahptypes` / `ahpws`) ships the **client** side only. Both clients
here use that real SDK; only the host is bespoke.

---

## 4. Running it yourself

Prerequisites: Go 1.23+.

```bash
make run-demo          # builds all three binaries and runs the end-to-end demo
# or manually:
make build             # -> bin/host bin/viewer bin/agent
./scripts/run_demo.sh
```

`run_demo.sh` starts the host, waits for its port, starts the **viewer**
(which subscribes to `ahp-root://` and waits), then starts the **agent**,
which creates a session and drives a scripted assistant turn. The viewer
prints every state change it receives, in `serverSeq` order.

### The coordination flow

```
 agent                     host                       viewer
   |                         |   (subscribed ahp-root://) |
   |-- createSession ------->|                            |
   |                         |-- root/sessionAdded ------>|   seq bump
   |                         |                            |-- subscribe session -->|
   |-- createChat ---------->|-- session/chatAdded ------>|-- subscribe chat ------>|
   |-- dispatch turn ------->|-- chat/* (each stamped) -->|   live, ordered
```

The viewer never talks to the agent directly. It learns the session exists
**only** because the host broadcast `root/sessionAdded` on the root
channel — that is the multi-client sync in action.

---

## 5. A real run (captured output)

The following is verbatim output from an actual `run_demo.sh` execution.

### Host

```
2026/07/17 04:21:19 host: AHP server listening on ws://:12345 (audit log: audit.log)
2026/07/17 04:21:28 host: shutting down
```

### Agent (official SDK client — the "writer")

```
[agent  04:21:21.041] connected; negotiated protocol 0.5.2
[agent  04:21:21.042] created session ahp-session:/aa3fb3dd-4f6a-452c-a8dd-df64fce92964
[agent  04:21:21.043] created chat ahp-chat:/54132829-ab38-4393-85b9-d5cb7521ac2e
[agent  04:21:21.794] dispatched chat/turnStarted (turn-42117488-2900-4073-b93c-1bbe6b5a73c4)
[agent  04:21:22.897] dispatched chat/delta "Let me "
[agent  04:21:23.248] dispatched chat/delta "check the "
[agent  04:21:23.599] dispatched chat/delta "disk usage "
[agent  04:21:23.950] dispatched chat/delta "for you."
[agent  04:21:24.300] dispatched chat/toolCallStart (tool-8d5c85e4-7a1b-402c-a98d-cf4852dfd8b2)
[agent  04:21:25.701] dispatched chat/toolCallComplete (tool-8d5c85e4-7a1b-402c-a98d-cf4852dfd8b2)
[agent  04:21:27.606] dispatched chat/turnComplete (turn-42117488-2900-4073-b93c-1bbe6b5a73c4)
[agent  04:21:27.606] turn finished; agent exiting
```

### Viewer (official SDK client — the "reader") — live synchronized state

```
[viewer 04:21:20.040] connected; negotiated protocol 0.5.2, serverSeq=0
[viewer 04:21:20.040] subscribed to ahp-root:// — waiting for a session to appear
[viewer 04:21:21.042] root/sessionAdded → session "ahp-session:/aa3fb3dd-4f6a-452c-a8dd-df64fce92964" (title "Demo Session", provider "demo")
[viewer 04:21:21.043] session snapshot ahp-session:/aa3fb3dd-4f6a-452c-a8dd-df64fce92964: lifecycle=ready chats=0
[viewer 04:21:21.043] seq=2   session session/chatAdded        chat=ahp-chat:/54132829-ab38-4393-85b9-d5cb7521ac2e
[viewer 04:21:21.043] chat snapshot ahp-chat:/54132829-ab38-4393-85b9-d5cb7521ac2e: title="Demo Chat"
[viewer 04:21:21.795] seq=3   chat   chat/turnStarted         turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 prompt="What is the current disk usage on the build server?"
[viewer 04:21:22.296] seq=4   chat   chat/activityChanged     activity="thinking"
[viewer 04:21:22.897] seq=5   chat   chat/responsePart        turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 part=markdown#part-14b57887-6fb7-43f6-a355-a4a2a1c0c0e2 ""
[viewer 04:21:22.897] seq=6   chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"Let me "
[viewer 04:21:23.248] seq=7   chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"check the "
[viewer 04:21:23.599] seq=8   chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"disk usage "
[viewer 04:21:23.950] seq=9   chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"for you."
[viewer 04:21:24.301] seq=10  chat   chat/toolCallStart       tool="Run shell command" id=tool-8d5c85e4-7a1b-402c-a98d-cf4852dfd8b2
[viewer 04:21:24.801] seq=11  chat   chat/toolCallReady       tool=tool-8d5c85e4-7a1b-402c-a98d-cf4852dfd8b2 invoking: df -h /
[viewer 04:21:25.702] seq=12  chat   chat/toolCallComplete    tool=tool-8d5c85e4-7a1b-402c-a98d-cf4852dfd8b2 success=true
[viewer 04:21:26.202] seq=13  chat   chat/responsePart        turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 part=markdown#part-dbdd387a-4f06-4d73-8169-95f5d9690278 ""
[viewer 04:21:26.202] seq=14  chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"The root "
[viewer 04:21:26.553] seq=15  chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"filesystem is "
[viewer 04:21:26.904] seq=16  chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"at 63% "
[viewer 04:21:27.255] seq=17  chat   chat/delta               turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 +"(28G free)."
[viewer 04:21:27.608] seq=18  chat   chat/activityChanged     activity cleared
[viewer 04:21:27.608] seq=19  chat   chat/turnComplete        turn=turn-42117488-2900-4073-b93c-1bbe6b5a73c4 complete
```

### Reading the output

- **`negotiated protocol 0.5.2`** — the agent and viewer each offered
  `["0.5.2","0.5.1"]`; the host supports `["0.6.0","0.5.2","0.5.1"]`, so
  the client's most-preferred common version, `0.5.2`, was chosen.
- **`root/sessionAdded`** — the viewer, subscribed to `ahp-root://`, is
  notified the instant the agent creates a session. This is the join
  point: cross-client discovery via the root channel.
- **`session/chatAdded` at `seq=2`** — note the viewer subscribed to the
  session *after* `session/ready` (which the host stamped `seq=1`) was
  already committed. Instead of replaying `seq=1`, the viewer's snapshot
  is current as of `seq=1` and it receives `seq=2` onward incrementally —
  exactly the **write-ahead** semantics the reconciliation model
  guarantees (no lost and no double-applied action).
- **Monotonic `seq=2 … 19`** — every mutation the agent dispatched arrives
  at the viewer in strict `serverSeq` order, proving the host's single
  source of ordering truth.

---

## 6. Code walkthrough

### Host: transport & dispatch (`host/server.go`)

One JSON message per WebSocket text frame, JSON-RPC 2.0. Requests
(with an `id`) get a result or error; notifications (no `id`) are actioned
silently. Every dispatch first consults the **default-deny allow-lists**
in `validation.go` — an unknown method returns `MethodNotFound` and is
audited, never guessed at.

### Host: ordering & reconciliation (`host/reconciliation.go`)

A single monotonic `serverSeq` and a bounded, write-ahead replay buffer.
An accepted action is appended to the buffer **before** it is broadcast,
so a client that reconnects with its last-seen `serverSeq` can be handed
exactly the actions it missed (`replay`), or fresh snapshots if the gap
exceeds the buffer window (`snapshot`).

### Host: audit trail (`host/audit_log.go`)

Every accepted or rejected action is appended to a JSONL file where each
line commits to its predecessor via `sha256(prevHash ‖ payload)`. Any
edit, reorder, or deletion breaks the chain from that point on and is
detectable by re-walking the file. `TestAuditChainIsTamperEvident`
demonstrates the verification.

### Clients (`clients/agent`, `clients/viewer`)

Both use the official SDK's `ahp`/`ahptypes`/`ahpws` packages. Because the
SDK's `StateAction.Value` field is guarded by an *unexported* marker
interface, actions must be constructed inline as
`ahptypes.StateAction{Value: &ahptypes.ChatDeltaAction{…}}` — you cannot
write a generic wrapper across the union. The agent dispatches a scripted
"disk usage" turn; the viewer subscribes to root, follows the session, and
prints each envelope as it arrives.

---

## 7. Security

This is a **local-only teaching artifact**. It deliberately skips
authentication, transport encryption, and rate limiting. Read
[SECURITY.md](./SECURITY.md) in full before running it anywhere other than
`127.0.0.1` — in particular, the ["Do NOT deploy this
as-is"](./SECURITY.md#4-do-not-deploy-this-as-is) section, which describes
the AHP authentication flow (RFC 9728 protected-resource metadata + RFC
6750 bearer tokens) you MUST add before exposing it on any untrusted
network.

The audit log's hash chain makes tampering **evident, not impossible** —
production deployments must place it on write-once storage (GCS Bucket
Lock or S3 Object Lock). See SECURITY.md §5.

---

## 8. Next steps

- **Add authentication.** Declare `protectedResources` on the agent's
  `AgentInfo` and require the `authenticate` handshake (SECURITY.md §4).
- **Persist state.** The host keeps sessions/chats in memory; back them
  with durable storage and seed the audit chain's `prevHash` from disk at
  startup.
- **Try reconnection.** Kill the viewer mid-turn and restart it — the
  reconnect path (`host/reconciliation.go`) will replay the actions it
  missed.
- **Explore the spec.** <https://microsoft.github.io/agent-host-protocol/>
