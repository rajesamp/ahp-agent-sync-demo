package main

import (
	"encoding/json"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// Root channel: the agent/session catalogue. Handles the initialize
// handshake (version negotiation + initial snapshots), subscribe,
// reconnect replay, listSessions, and createSession — the entry points
// through which a client discovers the host and spawns sessions.

// serverName identifies this host implementation in initialize results.
const serverName = "ahp-agent-sync-demo-host"

// negotiateVersion returns the client's most-preferred offered version
// that this host also supports, or "" if the intersection is empty.
func negotiateVersion(offered []string) string {
	for _, want := range offered {
		for _, have := range hostSupportedVersions {
			if want == have {
				return want
			}
		}
	}
	return ""
}

func (s *Server) handleInitialize(c *conn, req ahptypes.JsonRpcRequest) {
	var p ahptypes.InitializeParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "invalid initialize params")
		return
	}

	version := negotiateVersion(p.ProtocolVersions)
	if version == "" {
		_ = s.audit.Append(0, "in", "initialize", "", p.ClientId, false, "no acceptable protocol version")
		s.sendError(c, &req.ID, ahptypes.ErrorCodeUnsupportedProtocolVersion,
			"no mutually supported protocol version")
		return
	}

	s.mu.Lock()
	c.clientID = p.ClientId
	snapshots := make([]ahptypes.Snapshot, 0, len(p.InitialSubscriptions))
	for _, uri := range p.InitialSubscriptions {
		if !validChannelURI(uri) {
			continue
		}
		s.subscribeLocked(c, uri)
		if snap, ok := s.snapshotForLocked(uri); ok {
			snapshots = append(snapshots, *snap)
		}
	}
	seq := s.serverSeq
	s.mu.Unlock()

	_ = s.audit.Append(seq, "in", "initialize", "", p.ClientId, true, "")

	name := serverName
	s.sendResult(c, req.ID, ahptypes.InitializeResult{
		ProtocolVersion: version,
		ServerSeq:       seq,
		ServerInfo:      &ahptypes.Implementation{Name: name},
		Snapshots:       snapshots,
	})
}

func (s *Server) handleSubscribe(c *conn, req ahptypes.JsonRpcRequest) {
	var p ahptypes.SubscribeParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "invalid subscribe params")
		return
	}
	if !validChannelURI(p.Channel) {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "malformed channel URI")
		return
	}

	s.mu.Lock()
	snap, ok := s.snapshotForLocked(p.Channel)
	if !ok {
		s.mu.Unlock()
		s.sendError(c, &req.ID, ahptypes.ErrorCodeNotFound, "no such channel: "+p.Channel)
		return
	}
	s.subscribeLocked(c, p.Channel)
	s.mu.Unlock()

	_ = s.audit.Append(snap.FromSeq, "in", "subscribe", p.Channel, c.clientID, true, "")
	s.sendResult(c, req.ID, ahptypes.SubscribeResult{Snapshot: snap})
}

func (s *Server) handleListSessions(c *conn, req ahptypes.JsonRpcRequest) {
	s.mu.Lock()
	items := make([]ahptypes.SessionSummary, 0, len(s.sessions))
	for uri, sess := range s.sessions {
		items = append(items, s.buildSummaryLocked(uri, sess))
	}
	s.mu.Unlock()

	s.sendResult(c, req.ID, ahptypes.ListSessionsResult{Items: items})
}

