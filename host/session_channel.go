package main

import (
	"encoding/json"
	"strings"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// Session channel: per-session lifecycle, chat catalogue, and the
// dispatchAction fan-in. This is where untrusted client actions are
// validated against the channel they target before being sequenced and
// echoed to subscribers.

func (s *Server) handleDisposeSession(c *conn, req ahptypes.JsonRpcRequest) {
	var p ahptypes.DisposeSessionParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "invalid disposeSession params")
		return
	}
	if !isSessionURI(p.Channel) {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "channel must be an ahp-session URI")
		return
	}

	s.mu.Lock()
	if _, ok := s.sessions[p.Channel]; !ok {
		s.mu.Unlock()
		s.sendError(c, &req.ID, ahptypes.ErrorCodeSessionNotFound, "no such session: "+p.Channel)
		return
	}
	delete(s.sessions, p.Channel)
	for chatURI, owner := range s.chatOf {
		if owner == p.Channel {
			delete(s.chats, chatURI)
			delete(s.chatOf, chatURI)
		}
	}

	s.sendResult(c, req.ID, map[string]any{})
	s.broadcastProtocolLocked(ahptypes.RootResourceURI, "root/sessionRemoved",
		ahptypes.SessionRemovedParams{Channel: ahptypes.RootResourceURI, Session: p.Channel})
	s.mu.Unlock()
}

func (s *Server) handleUnsubscribe(c *conn, n ahptypes.JsonRpcNotification) {
	var p ahptypes.UnsubscribeParams
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return
	}
	s.mu.Lock()
	if set := s.subscribers[p.Channel]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(s.subscribers, p.Channel)
		}
	}
	s.mu.Unlock()
	_ = s.audit.Append(0, "in", "unsubscribe", p.Channel, c.clientID, true, "")
}

// handleDispatchAction is the write-ahead ingress for client actions.
//
// Validation follows the host threat model:
//   - Malformed frame or channel URI → dropped (audited), never echoed.
//   - Action targeting a nonexistent session/chat → silently ignored
//     (no state exists to mutate; the client will reconcile on its own).
//   - Action whose type does not belong to the targeted channel family →
//     echoed back with a rejectionReason so the dispatching client can
//     roll back its optimistic local apply.
func (s *Server) handleDispatchAction(c *conn, n ahptypes.JsonRpcNotification) {
	var p ahptypes.DispatchActionParams
	if err := json.Unmarshal(n.Params, &p); err != nil {
		_ = s.audit.Append(0, "in", "dispatchAction", "", c.clientID, false, "malformed params")
		return
	}
	if !validChannelURI(p.Channel) {
		_ = s.audit.Append(0, "in", "dispatchAction", p.Channel, c.clientID, false, "malformed channel URI")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.channelExistsLocked(p.Channel) {
		// Nonexistent target: nothing to sequence. Record and drop.
		_ = s.audit.Append(0, "in", "dispatchAction",
			p.Channel, c.clientID, false, "channel does not exist: "+string(actionTypeOf(p.Action)))
		return
	}

	rejection := validateActionForChannel(p.Channel, p.Action)
	origin := &ahptypes.ActionOrigin{ClientId: c.clientID, ClientSeq: p.ClientSeq}
	if rejection == "" {
		s.applyActionLocked(p.Channel, p.Action)
	}
	s.broadcastActionLocked(p.Channel, p.Action, origin, rejection)
}

// channelExistsLocked reports whether a well-formed channel URI refers
// to live state. Caller must hold s.mu.
func (s *Server) channelExistsLocked(uri string) bool {
	switch {
	case uri == ahptypes.RootResourceURI:
		return true
	case isSessionURI(uri):
		_, ok := s.sessions[uri]
		return ok
	case isChatURI(uri):
		_, ok := s.chats[uri]
		return ok
	default:
		return false
	}
}

// validateActionForChannel enforces that an action's type family matches
// the channel it targets (session/* on session channels, chat/* on chat
// channels, root/* on the root channel). A non-empty return is the
// rejectionReason echoed to subscribers.
func validateActionForChannel(uri string, action ahptypes.StateAction) string {
	family, _, _ := strings.Cut(string(actionTypeOf(action)), "/")
	switch {
	case uri == ahptypes.RootResourceURI:
		if family != "root" {
			return "root channel accepts only root/* actions"
		}
	case isSessionURI(uri):
		if family != "session" {
			return "session channel accepts only session/* actions"
		}
	case isChatURI(uri):
		if family != "chat" {
			return "chat channel accepts only chat/* actions"
		}
	}
	return ""
}
