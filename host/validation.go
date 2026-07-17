package main

import (
	"strings"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// Default-deny method allow-lists. Any JSON-RPC method not present here
// is rejected — never silently ignored — per the host's threat model
// (untrusted clients on a shared endpoint). Adding a new command is a
// deliberate, reviewable act: extend the relevant set below.

// allowedRequests is the set of client → server request methods the
// host will service. Everything else returns MethodNotFound.
var allowedRequests = map[string]struct{}{
	"initialize":     {},
	"ping":           {},
	"reconnect":      {},
	"subscribe":      {},
	"listSessions":   {},
	"createSession":  {},
	"disposeSession": {},
	"createChat":     {},
}

// allowedNotifications is the set of client → server notification
// methods the host will act on. Everything else is rejected.
var allowedNotifications = map[string]struct{}{
	"dispatchAction": {},
	"unsubscribe":    {},
}

func isAllowedRequest(method string) bool {
	_, ok := allowedRequests[method]
	return ok
}

func isAllowedNotification(method string) bool {
	_, ok := allowedNotifications[method]
	return ok
}

// validChannelURI enforces the AHP channel URI scheme. The host only
// exposes root, session, and chat channels, so anything else is
// malformed as far as this host is concerned and is rejected with
// InvalidParams before any state is touched.
func validChannelURI(uri string) bool {
	switch {
	case uri == ahptypes.RootResourceURI:
		return true
	case strings.HasPrefix(uri, "ahp-session:/"):
		return len(uri) > len("ahp-session:/")
	case strings.HasPrefix(uri, "ahp-chat:/"):
		return len(uri) > len("ahp-chat:/")
	default:
		return false
	}
}

func isSessionURI(uri string) bool { return strings.HasPrefix(uri, "ahp-session:/") }
func isChatURI(uri string) bool    { return strings.HasPrefix(uri, "ahp-chat:/") }

// actionTypeOf extracts the discriminant of a decoded StateAction so
// the dispatch validators can reason about it without a type switch on
// every variant.
func actionTypeOf(a ahptypes.StateAction) ahptypes.ActionType {
	switch v := a.Value.(type) {
	case *ahptypes.SessionDefaultChatChangedAction:
		return v.Type
	case *ahptypes.SessionTitleChangedAction:
		return v.Type
	case *ahptypes.SessionChatAddedAction:
		return v.Type
	case *ahptypes.ChatTurnStartedAction:
		return v.Type
	case *ahptypes.ChatResponsePartAction:
		return v.Type
	case *ahptypes.ChatDeltaAction:
		return v.Type
	case *ahptypes.ChatToolCallStartAction:
		return v.Type
	case *ahptypes.ChatToolCallReadyAction:
		return v.Type
	case *ahptypes.ChatToolCallCompleteAction:
		return v.Type
	case *ahptypes.ChatTurnCompleteAction:
		return v.Type
	case *ahptypes.ChatActivityChangedAction:
		return v.Type
	case *ahptypes.ChatToolCallConfirmedAction:
		return v.Type
	case *ahptypes.ChatTurnCancelledAction:
		return v.Type
	case *ahptypes.StateActionUnknown:
		return ""
	default:
		return ""
	}
}
