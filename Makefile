# AHP multi-client sync demo — build, run, and supply-chain targets.
#
# The security targets (sbom, sign, verify) document the supply-chain
# controls the CI pipeline runs. They are safe no-ops-with-guidance when
# the underlying tool is not installed locally, so `make` never fails
# just because syft/cosign are absent from a dev box.

IMAGE       ?= ahp-agent-sync-demo
TAG         ?= dev
IMAGE_REF   := $(IMAGE):$(TAG)
BIN         := bin
GO          ?= go

.PHONY: all build host viewer agent run-demo test vet tidy clean \
        docker-build sbom sign verify

all: build

## build: compile the host and both client binaries into ./bin
build: host viewer agent

host:
	$(GO) build -o $(BIN)/host ./host

viewer:
	$(GO) build -o $(BIN)/viewer ./clients/viewer

agent:
	$(GO) build -o $(BIN)/agent ./clients/agent

## run-demo: build everything and run the end-to-end sync demo
run-demo:
	./scripts/run_demo.sh

## test: run the Go test suite
test:
	$(GO) test ./...

## vet: static analysis
vet:
	$(GO) vet ./...

## tidy: sync go.mod/go.sum
tidy:
	$(GO) mod tidy

## clean: remove build artifacts and the local audit log
clean:
	rm -rf $(BIN) audit.log host.log demo_viewer.log demo_agent.log

## docker-build: build the hardened container image
docker-build:
	docker build -t $(IMAGE_REF) .

## sbom: generate a CycloneDX SBOM for the image (requires syft)
sbom:
	@if command -v syft >/dev/null 2>&1; then \
		syft $(IMAGE_REF) -o cyclonedx-json=sbom.cdx.json ; \
		echo "wrote sbom.cdx.json" ; \
	else \
		echo "syft not installed — CI runs anchore/sbom-action to produce this artifact." ; \
	fi

## sign: cosign keyless (OIDC) signature of the image (requires cosign)
sign:
	@if command -v cosign >/dev/null 2>&1; then \
		COSIGN_EXPERIMENTAL=1 cosign sign --yes $(IMAGE_REF) ; \
	else \
		echo "cosign not installed — CI signs keylessly via OIDC in the release job." ; \
	fi

## verify: verify the image's keyless cosign signature (requires cosign)
verify:
	@if command -v cosign >/dev/null 2>&1; then \
		COSIGN_EXPERIMENTAL=1 cosign verify $(IMAGE_REF) \
		  --certificate-identity-regexp '.*' \
		  --certificate-oidc-issuer-regexp '.*' ; \
	else \
		echo "cosign not installed — see TUTORIAL.md for the verification command." ; \
	fi
