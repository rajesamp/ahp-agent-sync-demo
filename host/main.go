// Command host is a minimal, spec-compliant Agent Host Protocol (AHP)
// server for the multi-client synchronization demo. It speaks JSON-RPC
// 2.0 over WebSocket, owns the authoritative root/session/chat state,
// sequences every action through a monotonic serverSeq, and fans state
// changes out to subscribed connections.
//
// The official Go SDK is client-only; this binary is the hand-built
// host the two demo clients (agent, viewer) talk to.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := envOr("AHP_HOST_ADDR", ":12345")
	auditPath := envOr("AHP_AUDIT_LOG", "audit.log")

	audit, err := NewAuditLog(auditPath)
	if err != nil {
		log.Fatalf("host: %v", err)
	}
	defer audit.Close()

	srv := NewServer(audit)

	mux := http.NewServeMux()
	mux.Handle("/", srv)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("host: AHP server listening on ws://%s (audit log: %s)", addr, auditPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("host: listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("host: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
