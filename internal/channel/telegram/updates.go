package telegram

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/bothyhq/bothy/internal/core"
)

func chatKind(t string) (core.ChatKind, bool) {
	switch t {
	case "private":
		return core.ChatDM, true
	case "group", "supergroup":
		return core.ChatGroup, true
	}
	return "", false
}

// chatRef maps a Telegram chat (and, for forum topics, the message's topic)
// to a ChatRef; channels are not supported.
func chatRef(c tgChat, m *tgMessage) (core.ChatRef, bool) {
	kind, ok := chatKind(c.Type)
	if !ok {
		return core.ChatRef{}, false
	}
	ref := core.ChatRef{Channel: Name, ChatID: strconv.FormatInt(c.ID, 10), Kind: kind}
	if m != nil && m.IsTopicMessage && m.MessageThreadID != 0 {
		ref.ThreadID = strconv.Itoa(m.MessageThreadID)
	}
	return ref, true
}

// inbound normalizes a message. ok is false for updates Bothy ignores:
// channel posts, messages without a sender, and commands addressed to
// another bot in a group.
func inbound(m *tgMessage, me tgUser) (core.Inbound, bool) {
	if m == nil || m.From == nil {
		return core.Inbound{}, false
	}
	chat, ok := chatRef(m.Chat, m)
	if !ok {
		return core.Inbound{}, false
	}
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	text, mentioned, forOther := stripMentions(text, entities, me)
	if forOther {
		return core.Inbound{}, false
	}
	if chat.Kind == core.ChatDM {
		mentioned = true
	}
	if r := m.ReplyToMessage; r != nil && r.From != nil && r.From.ID == me.ID {
		mentioned = true
	}
	atts := attachments(m)
	// Stickers, polls, service messages and the like carry nothing for the
	// agent, and an empty group Inbound is the hub's "bot was added by"
	// signal, which only my_chat_member may produce.
	if text == "" && len(atts) == 0 {
		return core.Inbound{}, false
	}
	return core.Inbound{
		Chat:           chat,
		MsgID:          fmt.Sprintf("%d:%d", m.Chat.ID, m.MessageID),
		SenderID:       strconv.FormatInt(m.From.ID, 10),
		SenderName:     strings.TrimSpace(m.From.FirstName + " " + m.From.LastName),
		SenderUsername: m.From.Username,
		SenderIsBot:    m.From.IsBot,
		Text:           text,
		Mentioned:      mentioned,
		Attachments:    atts,
		At:             time.Unix(m.Date, 0),
	}, true
}

// stripMentions removes the bot's @mention (and the @bot suffix of a
// command) from text, reporting whether the bot was addressed and whether a
// leading command was addressed to a different bot.
func stripMentions(text string, entities []tgEntity, me tgUser) (out string, mentioned, forOther bool) {
	if len(entities) == 0 {
		return strings.TrimSpace(text), false, false
	}
	u := utf16.Encode([]rune(text))
	type span struct{ off, n int }
	var cuts []span
	for _, e := range entities {
		if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > len(u) {
			continue
		}
		seg := string(utf16.Decode(u[e.Offset : e.Offset+e.Length]))
		switch e.Type {
		case "mention":
			if me.Username != "" && strings.EqualFold(seg, "@"+me.Username) {
				mentioned = true
				cuts = append(cuts, span{e.Offset, e.Length})
			}
		case "text_mention":
			if e.User != nil && e.User.ID == me.ID {
				mentioned = true
				cuts = append(cuts, span{e.Offset, e.Length})
			}
		case "bot_command":
			if e.Offset != 0 {
				continue
			}
			cmd, at, hasAt := strings.Cut(seg, "@")
			switch {
			case !hasAt:
				mentioned = true
			case strings.EqualFold(at, me.Username):
				mentioned = true
				n := len(utf16.Encode([]rune(cmd)))
				cuts = append(cuts, span{e.Offset + n, e.Length - n})
			default:
				forOther = true
			}
		}
	}
	slices.SortFunc(cuts, func(a, b span) int { return b.off - a.off })
	for _, c := range cuts {
		end := c.off + c.n
		// Drop one space too when the mention sat between two words.
		if c.off > 0 && end < len(u) && u[c.off-1] == ' ' && u[end] == ' ' {
			end++
		}
		u = append(u[:c.off], u[end:]...)
	}
	return strings.TrimSpace(string(utf16.Decode(u))), mentioned, forOther
}

func attachments(m *tgMessage) []core.Attachment {
	var out []core.Attachment
	if len(m.Photo) > 0 {
		p := m.Photo[0]
		for _, s := range m.Photo[1:] {
			if s.Width*s.Height > p.Width*p.Height {
				p = s
			}
		}
		out = append(out, core.Attachment{Name: "photo_" + p.FileUniqueID + ".jpg", MIME: "image/jpeg", Size: p.FileSize, FetchRef: p.FileID})
	}
	add := func(f *tgFile, prefix, mime, ext string) {
		if f == nil || f.FileID == "" {
			return
		}
		name := f.FileName
		if name == "" {
			name = prefix + "_" + f.FileUniqueID + ext
		}
		if f.MimeType != "" {
			mime = f.MimeType
		}
		out = append(out, core.Attachment{Name: name, MIME: mime, Size: f.FileSize, FetchRef: f.FileID})
	}
	add(m.Document, "document", "application/octet-stream", "")
	add(m.Audio, "audio", "audio/mpeg", ".mp3")
	add(m.Voice, "voice", "audio/ogg", ".ogg")
	add(m.Video, "video", "video/mp4", ".mp4")
	return out
}

// callback normalizes a button press on one of the bot's messages.
func callback(q *tgCallbackQuery) (core.ActionEvent, bool) {
	if q == nil || q.Message == nil || q.ID == "" {
		return core.ActionEvent{}, false
	}
	chat, ok := chatRef(q.Message.Chat, q.Message)
	if !ok {
		return core.ActionEvent{}, false
	}
	return core.ActionEvent{
		Chat:     chat,
		Msg:      core.MsgRef{Chat: chat, ID: strconv.Itoa(q.Message.MessageID)},
		SenderID: strconv.FormatInt(q.From.ID, 10),
		Data:     q.Data,
		AckRef:   q.ID,
	}, true
}

// joined turns "the bot was added to a group" into the hub's presence
// signal: an Inbound with no text from the user who added the bot, which the
// hub uses only to record the group with that user as its owner.
func joined(u *tgChatMemberUpdated, updateID int64, me tgUser) (core.Inbound, bool) {
	if u == nil || u.NewChatMember.User.ID != me.ID {
		return core.Inbound{}, false
	}
	chat, ok := chatRef(u.Chat, nil)
	if !ok || chat.Kind != core.ChatGroup {
		return core.Inbound{}, false
	}
	was, is := u.OldChatMember.Status, u.NewChatMember.Status
	if (was != "left" && was != "kicked" && was != "") || (is != "member" && is != "administrator") {
		return core.Inbound{}, false
	}
	return core.Inbound{
		Chat:           chat,
		MsgID:          fmt.Sprintf("%d:joined:%d", u.Chat.ID, updateID),
		SenderID:       strconv.FormatInt(u.From.ID, 10),
		SenderName:     strings.TrimSpace(u.From.FirstName + " " + u.From.LastName),
		SenderUsername: u.From.Username,
		SenderIsBot:    u.From.IsBot,
		At:             time.Unix(u.Date, 0),
	}, true
}
