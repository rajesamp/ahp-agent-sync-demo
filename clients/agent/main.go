// Command agent is an AHP client that simulates an AI agent doing work.
// It connects to the host, creates a session and a chat, then dispatches
// a scripted "turn" — assistant text streamed as deltas, a tool call
// that starts/runs/completes, and a closing summary — spread over a few
// seconds so a separately-connected viewer can watch the state
// synchronize live.
//
// Every mutation is a write-ahead `dispatchAction`: the agent would
// normally apply it to its own local state optimistically, and the host
// echoes it back (sequenced) to all subscribers, including this agent.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahpws"
)

func main() {
	url := envOr("AHP_HOST_URL", "ws://127.0.0.1:12345")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transport, err := ahpws.Connect(ctx, url)
	if err != nil {
		log.Fatalf("agent: connect: %v", err)
	}
	client, err := ahp.Connect(ctx, transport, ahp.DefaultConfig())
	if err != nil {
		log.Fatalf("agent: client: %v", err)
	}
	defer client.Shutdown(context.Background())

	init, err := client.Initialize(ctx, "agent-client", ahptypes.SupportedProtocolVersions(), nil)
	if err != nil {
		log.Fatalf("agent: initialize: %v", err)
	}
	logf("connected; negotiated protocol %s", init.ProtocolVersion)

	sessionURI := "ahp-session:/" + uuid()
	chatURI := "ahp-chat:/" + uuid()

	// 1. Create the session. The host broadcasts root/sessionAdded to
	//    every root subscriber (the viewer picks it up here).
	if err := client.Request(ctx, "createSession",
		ahptypes.CreateSessionParams{Channel: sessionURI, Provider: strptr("demo")}, &struct{}{}); err != nil {
		log.Fatalf("agent: createSession: %v", err)
	}
	logf("created session %s", sessionURI)

	// Subscribe to our own session so the host will sequence and echo
	// the chat we are about to add (and so we observe our own writes).
	if _, _, err := client.Subscribe(ctx, sessionURI); err != nil {
		log.Fatalf("agent: subscribe session: %v", err)
	}

	// 2. Create a chat within the session. The host announces it on the
	//    session channel as session/chatAdded.
	if err := client.Request(ctx, "createChat",
		ahptypes.CreateChatParams{Channel: sessionURI, Chat: chatURI}, &struct{}{}); err != nil {
		log.Fatalf("agent: createChat: %v", err)
	}
	logf("created chat %s", chatURI)
	if _, _, err := client.Subscribe(ctx, chatURI); err != nil {
		log.Fatalf("agent: subscribe chat: %v", err)
	}

	// Give the viewer a beat to receive sessionAdded and subscribe to
	// the chat channel before we start streaming turn actions.
	time.Sleep(750 * time.Millisecond)

	runScriptedTurn(ctx, client, chatURI)
	logf("turn finished; agent exiting")
	// Let the final frames flush to subscribers.
	time.Sleep(300 * time.Millisecond)
}

// runScriptedTurn dispatches a believable agent turn as a sequence of
// write-ahead actions, pausing between them so the synchronization is
// visible in real time.
func runScriptedTurn(ctx context.Context, client *ahp.Client, chat string) {
	turnID := "turn-" + uuid()
	partID := "part-" + uuid()
	toolID := "tool-" + uuid()

	dispatch := func(v ahptypes.StateAction) {
		if _, err := client.Dispatch(ctx, chat, v); err != nil {
			log.Printf("agent: dispatch: %v", err)
		}
	}
	step := func(d time.Duration) { time.Sleep(d) }

	// Turn begins with the user's prompt.
	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatTurnStartedAction{
		Type:   ahptypes.ActionTypeChatTurnStarted,
		TurnId: turnID,
		Message: ahptypes.Message{
			Text:   "What is the current disk usage on the build server?",
			Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKindUser},
		},
	}})
	logf("dispatched chat/turnStarted (%s)", turnID)
	step(500 * time.Millisecond)

	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatActivityChangedAction{
		Type: ahptypes.ActionTypeChatActivityChanged, Activity: strptr("thinking"),
	}})
	step(600 * time.Millisecond)

	// Open a markdown response part, then stream tokens into it.
	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatResponsePartAction{
		Type: ahptypes.ActionTypeChatResponsePart, TurnId: turnID,
		Part: ahptypes.ResponsePart{Value: &ahptypes.MarkdownResponsePart{
			Kind: ahptypes.ResponsePartKindMarkdown, Id: partID, Content: "",
		}},
	}})
	for _, tok := range []string{"Let me ", "check the ", "disk usage ", "for you."} {
		dispatch(ahptypes.StateAction{Value: &ahptypes.ChatDeltaAction{
			Type: ahptypes.ActionTypeChatDelta, TurnId: turnID, PartId: partID, Content: tok,
		}})
		logf("dispatched chat/delta %q", tok)
		step(350 * time.Millisecond)
	}

	// Tool call: start → ready(running) → complete.
	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallStartAction{
		Type: ahptypes.ActionTypeChatToolCallStart, TurnId: turnID, ToolCallId: toolID,
		ToolName: "run_shell", DisplayName: "Run shell command",
		Intention: strptr("Inspect disk usage"),
	}})
	logf("dispatched chat/toolCallStart (%s)", toolID)
	step(500 * time.Millisecond)

	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallReadyAction{
		Type: ahptypes.ActionTypeChatToolCallReady, TurnId: turnID, ToolCallId: toolID,
		InvocationMessage: ahptypes.NewStringOrMarkdownPlain("df -h /"),
		ToolInput:         strptr(`{"command":"df -h /"}`),
	}})
	step(900 * time.Millisecond)

	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallCompleteAction{
		Type: ahptypes.ActionTypeChatToolCallComplete, TurnId: turnID, ToolCallId: toolID,
		Result: ahptypes.ToolCallResult{
			Success:          true,
			PastTenseMessage: ahptypes.NewStringOrMarkdownPlain("Ran df -h /"),
		},
	}})
	logf("dispatched chat/toolCallComplete (%s)", toolID)
	step(500 * time.Millisecond)

	// Stream the concluding answer.
	summaryPart := "part-" + uuid()
	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatResponsePartAction{
		Type: ahptypes.ActionTypeChatResponsePart, TurnId: turnID,
		Part: ahptypes.ResponsePart{Value: &ahptypes.MarkdownResponsePart{
			Kind: ahptypes.ResponsePartKindMarkdown, Id: summaryPart, Content: "",
		}},
	}})
	for _, tok := range []string{"The root ", "filesystem is ", "at 63% ", "(28G free)."} {
		dispatch(ahptypes.StateAction{Value: &ahptypes.ChatDeltaAction{
			Type: ahptypes.ActionTypeChatDelta, TurnId: turnID, PartId: summaryPart, Content: tok,
		}})
		step(350 * time.Millisecond)
	}

	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatActivityChangedAction{Type: ahptypes.ActionTypeChatActivityChanged}})
	dispatch(ahptypes.StateAction{Value: &ahptypes.ChatTurnCompleteAction{
		Type: ahptypes.ActionTypeChatTurnComplete, TurnId: turnID,
	}})
	logf("dispatched chat/turnComplete (%s)", turnID)
}

// uuid returns a random RFC-4122-ish v4 identifier.
func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func strptr(s string) *string { return &s }

func logf(format string, args ...any) {
	fmt.Printf("[agent  %s] "+format+"\n",
		append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
