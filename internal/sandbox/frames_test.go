package sandbox

import (
	"encoding/base64"
	"strings"
	"testing"
)

func frameLine(stream int, data string) string {
	return `{"s":` + string(rune('0'+stream)) + `,"d":"` + base64.StdEncoding.EncodeToString([]byte(data)) + `"}` + "\n"
}

func TestReadFrames(t *testing.T) {
	var streamed []string
	in := frameLine(1, "hello ") + frameLine(2, "warn") + "\n" + frameLine(1, "world") +
		`{"exit":3,"ms":41,"truncated":true,"timed_out":false,"killed":true}` + "\n"
	res, err := readFrames(strings.NewReader(in), 1<<20, func(s int, b []byte) { streamed = append(streamed, string(b)) })
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "hello world" || string(res.Stderr) != "warn" || res.Exit != 3 || res.Duration.Milliseconds() != 41 ||
		!res.Truncated || !res.Killed || res.TimedOut || len(streamed) != 3 {
		t.Fatalf("result = %+v streamed %q", res, streamed)
	}
	// The client caps what it keeps even if the server sends more.
	res, err = readFrames(strings.NewReader(frameLine(1, "0123456789")+`{"exit":0}`+"\n"), 4, nil)
	if err != nil || string(res.Stdout) != "0123" || !res.Truncated {
		t.Fatalf("capped = %+v, %v", res, err)
	}
	for name, in := range map[string]string{
		"no final frame": frameLine(1, "x"),
		"unknown field":  `{"s":1,"d":"","evil":true}` + "\n",
		"bad stream":     `{"s":3,"d":"eA=="}` + "\n",
		"bad base64":     `{"s":1,"d":"!!"}` + "\n",
		"error frame":    `{"error":"exec create: no such container"}` + "\n",
		"inspect error":  `{"exit":-1,"error":"exec inspect: gone"}` + "\n",
		"not json":       "garbage\n",
		"huge line":      `{"s":1,"d":"` + strings.Repeat("A", maxFrameLine) + `"}` + "\n",
	} {
		if _, err := readFrames(strings.NewReader(in), 1<<20, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func FuzzReadFrames(f *testing.F) {
	f.Add(frameLine(1, "hi") + `{"exit":0,"ms":1}` + "\n")
	f.Add(`{"exit":124,"timed_out":true}`)
	f.Add(`{"s":2,"d":"` + strings.Repeat("QUFB", 100) + `"}` + "\n" + `{"error":"x"}`)
	f.Fuzz(func(t *testing.T, in string) {
		res, _ := readFrames(strings.NewReader(in), 64, func(s int, b []byte) {
			if s != 1 && s != 2 {
				t.Fatalf("stream %d delivered", s)
			}
		})
		if res == nil {
			t.Fatal("nil result")
		}
		if len(res.Stdout) > 64 || len(res.Stderr) > 64 {
			t.Fatalf("kept %d/%d bytes over the cap", len(res.Stdout), len(res.Stderr))
		}
	})
}
