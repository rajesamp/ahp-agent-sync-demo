# syntax=docker/dockerfile:1

# ─── Stage 1: build a fully static host binary ───────────────────────────
# Pin the toolchain image by tag; CI additionally records the resolved
# digest in the SBOM so the exact builder is auditable.
FROM golang:1.25.12-bookworm AS build

WORKDIR /src

# Cache module downloads separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO disabled + netgo/osusergo → a self-contained static binary that
# runs on a scratch/distroless base with no libc. -trimpath and the
# stripped symbol table keep the artifact reproducible and small.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/host ./host

# ─── Stage 2: minimal, non-root runtime ──────────────────────────────────
# Distroless "static" carries only CA certs, /etc/passwd (with a nonroot
# user), and tzdata — no shell, no package manager, minimal attack
# surface. PIN BY DIGEST in production so the base cannot drift; resolve
# the current digest with:
#
#   crane digest gcr.io/distroless/static-debian12:nonroot
#
# then replace the tag below with `@sha256:<digest>`. The tag form is
# kept here so the demo builds without network-resolving a digest first.
FROM gcr.io/distroless/static-debian12:nonroot
# Example pinned form (uncomment and set the digest for production):
# FROM gcr.io/distroless/static-debian12@sha256:<digest>

WORKDIR /app
COPY --from=build /out/host /app/host

# Run as the image's built-in unprivileged user (uid 65532). The audit
# log defaults to /app; mount a writable volume there, or point
# AHP_AUDIT_LOG at a writable path, when running read-only rootfs.
USER nonroot:nonroot

EXPOSE 12345
ENV AHP_HOST_ADDR=":12345" \
    AHP_AUDIT_LOG="/tmp/audit.log"

# No shell in the image, so use exec form.
ENTRYPOINT ["/app/host"]
