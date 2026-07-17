package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

func TestNegotiateVersion(t *testing.T) {
	cases := []struct {
		name    string
		offered []string
		want    string
	}{
		{"client prefers newest we share", []string{"0.5.2", "0.5.1"}, "0.5.2"},
		{"client order is honored", []string{"0.5.1", "0.5.2"}, "0.5.1"},
		{"skips versions we lack", []string{"9.9.9", "0.5.1"}, "0.5.1"},
		{"no overlap", []string{"1.0.0", "2.0.0"}, ""},
		{"empty offer", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := negotiateVersion(tc.offered); got != tc.want {
				t.Fatalf("negotiateVersion(%v) = %q, want %q", tc.offered, got, tc.want)
			}
		})
	}
}

func TestValidChannelURI(t *testing.T) {
	valid := []string{ahptypes.RootResourceURI, "ahp-session:/abc", "ahp-chat:/xyz"}
	for _, u := range valid {
		if !validChannelURI(u) {
			t.Errorf("expected %q to be valid", u)
		}
	}
	invalid := []string{"", "http://evil", "ahp-session:/", "ahp-chat:/", "file:///etc/passwd"}
	for _, u := range invalid {
		if validChannelURI(u) {
			t.Errorf("expected %q to be invalid", u)
		}
	}
}

func TestValidateActionForChannel(t *testing.T) {
	chatAction := ahptypes.StateAction{Value: &ahptypes.ChatDeltaAction{Type: ahptypes.ActionTypeChatDelta}}
	sessionAction := ahptypes.StateAction{Value: &ahptypes.SessionReadyAction{Type: ahptypes.ActionTypeSessionReady}}

	// A chat action on a chat channel is accepted (empty rejection).
	if r := validateActionForChannel("ahp-chat:/c1", chatAction); r != "" {
		t.Errorf("chat action on chat channel rejected: %q", r)
	}
	// A chat action on a session channel is rejected.
	if r := validateActionForChannel("ahp-session:/s1", chatAction); r == "" {
		t.Error("expected rejection for chat action on session channel")
	}
	// A session action on a chat channel is rejected.
	if r := validateActionForChannel("ahp-chat:/c1", sessionAction); r == "" {
		t.Error("expected rejection for session action on chat channel")
	}
}

func TestDefaultDenyAllowLists(t *testing.T) {
	if !isAllowedRequest("initialize") || !isAllowedNotification("dispatchAction") {
		t.Fatal("expected core methods to be allow-listed")
	}
	if isAllowedRequest("resourceDelete") || isAllowedNotification("evilNotify") {
		t.Fatal("expected non-allow-listed methods to be denied")
	}
}

// TestAuditChainIsTamperEvident writes a few entries, then re-walks the
// file and recomputes the hash chain to prove each line commits to its
// predecessor.
func TestAuditChainIsTamperEvident(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	al, err := NewAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := al.Append(int64(i), "action", "chat/delta", "ahp-chat:/c", "client", true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := al.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	prev := genesisHash
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.PrevHash != prev {
			t.Fatalf("broken chain: prevHash %q != expected %q", e.PrevHash, prev)
		}
		payload := e
		payload.Hash = ""
		payload.PrevHash = ""
		raw, _ := json.Marshal(payload)
		sum := sha256.Sum256(append([]byte(prev), raw...))
		if want := hex.EncodeToString(sum[:]); want != e.Hash {
			t.Fatalf("hash mismatch: got %q want %q", e.Hash, want)
		}
		prev = e.Hash
	}
}
