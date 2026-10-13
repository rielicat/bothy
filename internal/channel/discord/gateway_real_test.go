//go:build !race

package discord

import "github.com/rielicat/bothy/internal/channel/discord/discordtest"

// realGateway: without the race detector the tests drive disgo's gateway
// over WebSocket against the fake.
const realGateway = true

func useGateway(*adapter, *discordtest.Server) {}
