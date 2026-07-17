package main

import (
	"encoding/json"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// Chat channel: chat creation within a session and the light-touch
// application of chat/* actions to authoritative host state.
//
// The host is deliberately a thin sequencer: it does not reconstruct
// full turn transcripts from the streaming action deltas (the SDK's
// reducers do that on the client). It keeps just enough chat state —
// existence, title, status, activity — to answer subscribe/snapshot and
// to keep the session catalogue coherent.

func (s *Server) handleCreateChat(c *conn, req ahptypes.JsonRpcRequest) {
	var p ahptypes.CreateChatParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "invalid createChat params")
		return
	}
	if !isSessionURI(p.Channel) {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "channel must be an ahp-session URI")
		return
	}
	if !isChatURI(p.Chat) {
		s.sendError(c, &req.ID, ahptypes.ErrorCodeInvalidParams, "chat must be an ahp-chat URI")
		return
	}

	s.mu.Lock()
	sess, ok := s.sessions[p.Channel]
	if !ok {
		s.mu.Unlock()
		s.sendError(c, &req.ID, ahptypes.ErrorCodeSessionNotFound, "no such session: "+p.Channel)
		return
	}
	if _, exists := s.chats[p.Chat]; exists {
		s.mu.Unlock()
		s.sendError(c, &req.ID, ahptypes.ErrorCodeAlreadyExists, "chat already exists: "+p.Chat)
		return
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	chat := &ahptypes.ChatState{
		Resource:   p.Chat,
		Title:      "Demo Chat",
		Status:     ahptypes.SessionStatusIdle,
		ModifiedAt: now,
		Origin:     &ahptypes.ChatOrigin{Value: &ahptypes.ChatUserOrigin{Kind: ahptypes.ChatOriginKindUser}},
		Turns:      []ahptypes.Turn{},
	}
	s.chats[p.Chat] = chat
	s.chatOf[p.Chat] = p.Channel

	summary := ahptypes.ChatSummary{
		Resource:   chat.Resource,
		Title:      chat.Title,
		Status:     chat.Status,
		ModifiedAt: chat.ModifiedAt,
		Origin:     chat.Origin,
	}
	sess.Chats = append(sess.Chats, summary)
	if sess.DefaultChat == nil {
		uri := p.Chat
		sess.DefaultChat = &uri
	}

	s.sendResult(c, req.ID, map[string]any{})
	// Announce the new chat on the session channel so session
	// subscribers learn about it and can subscribe to the chat URI.
	s.broadcastActionLocked(p.Channel,
		ahptypes.StateAction{Value: &ahptypes.SessionChatAddedAction{
			Type:    ahptypes.ActionTypeSessionChatAdded,
			Summary: summary,
		}}, nil, "")
	s.mu.Unlock()
}

// applyActionLocked folds an accepted action into authoritative host
// state, to the limited extent the host tracks it. Unhandled action
// types are relayed verbatim without a local state change. Caller must
// hold s.mu.
func (s *Server) applyActionLocked(channel string, action ahptypes.StateAction) {
	switch v := action.Value.(type) {
	case *ahptypes.ChatActivityChangedAction:
		if chat, ok := s.chats[channel]; ok {
			chat.Activity = v.Activity
			chat.ModifiedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		}
	case *ahptypes.ChatTurnStartedAction:
		if chat, ok := s.chats[channel]; ok {
			chat.Status = ahptypes.SessionStatusInProgress
		}
	case *ahptypes.ChatTurnCompleteAction, *ahptypes.ChatTurnCancelledAction:
		if chat, ok := s.chats[channel]; ok {
			chat.Status = ahptypes.SessionStatusIdle
			chat.Activity = nil
		}
	case *ahptypes.SessionActivityChangedAction:
		if sess, ok := s.sessions[channel]; ok {
			sess.Activity = v.Activity
		}
	}
}
