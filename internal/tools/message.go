package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	messageName    = "message"
	maxMessageText = 16000 // runes; the hub splits to each channel's limit
)

// message replies in the run's originating chat and nowhere else: it has no
// recipient parameter, so a prompt injection cannot pick a destination.
type message struct{ messenger core.Messenger }

type messageArgs struct {
	Text string `json:"text"`
}

func (message) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        messageName,
			Description: "Send a message to this chat now, e.g. a progress update. Your final answer is delivered anyway.",
			Schema:      objectSchema(`"text":{"type":"string","description":"Markdown text"}`, "text"),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Send message", SideEffect: core.SELocalWrite, Messaging: true,
			Core: true, Timeout: 30 * time.Second, MaxOutputKB: 4,
		},
	}
}

// Enabled implements core.Gated: only channel chats can receive it; web and
// API runs already get the reply in their own stream.
func (message) Enabled(env *core.ToolEnv) bool { return chatOf(env) != nil }

func chatOf(env *core.ToolEnv) *core.ChatRef {
	if env == nil || env.Chat == nil || env.Chat.ChatID == "" {
		return nil
	}
	switch env.Chat.Channel {
	case "", "web", "api":
		return nil
	}
	return env.Chat
}

func (message) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in messageArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	chat := chatOf(env)
	if chat == nil {
		return nil, fmt.Errorf("%w: this run has no chat to message", core.ErrInvalid)
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, fmt.Errorf("%w: text is empty", core.ErrInvalid)
	}
	if n := utf8.RuneCountInString(in.Text); n > maxMessageText {
		return nil, fmt.Errorf("%w: text is %d characters; the limit is %d", core.ErrInvalid, n, maxMessageText)
	}
	a, err := newAction(messageName, core.ActSend, core.SELocalWrite, in)
	if err != nil {
		return nil, err
	}
	a.Recipient = chat.Key()
	return a, nil
}

// Exec sends only to the chat the Action was planned for, which for a
// resumed run must still be the run's chat.
func (m message) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var in messageArgs
	if err := payloadOf(a, &in); err != nil {
		return nil, err
	}
	chat := chatOf(env)
	if chat == nil || chat.Key() != a.Recipient {
		return nil, errors.New("message: the planned chat is not this run's chat")
	}
	if m.messenger == nil {
		return nil, errors.New("message: no messenger configured")
	}
	if _, err := m.messenger.Send(ctx, *chat, in.Text); err != nil {
		return nil, fmt.Errorf("message: send: %w", err)
	}
	return &core.ToolResult{Content: "Message sent."}, nil
}
