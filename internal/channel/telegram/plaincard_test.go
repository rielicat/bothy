package telegram

import "testing"

// TestRenderPlainCard: an approval card in the hub's plain copy keeps its
// words on Telegram: the bold question with the file name in code (never a
// link), the consequence, the warning line, bold labels and the italic
// context line with its file name in code too.
func TestRenderPlainCard(t *testing.T) {
	card := "**Delete `notes/old.md`?**\nThis can't be undone.\n⚠ Bothy hasn't sent anything to `bob@example.com` before.\n" +
		"**File:** `notes/old.md`\n**Run this command?**\n_Telegram · DM · “Clean up `notes.md`”_"
	want := "<b>Delete </b><code>notes/old.md</code><b>?</b>\nThis can't be undone.\n⚠ Bothy hasn't sent anything to <code>bob@example.com</code> before.\n" +
		"<b>File:</b> <code>notes/old.md</code>\n<b>Run this command?</b>\n<i>Telegram · DM · “Clean up </i><code>notes.md</code><i>”</i>"
	if html, _ := render(card); html != want {
		t.Errorf("render =\n%q\nwant\n%q", html, want)
	}
}
