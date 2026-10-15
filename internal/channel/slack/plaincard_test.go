package slack

import (
	"testing"

	"github.com/slack-go/slack"

	"github.com/rielicat/bothy/internal/core"
)

// TestRenderPlainCard: an approval card in the hub's plain copy keeps its
// words on Slack, and its primary button is red only when the hub says the
// action can't be undone.
func TestRenderPlainCard(t *testing.T) {
	card := "**Delete `notes/old.md`?**\nThis can't be undone.\n⚠ Bothy hasn't sent anything to `bob@example.com` before.\n" +
		"**File:** `notes/old.md`"
	want := "*Delete `notes/old.md`?*\nThis can't be undone.\n⚠ Bothy hasn't sent anything to `bob@example.com` before.\n" +
		"*File:* `notes/old.md`"
	if got := render(card); got != want {
		t.Errorf("render =\n%q\nwant\n%q", got, want)
	}
	blocks, err := actionBlocks([][]core.Button{{{Label: "Delete file", Data: "a:x:o:m", Style: core.BtnDanger}, {Label: "Deny", Data: "a:x:d:m"}}})
	if err != nil {
		t.Fatal(err)
	}
	act, ok := blocks[0].(*slack.ActionBlock)
	if !ok || len(act.Elements.ElementSet) != 2 {
		t.Fatalf("blocks = %+v", blocks)
	}
	del, deny := act.Elements.ElementSet[0].(*slack.ButtonBlockElement), act.Elements.ElementSet[1].(*slack.ButtonBlockElement)
	if del.Text.Text != "Delete file" || del.Style != slack.StyleDanger || deny.Text.Text != "Deny" || deny.Style != slack.StyleDefault {
		t.Errorf("buttons = %+v %+v", del, deny)
	}
}
