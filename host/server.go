package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// hostSupportedVersions is the set of protocol versions this host can
// speak, most-preferred first. Negotiation picks the client's
// most-preferred entry that also appears here; if the intersection is
// empty the host returns UnsupportedProtocolVersion (-32005).
var hostSupportedVersions = []string{"0.6.0", "0.5.2", "0.5.1"}

// Server is a minimal, spec-compliant AHP host. It owns the
// authoritative state of the root, session, and chat channels, sequences
// every action through a single monotonic serverSeq, and fans state
// changes out to subscribed connections.
//
// The official Go SDK is client-only; this type is the hand-built host
// counterpart the SDK talks to.
type Server struct {
	mu        sync.Mutex
	serverSeq int64

	root     ahptypes.RootState
	sessions map[string]*ahptypes.SessionState // session URI → state
	chats    map[string]*ahptypes.ChatState    // chat URI → state
	chatOf   map[string]string                 // chat URI → owning session URI

	// subscribers maps a channel URI to the connections receiving its
	// action stream.
	subscribers map[string]map[*conn]struct{}
	conns       map[*conn]struct{}

	buffer []ahptypes.ActionEnvelope
	audit  *AuditLog
}

// conn is a single WebSocket client connection. Outbound frames flow
// through the buffered out channel and are written in order by a
// dedicated pump goroutine, which preserves per-connection serverSeq
// ordering without holding the server lock across network writes.
type conn struct {
	ws       *websocket.Conn
	clientID string
	out      chan []byte
	closed   chan struct{}
	closeOne sync.Once
}

// NewServer constructs a host with a fixed demo agent catalogue in root
// state and an open audit log.
func NewServer(audit *AuditLog) *Server {
	desc := "Local simulated agent for the AHP multi-client sync demo"
	return &Server{
		root: ahptypes.RootState{
			Agents: []ahptypes.AgentInfo{{
				Provider:    "demo",
				DisplayName: "Demo Agent",
				Description: desc,
				Models: []ahptypes.SessionModelInfo{{
					Id:       "demo-model-1",
					Provider: "demo",
					Name:     "Demo Model",
				}},
				// No protectedResources: this agent requires no auth.
				// See SECURITY.md for why that is local-only.
			}},
		},
		sessions:    map[string]*ahptypes.SessionState{},
		chats:       map[string]*ahptypes.ChatState{},
		chatOf:      map[string]string{},
		subscribers: map[string]map[*conn]struct{}{},
		conns:       map[*conn]struct{}{},
		audit:       audit,
	}
}

// ServeHTTP upgrades an incoming HTTP request to a WebSocket and serves
// the AHP session on it. One JSON-RPC message per text frame.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		log.Printf("host: websocket accept: %v", err)
		return
	}
	ws.SetReadLimit(32 * 1024 * 1024)

	c := &conn{
		ws:     ws,
		out:    make(chan []byte, 256),
		closed: make(chan struct{}),
	}
	s.register(c)
	defer s.drop(c)

	ctx := r.Context()
	go c.writePump(ctx)
	s.readLoop(ctx, c)
}

func (s *Server) register(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[c] = struct{}{}
}

// drop tears down a connection: closes its write pump and removes it
// from every subscription set.
func (s *Server) drop(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	for uri, set := range s.subscribers {
		delete(set, c)
		if len(set) == 0 {
			delete(s.subscribers, uri)
		}
	}
	s.mu.Unlock()
	c.close()
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
}

func (c *conn) close() {
	c.closeOne.Do(func() { close(c.closed) })
}

// writePump serializes outbound frames for one connection.
func (c *conn) writePump(ctx context.Context) {
	for {
		select {
		case <-c.closed:
			return
		case <-ctx.Done():
			return
		case frame := <-c.out:
			if err := c.ws.Write(ctx, websocket.MessageText, frame); err != nil {
				c.close()
				return
			}
		}
	}
}

// send enqueues one already-encoded frame. Drops the frame (and closes
// the connection) if the client cannot keep up, mirroring the SDK's
// slow-consumer policy.
func (c *conn) send(frame []byte) {
	select {
	case c.out <- frame:
	case <-c.closed:
	default:
		// Buffer full: the consumer is too slow to preserve ordered
		// delivery. Close so it must reconnect and replay.
		c.close()
	}
}

func (s *Server) readLoop(ctx context.Context, c *conn) {
	for {
		mt, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg ahptypes.JsonRpcMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			// Malformed frame: cannot correlate to an id, so reply with
			// a null-id parse error and keep the connection open.
			s.sendError(c, nil, ahptypes.ErrorCodeParseError, "invalid JSON-RPC frame")
			continue
		}
		s.handleMessage(c, msg)
	}
}

