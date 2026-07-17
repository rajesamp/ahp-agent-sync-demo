package main

import "github.com/microsoft/agent-host-protocol/clients/go/ahptypes"

// Write-ahead reconciliation, host side.
//
// The host is the single source of truth for the monotonic serverSeq.
// Every state mutation is expressed as an ActionEnvelope stamped with
// the next serverSeq and appended to an in-memory replay buffer BEFORE
// it is broadcast ("write ahead"). Clients optimistically apply their
// own dispatched actions locally, then reconcile against the ordered
// echo the host broadcasts here. On reconnect the client sends the last
// serverSeq it saw and the host replays everything after it from the
// buffer, so no committed action is lost across a dropped socket.
//
// All methods below assume the caller holds s.mu.

// replayBufferLimit bounds the in-memory buffer. A real host would spill
// to durable storage; when the gap exceeds this window the host falls
// back to shipping fresh snapshots (see handleReconnect).
const replayBufferLimit = 4096

// nextSeq advances and returns the authoritative server sequence.
func (s *Server) nextSeq() int64 {
	s.serverSeq++
	return s.serverSeq
}

// bufferEnvelope records a stamped envelope for later replay, trimming
// the oldest entries once the window is full.
func (s *Server) bufferEnvelope(env ahptypes.ActionEnvelope) {
	s.buffer = append(s.buffer, env)
	if len(s.buffer) > replayBufferLimit {
		s.buffer = s.buffer[len(s.buffer)-replayBufferLimit:]
	}
}

// replayFrom returns every buffered envelope with serverSeq strictly
// greater than lastSeen whose channel is in the caller's subscription
// set, plus whether the requested point is still within the buffer. If
// the buffer no longer covers lastSeen the caller MUST fall back to
// snapshots.
func (s *Server) replayFrom(lastSeen int64, subs map[string]struct{}) (envs []ahptypes.ActionEnvelope, covered bool) {
	if len(s.buffer) == 0 {
		return nil, true
	}
	oldest := s.buffer[0].ServerSeq
	// covered when the next needed seq (lastSeen+1) is at or after the
	// oldest retained entry, or the client is already current.
	covered = lastSeen+1 >= oldest || lastSeen >= s.serverSeq
	for _, env := range s.buffer {
		if env.ServerSeq <= lastSeen {
			continue
		}
		if _, ok := subs[env.Channel]; ok {
			envs = append(envs, env)
		}
	}
	return envs, covered
}
