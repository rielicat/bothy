// Package channel is the channel router (core.ChannelHub). Owner: M9.
//
// The hub owns the chat adapters: it runs the enabled ones (restarting them
// when their channels.* config changes), turns inbound messages into runs
// after dedup, identity, rate-limit, command and space checks, streams runs
// back to chats, and delivers and updates approval cards.
package channel
