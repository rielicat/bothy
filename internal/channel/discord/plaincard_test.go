package discord

import (
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/rielicat/bothy/internal/core"
)

// TestRenderPlainCard: an approval card in the hub's plain copy keeps its
// words on Discord, and its primary button is red only when the hub says the
// action can't be undone.
func TestRenderPlainCard(t *testing.T) {
	card := "**Delete `notes/old.md`?**\nThis can't be undone.\n⚠ Bothy hasn't sent anything to `bob@example.com` before.\n" +
		"**File:** `notes/old.md`"
	if got := render(card); got != card {
		t.Errorf("render =\n%q\nwant\n%q", got, card)
	}
	rows, err := components([][]core.Button{{{Label: "Delete file", Data: "a:x:o:m", Style: core.BtnDanger}, {Label: "Deny", Data: "a:x:d:m"}}})
	if err != nil {
		t.Fatal(err)
	}
	row, ok := rows[0].(discord.ActionRowComponent)
	if !ok || len(row.Components) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	del, deny := row.Components[0].(discord.ButtonComponent), row.Components[1].(discord.ButtonComponent)
	if del.Label != "Delete file" || del.Style != discord.ButtonStyleDanger || deny.Label != "Deny" || deny.Style != discord.ButtonStyleSecondary {
		t.Errorf("buttons = %+v %+v", del, deny)
	}
}
