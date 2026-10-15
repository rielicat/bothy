package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// phrase is how an action reads in plain words, after the UI's phrase table
// (docs/design/ui.md §7.4): ask completes "Asked to …", ing completes
// "Approved …", done and failed stand alone.
type phrase struct {
	ask, ing, done, failed string
}

// phraseCommandMax keeps a summary to one line; Raw record has the rest.
const phraseCommandMax = 60

// actionPhrase phrases a tool call from its system-built Action when there is
// one, else from the tool name alone ("delete a file").
func actionPhrase(tool string, a *core.Action) phrase {
	if a != nil && a.Tool != "" {
		tool = a.Tool
	}
	var payload map[string]any
	if a != nil && len(a.Payload) > 0 {
		_ = json.Unmarshal(a.Payload, &payload)
	}
	str := func(k string) string {
		v, _ := payload[k].(string)
		return strings.TrimSpace(v)
	}
	num := func(k string) string {
		if v, ok := payload[k].(float64); ok && v > 0 {
			return fmt.Sprintf("#%d", int64(v))
		}
		return ""
	}
	path := ""
	if a != nil && len(a.Paths) > 0 {
		path = a.Paths[0]
	}
	path = cmpOr(path, str("path"))
	host := ""
	if a != nil {
		host = a.Host
	}
	repo := str("repo")
	verbs := func(ask, ing, done, failed string) phrase {
		return phrase{ask: ask, ing: ing, done: done, failed: failed}
	}
	switch {
	case tool == "fs_read":
		p := cmpOr(path, "a file")
		return verbs("read "+p, "reading "+p, "Read "+p, "Couldn't read "+p)
	case tool == "fs_list":
		d := cmpOr(path, "Bothy's files")
		return verbs("look in "+d, "looking in "+d, "Looked in "+d, "Couldn't open "+d)
	case tool == "fs_write":
		p := cmpOr(path, "a file")
		switch str("mode") {
		case "create":
			return verbs("create "+p, "creating "+p, "Created "+p, "Couldn't save "+p)
		case "append":
			return verbs("add to "+p, "adding to "+p, "Added to "+p, "Couldn't save "+p)
		}
		return verbs("change "+p, "changing "+p, "Changed "+p, "Couldn't save "+p)
	case tool == "fs_delete":
		p := cmpOr(path, "a file")
		return verbs("delete "+p, "deleting "+p, "Deleted "+p, "Couldn't delete "+p)
	case tool == "shell":
		c := oneLine(commandOf(a, str("command")), phraseCommandMax)
		if c == "" {
			return verbs("run a command", "running a command", "Ran a command", "A command finished with errors")
		}
		return verbs("run "+c, "running "+c, "Ran "+c, c+" finished with errors")
	case tool == "python":
		return verbs("run a Python script", "running a Python script", "Ran a Python script", "The script stopped with an error")
	case tool == "web_search":
		return verbs("search the web", "searching the web", "Searched the web", "Web search didn't work")
	case tool == "web_fetch":
		h := cmpOr(host, "a website")
		if a != nil && a.Method != "" && !strings.EqualFold(a.Method, "GET") {
			return verbs("send data to "+h, "sending data to "+h, "Sent data to "+h, "Couldn't reach "+h)
		}
		return verbs("read "+h, "reading "+h, "Read "+h, "Couldn't open "+h)
	case tool == "net_allow" || a != nil && a.Kind == core.ActNet:
		h := cmpOr(host, "a website")
		return verbs("reach "+h, "letting Bothy reach "+h, "Can now reach "+h, "Couldn't reach "+h)
	case tool == "memory" && str("op") == "replace":
		return verbs("update a saved note", "updating a saved note", "Updated a saved note", "Couldn't update that note")
	case tool == "memory" && str("op") == "remove":
		return verbs("forget something", "forgetting something", "Forgot something", "Couldn't remove that note")
	case tool == "memory":
		return verbs("remember something", "remembering something", "Remembered something", "Couldn't save that")
	case tool == "session_search":
		return verbs("search past chats", "searching past chats", "Searched past chats", "Couldn't search past chats")
	case tool == "message":
		return verbs("send a message", "sending a message", "Sent a message", "Couldn't send the message")
	case tool == "skill_load" || tool == "skill_read":
		s := quoted(str("name"), "a skill")
		return verbs("follow "+s, "following "+s, "Followed "+s, "Couldn't follow "+s)
	case tool == "schedule_list":
		return verbs("check the schedules", "checking the schedules", "Checked the schedules", "Couldn't check the schedules")
	case tool == "schedule_create":
		s := quoted(str("name"), "a schedule")
		return verbs("create the schedule "+s, "creating the schedule "+s, "Created the schedule "+s, "Couldn't create the schedule")
	case tool == "schedule_delete":
		return verbs("delete a schedule", "deleting a schedule", "Deleted a schedule", "Couldn't delete the schedule")
	case tool == "repo_list":
		return verbs("check the connected repositories", "checking repositories", "Checked the connected repositories", "Couldn't check the repositories")
	case tool == "repo_checkout":
		r := cmpOr(repo, "a repository")
		return verbs("get a copy of "+r, "getting a copy of "+r, "Got a copy of "+r, "Couldn't get "+r)
	case tool == "repo_changes":
		r := cmpOr(repo, "a repository")
		return verbs("review changes in "+r, "reviewing changes in "+r, "Reviewed changes in "+r, "Couldn't review changes in "+r)
	case tool == "repo_open_pr":
		r := cmpOr(repo, "a repository")
		return verbs("open a pull request in "+r, "opening a pull request in "+r, "Opened a pull request in "+r, "Couldn't open the pull request")
	case tool == "github_issues":
		r := cmpOr(repo, "a repository")
		return verbs("read issues in "+r, "reading issues in "+r, "Read issues in "+r, "Couldn't read issues in "+r)
	case tool == "github_pr" && num("number") == "":
		in := " in " + cmpOr(repo, "a repository")
		return verbs("list pull requests"+in, "listing pull requests"+in, "Listed pull requests"+in, "Couldn't list pull requests"+in)
	case tool == "github_pr":
		pr := "pull request " + num("number")
		return verbs("read "+pr, "reading "+pr, "Read "+pr, "Couldn't read "+pr)
	case tool == "github_comment":
		on := strings.TrimSpace(num("number") + " in " + cmpOr(repo, "a repository"))
		return verbs("comment on "+on, "commenting on "+on, "Commented on "+on, "Couldn't post the comment")
	case tool == "github_runs":
		r := cmpOr(repo, "a repository")
		return verbs("check builds in "+r, "checking builds in "+r, "Checked recent builds in "+r, "Couldn't check builds in "+r)
	case tool == "release_deploy":
		env := cmpOr(str("environment"), "an environment")
		what := env
		if repo != "" {
			what = repo + " to " + env
		}
		// The tool only starts the workflow; release.finish says how it ended.
		return verbs("deploy "+what, "deploying "+what, "Started deploying "+what, "Deploy to "+env+" failed")
	case tool == "release_status":
		return verbs("check the deploys", "checking the deploys", "Checked the deploys", "Couldn't check the deploys")
	case tool == "knowledge_search":
		return verbs("search project knowledge", "searching project knowledge", "Searched project knowledge", "Couldn't search project knowledge")
	case tool == "notes":
		r := cmpOr(repo, "a repository")
		return verbs("update the notes for "+r, "updating the notes for "+r, "Updated the notes for "+r, "Couldn't update the notes for "+r)
	case strings.HasPrefix(tool, "mcp__"):
		server, t, _ := strings.Cut(strings.TrimPrefix(tool, "mcp__"), "__")
		u := server + ": " + humanize(t)
		return verbs("use "+u, "using "+u, "Used "+u, u+" failed")
	case tool == "":
		return phrase{}
	}
	return verbs("use "+tool, "using "+tool, "Used "+tool, tool+" failed")
}

// commandOf is the shell command line of an exec action.
func commandOf(a *core.Action, fallback string) string {
	if a != nil && len(a.Argv) == 3 && a.Argv[0] == "bash" && a.Argv[1] == "-lc" {
		return a.Argv[2]
	}
	if a != nil && len(a.Argv) > 0 {
		return strings.Join(a.Argv, " ")
	}
	return fallback
}

// oneLine collapses s to one line of at most limit characters.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > limit {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:limit-1])) + "…"
	}
	return s
}

// quoted puts curly quotes around a name someone chose.
func quoted(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return "“" + oneLine(name, phraseCommandMax) + "”"
}

// humanize turns an identifier into words: "create_issue" → "Create issue".
func humanize(id string) string {
	s := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(id))
	if s == "" {
		return id
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}