func (s *Server) handleCreateSession(c *conn, req ahptypes.JsonRpcRequest) {
	var p ahptypes.CreateSessionParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "invalid createSession params")
		return
	}
	if !isSessionURI(p.Channel) {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "channel must be an ahp-session URI")
		return
	}

	s.mu.Lock()
	if _, exists := s.sessions[p.Channel]; exists {
		s.mu.Unlock()
		s.sendError(c, &req.ID, ahptypes.ErrorCodeSessionAlreadyExists, "session already exists: "+p.Channel)
		return
	}

	provider := "demo"
	if p.Provider != nil {
		provider = *p.Provider
	}
	sess := &ahptypes.SessionState{
		Provider:      provider,
		Title:         "Demo Session",
		Status:        ahptypes.SessionStatusIdle,
		Lifecycle:     ahptypes.SessionLifecycleReady,
		ActiveClients: []ahptypes.SessionActiveClient{},
		Chats:         []ahptypes.ChatSummary{},
	}
	s.sessions[p.Channel] = sess
	summary := s.buildSummaryLocked(p.Channel, sess)

	// Reply first, then fan the lifecycle notification out to every
	// root subscriber so late-joining state stays consistent.
	s.sendResult(c, req.ID, map[string]any{})
	s.broadcastProtocolLocked(ahptypes.RootResourceURI, "root/sessionAdded",
		ahptypes.SessionAddedParams{Channel: ahptypes.RootResourceURI, Summary: summary})
	// Announce readiness on the session channel itself (buffered for
	// replay) so a client that subscribes later still observes it.
	s.broadcastActionLocked(p.Channel,
		ahptypes.StateAction{Value: &ahptypes.SessionReadyAction{Type: ahptypes.ActionTypeSessionReady}},
		nil, "")
	s.mu.Unlock()
}

func (s *Server) handleReconnect(c *conn, req ahptypes.JsonRpcRequest) {
	var p ahptypes.ReconnectParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "invalid reconnect params")
		return
	}

	s.mu.Lock()
	c.clientID = p.ClientId
	subs := map[string]struct{}{}
	var missing []string
	for _, uri := range p.Subscriptions {
		if _, ok := s.snapshotForLocked(uri); !ok {
			missing = append(missing, uri) // resource gone (e.g. disposed session)
			continue
		}
		s.subscribeLocked(c, uri)
		subs[uri] = struct{}{}
	}

	envs, covered := s.replayFrom(p.LastSeenServerSeq, subs)
	if missing == nil {
		missing = []string{}
	}

	if covered {
		if envs == nil {
			envs = []ahptypes.ActionEnvelope{}
		}
		s.mu.Unlock()
		_ = s.audit.Append(p.LastSeenServerSeq, "in", "reconnect", "", p.ClientId, true, "replay")
		s.sendResult(c, req.ID, map[string]any{
			"type":    ahptypes.ReconnectResultTypeReplay,
			"actions": envs,
			"missing": missing,
		})
		return
	}

	// Gap exceeds the replay buffer: ship fresh snapshots instead.
	snaps := make([]ahptypes.Snapshot, 0, len(subs))
	for uri := range subs {
		if snap, ok := s.snapshotForLocked(uri); ok {
			snaps = append(snaps, *snap)
		}
	}
	s.mu.Unlock()
	_ = s.audit.Append(p.LastSeenServerSeq, "in", "reconnect", "", p.ClientId, true, "snapshot")
	s.sendResult(c, req.ID, map[string]any{
		"type":      ahptypes.ReconnectResultTypeSnapshot,
		"snapshots": snaps,
	})
}

// snapshotForLocked builds the current snapshot for a channel URI, or
// reports false if the channel has no state (unknown/nonexistent).
// Caller must hold s.mu.
func (s *Server) snapshotForLocked(uri string) (*ahptypes.Snapshot, bool) {
	var state ahptypes.SnapshotState
	switch {
	case uri == ahptypes.RootResourceURI:
		root := s.root
		state = ahptypes.SnapshotState{Root: &root}
	case isSessionURI(uri):
		sess, ok := s.sessions[uri]
		if !ok {
			return nil, false
		}
		state = ahptypes.SnapshotState{Session: sess}
	case isChatURI(uri):
		chat, ok := s.chats[uri]
		if !ok {
			return nil, false
		}
		state = ahptypes.SnapshotState{Chat: chat}
	default:
		return nil, false
	}
	return &ahptypes.Snapshot{Resource: uri, State: state, FromSeq: s.serverSeq}, true
}

// buildSummaryLocked derives the catalogue SessionSummary from full
// session state. Caller must hold s.mu.
func (s *Server) buildSummaryLocked(uri string, sess *ahptypes.SessionState) ahptypes.SessionSummary {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	return ahptypes.SessionSummary{
		Provider:   sess.Provider,
		Title:      sess.Title,
		Status:     sess.Status,
		Resource:   uri,
		CreatedAt:  now,
		ModifiedAt: now,
	}
}
