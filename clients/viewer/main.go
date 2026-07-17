// Command viewer is a passive AHP client that observes another client's
// work in real time. It connects to the host, subscribes to the root
// channel, and waits. When any client creates a session, the host
// broadcasts `root/sessionAdded`; the viewer then subscribes to that
// session — and to each chat the session announces — and prints every
// synchronized state change as it arrives.
//
// The viewer never creates or mutates anything. Everything it prints is
// state the host fanned out to it, demonstrating multi-client sync.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahpws"
)

func main() {
	url := envOr("AHP_HOST_URL", "ws://127.0.0.1:12345")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	transport, err := ahpws.Connect(ctx, url)
	if err != nil {
		log.Fatalf("viewer: connect: %v", err)
	}
	client, err := ahp.Connect(ctx, transport, ahp.DefaultConfig())
	if err != nil {
		log.Fatalf("viewer: client: %v", err)
	}
	defer client.Shutdown(context.Background())

	// Subscribe to the root channel during the handshake so we hear
	// about sessions the instant any client creates one.
	init, err := client.Initialize(ctx, "viewer-client",
		ahptypes.SupportedProtocolVersions(), []string{ahptypes.RootResourceURI})
	if err != nil {
		log.Fatalf("viewer: initialize: %v", err)
	}
	logf("connected; negotiated protocol %s, serverSeq=%d", init.ProtocolVersion, init.ServerSeq)

	root := client.AttachSubscription(ahptypes.RootResourceURI)
	defer root.Close()
	logf("subscribed to %s — waiting for a session to appear", ahptypes.RootResourceURI)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-root.Events():
			if !ok {
				return
			}
			if added, isAdd := ev.(ahp.SubscriptionEventSessionAdded); isAdd {
				sessionURI := added.Params.Summary.Resource
				logf("root/sessionAdded → session %q (title %q, provider %q)",
					sessionURI, added.Params.Summary.Title, added.Params.Summary.Provider)
				go watchSession(ctx, client, sessionURI)
			}
		}
	}
}

// watchSession subscribes to a freshly announced session, prints its
// snapshot, and follows the session's action stream. When the session
// announces a chat (`session/chatAdded`), it also subscribes to that
// chat and mirrors every turn/tool-call/delta.
func watchSession(ctx context.Context, client *ahp.Client, sessionURI string) {
	res, sub, err := client.Subscribe(ctx, sessionURI)
	if err != nil {
		logf("subscribe %s: %v", sessionURI, err)
		return
	}
	defer sub.Close()
	if res.Snapshot != nil && res.Snapshot.State.Session != nil {
		logf("session snapshot %s: lifecycle=%s chats=%d",
			sessionURI, res.Snapshot.State.Session.Lifecycle, len(res.Snapshot.State.Session.Chats))
	}

	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			act, isAct := ev.(ahp.SubscriptionEventAction)
			if !isAct {
				continue
			}
			env := act.Envelope
			describe("session", env)
			if added, ok := env.Action.Value.(*ahptypes.SessionChatAddedAction); ok {
				chatURI := added.Summary.Resource
				if !seen[chatURI] {
					seen[chatURI] = true
					go watchChat(ctx, client, chatURI)
				}
			}
		}
	}
}

// watchChat subscribes to a chat channel and prints every synchronized
// action the agent dispatches into it.
func watchChat(ctx context.Context, client *ahp.Client, chatURI string) {
	res, sub, err := client.Subscribe(ctx, chatURI)
	if err != nil {
		logf("subscribe %s: %v", chatURI, err)
		return
	}
	defer sub.Close()
	if res.Snapshot != nil && res.Snapshot.State.Chat != nil {
		logf("chat snapshot %s: title=%q", chatURI, res.Snapshot.State.Chat.Title)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			if act, isAct := ev.(ahp.SubscriptionEventAction); isAct {
				describe("chat", act.Envelope)
			}
		}
	}
}

// describe renders one synchronized action envelope for the console.
func describe(scope string, env ahptypes.ActionEnvelope) {
	typ := actionType(env.Action)
	var b strings.Builder
	fmt.Fprintf(&b, "seq=%-3d %-6s %-24s", env.ServerSeq, scope, typ)
	switch v := env.Action.Value.(type) {
	case *ahptypes.SessionReadyAction:
		b.WriteString(" session is ready")
	case *ahptypes.SessionChatAddedAction:
		fmt.Fprintf(&b, " chat=%s", v.Summary.Resource)
	case *ahptypes.ChatTurnStartedAction:
		fmt.Fprintf(&b, " turn=%s prompt=%q", v.TurnId, v.Message.Text)
	case *ahptypes.ChatActivityChangedAction:
		if v.Activity != nil {
			fmt.Fprintf(&b, " activity=%q", *v.Activity)
		} else {
			b.WriteString(" activity cleared")
		}
	case *ahptypes.ChatResponsePartAction:
		fmt.Fprintf(&b, " turn=%s part=%s", v.TurnId, responsePartText(v.Part))
	case *ahptypes.ChatDeltaAction:
		fmt.Fprintf(&b, " turn=%s +%q", v.TurnId, v.Content)
	case *ahptypes.ChatToolCallStartAction:
		fmt.Fprintf(&b, " tool=%q id=%s", v.DisplayName, v.ToolCallId)
	case *ahptypes.ChatToolCallReadyAction:
		fmt.Fprintf(&b, " tool=%s invoking: %s", v.ToolCallId, v.InvocationMessage.AsText())
	case *ahptypes.ChatToolCallCompleteAction:
		fmt.Fprintf(&b, " tool=%s success=%v", v.ToolCallId, v.Result.Success)
	case *ahptypes.ChatTurnCompleteAction:
		fmt.Fprintf(&b, " turn=%s complete", v.TurnId)
	}
	if env.RejectionReason != nil {
		fmt.Fprintf(&b, "  [REJECTED: %s]", *env.RejectionReason)
	}
	logf("%s", b.String())
}

func responsePartText(part ahptypes.ResponsePart) string {
	if md, ok := part.Value.(*ahptypes.MarkdownResponsePart); ok {
		return fmt.Sprintf("markdown#%s %q", md.Id, md.Content)
	}
	return "<part>"
}

func actionType(a ahptypes.StateAction) string {
	switch v := a.Value.(type) {
	case *ahptypes.SessionReadyAction:
		return string(v.Type)
	case *ahptypes.SessionChatAddedAction:
		return string(v.Type)
	case *ahptypes.ChatTurnStartedAction:
		return string(v.Type)
	case *ahptypes.ChatActivityChangedAction:
		return string(v.Type)
	case *ahptypes.ChatResponsePartAction:
		return string(v.Type)
	case *ahptypes.ChatDeltaAction:
		return string(v.Type)
	case *ahptypes.ChatToolCallStartAction:
		return string(v.Type)
	case *ahptypes.ChatToolCallReadyAction:
		return string(v.Type)
	case *ahptypes.ChatToolCallCompleteAction:
		return string(v.Type)
	case *ahptypes.ChatTurnCompleteAction:
		return string(v.Type)
	}
	return "action"
}

func logf(format string, args ...any) {
	fmt.Printf("[viewer %s] "+format+"\n",
		append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
