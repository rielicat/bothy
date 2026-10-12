// Package channeltest provides a fake core.Channel that records sends/edits and
// lets tests inject inbound messages and button presses.
package channeltest

import "github.com/bothyhq/bothy/internal/core"

type Channel struct {
	core.Channel
	NameV  string
	CapsV  core.ChannelCaps
	Sent   []core.OutMessage
	Edited []core.OutMessage
	Acks   []string
}

// New returns a fake with Telegram-like caps unless overridden.
func New(name string) *Channel { return &Channel{NameV: name} }

// Inject delivers an inbound message to the sink passed to Run.
func (c *Channel) Inject(in core.Inbound) {}

// Press delivers a button press for a previously sent message.
func (c *Channel) Press(msg core.MsgRef, senderID, data string) {}
