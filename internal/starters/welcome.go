package starters

import "strings"

// Sender marks the welcome message: the web shows it as Bothy's own
// message, not as a notice.
const Sender = "bothy:welcome"

// WelcomeTitle names the welcome chat until its first message names it. A
// title is plain text, so the name is kept as written.
func WelcomeTitle(agent string) string { return "Welcome to " + agentName(agent, oneLine) }

// Welcome is the welcome chat's one message, a static notice that never
// reaches the model. Like the hello in a chat app it speaks as Bothy, the
// one place outside model output where Bothy says "I". displayName is the
// viewer's; its first word greets them.
func Welcome(displayName, agent, repo string) string {
	hi := "Hi"
	if f := strings.Fields(plain(displayName)); len(f) > 0 {
		hi += " " + f[0]
	}
	code := "work through lists of tasks"
	if r := plain(repo); r != "" {
		code = "read your code in " + r
	}
	return hi + ", I'm " + agentName(agent, plain) + ". I work for your team: I can research questions, draft documents, " + code +
		" and keep an eye on things on a schedule.\n\n" +
		"Before I do anything you'd want to check, like changing a file or opening a pull request, I ask first and show " +
		"exactly what I want to do.\n\n" +
		"- **Approvals** shows what's waiting for a decision.\n" +
		"- **Knowledge** holds your projects, what I remember and the skills your team has taught me.\n" +
		"- **Activity** shows what I've worked on, your schedules and what it cost.\n\n" +
		"Ask me anything to get started."
}

func agentName(agent string, clean func(string) string) string {
	if a := clean(agent); a != "" {
		return a
	}
	return "Bothy"
}