// handleMessage routes a parsed inbound message. Requests and
// notifications are default-denied unless allow-listed in validation.go.
func (s *Server) handleMessage(c *conn, msg ahptypes.JsonRpcMessage) {
	switch {
	case msg.Request != nil:
		req := msg.Request
		if !isAllowedRequest(req.Method) {
			_ = s.audit.Append(0, "in", req.Method, "", c.clientID, false, "method not allow-listed")
			s.sendError(c, &req.ID, ahptypes.ErrorCodeMethodNotFound, "method not permitted: "+req.Method)
			return
		}
		s.handleRequest(c, *req)
	case msg.Notification != nil:
		n := msg.Notification
		if !isAllowedNotification(n.Method) {
			_ = s.audit.Append(0, "in", n.Method, "", c.clientID, false, "notification not allow-listed")
			// Notifications carry no id; surface the rejection with a
			// null-id error rather than dropping it silently.
			s.sendError(c, nil, ahptypes.ErrorCodeMethodNotFound, "notification not permitted: "+n.Method)
			return
		}
		s.handleNotification(c, *n)
	default:
		// Responses from a client are unexpected on this host.
	}
}

func (s *Server) handleRequest(c *conn, req ahptypes.JsonRpcRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(c, req)
	case "ping":
		s.sendResult(c, req.ID, map[string]any{})
	case "reconnect":
		s.handleReconnect(c, req)
	case "subscribe":
		s.handleSubscribe(c, req)
	case "listSessions":
		s.handleListSessions(c, req)
	case "createSession":
		s.handleCreateSession(c, req)
	case "disposeSession":
		s.handleDisposeSession(c, req)
	case "createChat":
		s.handleCreateChat(c, req)
	}
}

func (s *Server) handleNotification(c *conn, n ahptypes.JsonRpcNotification) {
	switch n.Method {
	case "dispatchAction":
		s.handleDispatchAction(c, n)
	case "unsubscribe":
		s.handleUnsubscribe(c, n)
	}
}

// ─── subscription + broadcast plumbing ──────────────────────────────────

// subscribe adds c to a channel's subscriber set. Caller must hold s.mu.
func (s *Server) subscribeLocked(c *conn, uri string) {
	set := s.subscribers[uri]
	if set == nil {
		set = map[*conn]struct{}{}
		s.subscribers[uri] = set
	}
	set[c] = struct{}{}
}

// broadcastAction stamps an action with the next serverSeq, buffers it
// for replay, audits it, and fans the `action` notification out to every
// current subscriber of the channel. Caller must hold s.mu.
//
// A non-empty rejection echoes the action back with a rejectionReason
// (per the session/chat validation rules) instead of applying it.
func (s *Server) broadcastActionLocked(channel string, action ahptypes.StateAction, origin *ahptypes.ActionOrigin, rejection string) int64 {
	seq := s.nextSeq()
	env := ahptypes.ActionEnvelope{
		Channel:   channel,
		Action:    action,
		ServerSeq: seq,
		Origin:    origin,
	}
	if rejection != "" {
		env.RejectionReason = &rejection
	}
	s.bufferEnvelope(env)

	accepted := rejection == ""
	clientID := ""
	if origin != nil {
		clientID = origin.ClientId
	}
	_ = s.audit.Append(seq, "action", string(actionTypeOf(action)), channel, clientID, accepted, rejection)

	frame := encodeNotification("action", env)
	for c := range s.subscribers[channel] {
		c.send(frame)
	}
	return seq
}

// broadcastProtocolLocked fans a protocol notification (root/sessionAdded
// etc.) out to subscribers of channel. These are ephemeral and are NOT
// buffered for replay, matching the spec. Caller must hold s.mu.
func (s *Server) broadcastProtocolLocked(channel, method string, params any) {
	frame := encodeNotification(method, params)
	for c := range s.subscribers[channel] {
		c.send(frame)
	}
	_ = s.audit.Append(0, "notify", method, channel, "", true, "")
}

// ─── frame helpers ──────────────────────────────────────────────────────

func encodeNotification(method string, params any) []byte {
	raw, _ := json.Marshal(params)
	msg := ahptypes.JsonRpcMessage{Notification: &ahptypes.JsonRpcNotification{
		JsonRpc: ahptypes.JsonRpcV2,
		Method:  method,
		Params:  raw,
	}}
	out, _ := json.Marshal(msg)
	return out
}

func (s *Server) sendResult(c *conn, id uint64, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		s.sendError(c, &id, ahptypes.ErrorCodeInternalError, err.Error())
		return
	}
	msg := ahptypes.JsonRpcMessage{SuccessResponse: &ahptypes.JsonRpcSuccessResponse{
		JsonRpc: ahptypes.JsonRpcV2,
		ID:      id,
		Result:  raw,
	}}
	out, _ := json.Marshal(msg)
	c.send(out)
}

// sendError emits a JSON-RPC error response. id may be nil for frames
// that cannot be correlated (parse errors, denied notifications).
func (s *Server) sendError(c *conn, id *uint64, code int32, message string) {
	var realID uint64
	if id != nil {
		realID = *id
	}
	msg := ahptypes.JsonRpcMessage{ErrorResponse: &ahptypes.JsonRpcErrorResponse{
		JsonRpc: ahptypes.JsonRpcV2,
		ID:      realID,
		Error:   ahptypes.JsonRpcError{Code: code, Message: message},
	}}
	out, _ := json.Marshal(msg)
	c.send(out)
}
