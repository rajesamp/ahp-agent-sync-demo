package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// AuditLog is an append-only, hash-chained JSONL record of every action
// the host dispatches. Each line commits to its predecessor via
//
//	thisHash = sha256(prevHash || canonicalPayload)
//
// so any tampering with, reordering of, or deletion of an earlier line
// breaks the chain from that point forward and is detectable by
// re-walking the file. This is a tamper-EVIDENT log, not a
// tamper-PROOF one: a writer with filesystem access can still rewrite
// the whole chain. In production the file MUST live on write-once
// storage (GCS bucket-lock, S3 Object Lock in compliance mode) so the
// append-only property is enforced by the platform, not by convention.
type AuditLog struct {
	mu       sync.Mutex
	f        *os.File
	prevHash string
}

// AuditEntry is one line of the log.
type AuditEntry struct {
	Seq             int64  `json:"seq"`
	Timestamp       string `json:"ts"`
	Direction       string `json:"direction"`
	Method          string `json:"method"`
	Channel         string `json:"channel"`
	ClientID        string `json:"clientId,omitempty"`
	Accepted        bool   `json:"accepted"`
	RejectionReason string `json:"rejectionReason,omitempty"`
	PrevHash        string `json:"prevHash"`
	Hash            string `json:"hash"`
}

// genesisHash seeds the chain. Any fixed, well-known value works; using
// the SHA-256 of a domain-separation string documents intent.
var genesisHash = sha256Hex([]byte("ahp-agent-sync-demo/audit/v1"))

// NewAuditLog opens (creating if needed) the JSONL audit file in append
// mode. The chain continues from genesis on every process start; a
// production deployment would instead seed prevHash from the last line
// already on disk.
func NewAuditLog(path string) (*AuditLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &AuditLog{f: f, prevHash: genesisHash}, nil
}

// Append writes one hash-chained entry. seq/method/channel/clientID
// describe the dispatched action; accepted and rejectionReason record
// the validation outcome. The returned error is non-nil only on a write
// failure — the caller SHOULD treat that as fatal, since a gap in the
// chain defeats the log's purpose.
func (a *AuditLog) Append(seq int64, direction, method, channel, clientID string, accepted bool, rejection string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	entry := AuditEntry{
		Seq:             seq,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		Direction:       direction,
		Method:          method,
		Channel:         channel,
		ClientID:        clientID,
		Accepted:        accepted,
		RejectionReason: rejection,
		PrevHash:        a.prevHash,
	}

	// Hash the payload with prevHash omitted from the hashed bytes so
	// the digest commits to content + predecessor exactly once.
	payload := entry
	payload.Hash = ""
	payload.PrevHash = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	entry.Hash = sha256Hex(append([]byte(a.prevHash), raw...))

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit: marshal line: %w", err)
	}
	if _, err := a.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	a.prevHash = entry.Hash
	return nil
}

// Close flushes and closes the underlying file.
func (a *AuditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.Close()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
