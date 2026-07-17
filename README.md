# ahp-agent-sync-demo

[![ci](https://github.com/rajksampath/ahp-agent-sync-demo/actions/workflows/ci.yml/badge.svg)](https://github.com/rajksampath/ahp-agent-sync-demo/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.23-00ADD8)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](./LICENSE)

A minimal, runnable demonstration of **multi-client state synchronization**
over Microsoft's [**Agent Host Protocol (AHP)**](https://microsoft.github.io/agent-host-protocol/).

It contains a hand-built, spec-compliant AHP **host** and two clients built
on Microsoft's official (client-only) Go SDK: an **agent** that drives a
scripted assistant turn, and a **viewer** that watches the same session
update live, in order, without ever talking to the agent directly.

> The SDK ships the client side only, so the host is written from scratch
> against the wire protocol. Both clients use the real SDK
> (`github.com/microsoft/agent-host-protocol/clients/go`).

## Architecture

```
                        ┌──────────────────────────────┐
                        │            HOST                │
                        │  authoritative, ordered state  │
                        │  monotonic serverSeq            │
                        │  hash-chained audit log         │
                        └──────────────────────────────┘
                          ▲   JSON-RPC 2.0 / WebSocket  ▲
             dispatchAction│      (ws://:12345)          │broadcasts
                           │                             │
        ┌──────────────────┴───────┐        ┌───────────┴──────────────────┐
        │        AGENT (SDK)        │        │        VIEWER (SDK)           │
        │  createSession            │        │  subscribe ahp-root://        │
        │  createChat               │        │  ── root/sessionAdded ──►     │
        │  dispatch scripted turn   │        │  subscribe that session+chat  │
        │  (deltas, tool call, …)   │        │  print every action in order  │
        └───────────────────────────┘        └───────────────────────────────┘

   Channels:  ahp-root://          root: sessions appear/disappear
              ahp-session:/<uuid>  one session's lifecycle + chat list
              ahp-chat:/<uuid>     one chat's turns, deltas, tool calls
```

The viewer discovers the agent's session **only** because the host
broadcasts `root/sessionAdded` on the root channel — that is the
multi-client sync this demo exists to show.

## Quickstart

Requires Go 1.23+.

```bash
make run-demo          # build all three binaries and run the end-to-end demo
```

Or step by step:

```bash
make build             # -> bin/host bin/viewer bin/agent
make test              # go test ./...
make vet               # go vet ./...
./scripts/run_demo.sh  # start host + viewer, then run the agent
```

You'll see the viewer print each state change in strict `serverSeq` order
as the agent produces it. A full annotated run is in
**[TUTORIAL.md](./TUTORIAL.md)**.

### Configuration

| Env var | Default | Purpose |
|---------|---------|---------|
| `AHP_HOST_ADDR` | `:12345` | Host listen address |
| `AHP_AUDIT_LOG` | `audit.log` | Path to the hash-chained audit log |
| `AHP_HOST_URL` | `ws://127.0.0.1:12345` | URL the clients connect to |

## Container

```bash
make docker-build      # hardened multi-stage build -> distroless static, non-root
```

The image is a `CGO_ENABLED=0` static binary on a distroless base with no
shell or package manager, runs as an unprivileged user, and is compatible
with a read-only root filesystem. See the [Dockerfile](./Dockerfile) and
[SECURITY.md §6](./SECURITY.md#6-supply-chain-controls) for the
supply-chain controls (SBOM, Trivy scan, cosign keyless signing, SLSA
provenance) wired up in [CI](./.github/workflows/ci.yml).

## Security

This is a **local-only teaching artifact**. It deliberately omits
authentication, transport encryption, and rate limiting for simplicity.
**Bind only to `127.0.0.1`; never expose it on an untrusted network** until
you add the AHP authentication flow. Read **[SECURITY.md](./SECURITY.md)**
before running it anywhere.

## Documentation

- **[TUTORIAL.md](./TUTORIAL.md)** — what AHP is, how it relates to MCP /
  A2A / ACP, the core concepts, and a full annotated run with real output.
- **[SECURITY.md](./SECURITY.md)** — threat model, ordering guarantee, the
  "do not deploy as-is" checklist, and supply-chain controls.
- **[LICENSE](./LICENSE)** — MIT.

## License

MIT — see [LICENSE](./LICENSE). AHP itself is an MIT-licensed Microsoft
project; see the [upstream repository](https://github.com/microsoft/agent-host-protocol).
