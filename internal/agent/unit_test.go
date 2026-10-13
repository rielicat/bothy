package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestNeutralizeLinks(t *testing.T) {
	cases := map[string]string{
		"see https://evil.com/a?b=c.":           "see hxxps://evil[.]com/a?b=c.",
		"HTTP://Example.ORG":                    "hxxp://Example[.]ORG",
		"ftp://files.example.net/x":             "fxp://files[.]example[.]net/x",
		"visit www.example.com/path now":        "visit www[.]example[.]com/path now",
		"[docs](https://docs.example.com/page)": "[docs](hxxps://docs[.]example[.]com/page)",
		"no links here":                         "no links here",
		"hxxps://evil[.]com":                    "hxxps://evil[.]com",
	}
	for in, want := range cases {
		if got := neutralizeLinks(in); got != want {
			t.Errorf("neutralizeLinks(%q) = %q, want %q", in, got, want)
		}
		if got := neutralizeLinks(neutralizeLinks(in)); got != want {
			t.Errorf("not idempotent for %q: %q", in, got)
		}
	}
}

func TestExtractURLs(t *testing.T) {
	got := extractURLs("Read https://a.example/x, then (https://b.example/y). Also ftp://c and https://a.example/x again!")
	want := []string{"https://a.example/x", "https://b.example/y"}
	if !slices.Equal(got, want) {
		t.Fatalf("extractURLs = %v, want %v", got, want)
	}
}

func TestStandingInstructions(t *testing.T) {
	text := "Thanks! Don't send anything until I say so. Always reply in French.\nWhat time is it? " +
		"From now on use metric units. Stop adding emojis. Only book flights if they are direct. I like cats."
	got := standingInstructions(text)
	want := []string{
		"Don't send anything until I say so.",
		"Always reply in French.",
		"From now on use metric units.",
		"Stop adding emojis.",
		"Only book flights if they are direct.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("standingInstructions =\n%q\nwant\n%q", got, want)
	}
	if got := standingInstructions("Will you always be here?"); len(got) != 0 {
		t.Fatalf("questions are not instructions: %q", got)
	}
}

func TestWrapUntrustedEscapesAttributes(t *testing.T) {
	got := wrapUntrusted("abc", "body", "source", "web", "sender", "Eve\" id=\"x‮<>")
	want := "<untrusted source=\"web\" sender=\"Eve' id='x‹›\" id=\"abc\">\nbody\n</untrusted id=\"abc\">"
	if got != want {
		t.Fatalf("wrapUntrusted = %q\nwant %q", got, want)
	}
}

func TestHeadTail(t *testing.T) {
	s := strings.Repeat("é", 100) // 200 bytes
	out := headTail(s, 50, "[cut]")
	if !strings.Contains(out, "\n[cut]\n") || !utf8Valid(out) || len(out) > 50+len("\n[cut]\n") {
		t.Fatalf("headTail = %q", out)
	}
	if headTail("short", 50, "x") != "short" {
		t.Fatal("short input changed")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestScaleBreakdownSumsExactly(t *testing.T) {
	bd := core.Breakdown{Constitution: 333, Persona: 333, Memory: 1, Tools: 333, History: 7, TurnContext: 3, Cacheable: 1000}
	for _, reported := range []int{1, 999, 1010, 4096, 123457} {
		got := scaleBreakdown(bd, reported)
		sum := got.Constitution + got.Persona + got.Memory + got.Skills + got.Pins + got.Tools + got.Summary + got.History + got.ToolResults + got.TurnContext
		if sum != reported || got.Total != reported {
			t.Fatalf("reported %d: sum %d total %d", reported, sum, got.Total)
		}
	}
	if got := scaleBreakdown(bd, 0); got.Total != 1010 {
		t.Fatalf("unreported input keeps the estimate: %d", got.Total)
	}
}

func TestSemaphoreFIFOAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limit := 1
		s := newSemaphore(func() int { return limit })
		ctx := context.Background()
		if err := s.acquire(ctx, nil); err != nil {
			t.Fatal(err)
		}
		var order []int
		positions := make(chan int, 3)
		cctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 3)
		go func() { done <- s.acquire(cctx, func(p int) { positions <- p }) }() // will give up
		synctest.Wait()
		for i := 1; i <= 2; i++ {
			go func() {
				if err := s.acquire(ctx, func(p int) { positions <- p }); err == nil {
					order = append(order, i)
					s.release()
				}
				done <- nil
			}()
			synctest.Wait()
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter = %v", err)
		}
		s.release()
		<-done
		<-done
		if !slices.Equal(order, []int{1, 2}) {
			t.Fatalf("order = %v", order)
		}
		if got := []int{<-positions, <-positions, <-positions}; !slices.Equal(got, []int{1, 2, 3}) {
			t.Fatalf("positions = %v", got)
		}
		limit = 2 // hot reload widens the semaphore
		if err := s.acquire(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.acquire(ctx, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProfilesAndReserve(t *testing.T) {
	c := config.Default()
	cases := []struct {
		window  int
		p       profile
		reserve int
		trigger float64
		output  int
	}{
		{200_000, profileStandard, 8192, 0.70, 4000},
		{32_768, profileStandard, 3932, 0.70, 3276},
		{20_000, profileCompact, 2048, 0.60, 1000},
		{16_384, profileCompact, 2048, 0.60, 1000},
		{10_000, profileDegraded, 1024, 0.55, 500},
		{4_096, profileTextOnly, 1024, 0.55, 500},
	}
	for _, tc := range cases {
		p := profileFor(c, tc.window)
		if p != tc.p || reserveFor(c, p, tc.window) != tc.reserve || triggerFor(c, p) != tc.trigger || toolOutputCap(c, p, tc.window) != tc.output {
			t.Errorf("window %d: %s reserve %d trigger %v output %d", tc.window, p, reserveFor(c, p, tc.window), triggerFor(c, p), toolOutputCap(c, p, tc.window))
		}
	}
	c.Context.ReserveOutput = "3000"
	if got := reserveFor(c, profileCompact, 20_000); got != 3000 {
		t.Fatalf("fixed reserve = %d", got)
	}
	if got := reserveFor(c, profileCompact, 4000); got != 2000 {
		t.Fatalf("reserve is capped at half the window: %d", got)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"\"Weekend plans.\"":        "Weekend plans",
		"# **Trip to Rome**\nextra": "Trip to Rome",
		"  spaced   out   title  ":  "spaced out title",
		strings.Repeat("long ", 40): strings.Repeat("long ", 16)[:79] + "…",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{24 * time.Hour: "24 h", 90 * time.Minute: "90 min", 1500 * time.Millisecond: "1.5s"} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q", d, got)
		}
	}
}
