package core

import (
	"maps"
	"os"
	"regexp"
	"testing"
)

func TestServiceNames(t *testing.T) {
	for id, want := range map[string]string{"deepwiki": "DeepWiki", "GitHub": "GitHub", "team-docs": "Team docs", "linear": "Linear", "my_wiki": "My wiki"} {
		if got := ServiceName(id); got != want {
			t.Errorf("ServiceName(%q) = %q, want %q", id, got, want)
		}
	}
	// Cards are worded on the server and in the browser; both spell services alike.
	src, err := os.ReadFile("../../web/src/lib/format.ts")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const serviceNames: Record<string, string> = \{(.*?)\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("serviceNames not found in web/src/lib/format.ts")
	}
	web := map[string]string{}
	for _, m := range regexp.MustCompile(`'?([a-z0-9-]+)'?: '([^']+)'`).FindAllSubmatch(block[1], -1) {
		web[string(m[1])] = string(m[2])
	}
	if !maps.Equal(web, serviceNames) {
		t.Fatalf("web/src/lib/format.ts serviceNames = %v, want %v", web, serviceNames)
	}
}
