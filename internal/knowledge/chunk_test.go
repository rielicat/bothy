package knowledge

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

type want struct {
	kind, title string
	start, end  int
}

// checkChunks compares kinds, titles (prefix) and line ranges, and that every
// body is exactly its lines of the source.
func checkChunks(t *testing.T, path, src string, wants []want) []core.KnowledgeChunk {
	t.Helper()
	_, got := chunkFile(path, []byte(src))
	lines := splitLines(src)
	for _, c := range got {
		if body := strings.Join(lines[c.StartLine-1:c.EndLine], "\n"); body != c.Body {
			t.Errorf("%s: chunk %d-%d body differs from its lines:\n%q\n%q", path, c.StartLine, c.EndLine, c.Body, body)
		}
	}
	if len(got) != len(wants) {
		for _, c := range got {
			t.Logf("  %s %q %d-%d", c.Kind, c.Title, c.StartLine, c.EndLine)
		}
		t.Fatalf("%s: %d chunks, want %d", path, len(got), len(wants))
	}
	for i, w := range wants {
		c := got[i]
		if c.Kind != w.kind || !strings.HasPrefix(c.Title, w.title) || c.StartLine != w.start || c.EndLine != w.end {
			t.Errorf("%s chunk %d = %s %q %d-%d, want %s %q %d-%d", path, i, c.Kind, c.Title, c.StartLine, c.EndLine,
				w.kind, w.title, w.start, w.end)
		}
	}
	return got
}

func TestChunkGo(t *testing.T) {
	src := `// Package limit throttles callers.
package limit

import "time"

// Bucket is a token bucket.
type Bucket struct {
	tokens int
	last   time.Time
}

// New returns a full bucket.
func New(n int) *Bucket {
	return &Bucket{tokens: n}
}

// Allow takes a token.
func (b *Bucket) Allow() bool {
	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}

const (
	A = 1
	B = 2
)
`
	got := checkChunks(t, "internal/limit/bucket.go", src, []want{
		{"header", "// Package limit", 1, 4},
		{"type", "type Bucket struct", 6, 10},
		{"func", "func New(n int) *Bucket", 12, 15},
		{"method", "func (b *Bucket) Allow() bool", 17, 24},
		{"var", "const (", 26, 29},
	})
	if got[0].Lang != "go" {
		t.Errorf("lang = %q", got[0].Lang)
	}
	checkChunks(t, "ids.go", "package ids\n\ntype A string\ntype B string\n\nconst X = 1\nconst Y = 2\n\nfunc F() {\n\treturn\n}\n", []want{
		{"header", "package ids", 1, 1},
		{"type", "type A string", 3, 7},
		{"func", "func F()", 9, 11},
	})
}

func TestChunkPython(t *testing.T) {
	src := `"""Billing helpers."""
import os


@dataclass
class Invoice:
    total: int

    def pay(self, amount):
        self.total -= amount
        return self.total

    async def refund(self):
        await notify()
        return None


def main():
    print(Invoice(3))
    return 0
`
	checkChunks(t, "billing.py", src, []want{
		{"header", `"""Billing helpers."""`, 1, 2},
		{"class", "class Invoice", 5, 7},
		{"method", "Invoice: def pay(self, amount)", 9, 11},
		{"method", "Invoice: async def refund(self)", 13, 15},
		{"func", "def main()", 18, 20},
	})
}

func TestChunkTypeScript(t *testing.T) {
	src := `import { x } from "./x";

/** A user. */
export interface User {
  id: string;
}

export class Store {
  private items: User[] = [];

  async load(id: string): Promise<User> {
    if (id) {
      return this.items[0];
    }
    return fetchUser(id);
  }
}

export function makeStore(): Store {
  return new Store();
}

export const limiter = createLimiter({ rate: 10, burst: 5, window: 60 });
`
	checkChunks(t, "web/src/store.ts", src, []want{
		{"header", "import { x }", 1, 1},
		{"type", "export interface User", 3, 6},
		{"class", "export class Store", 8, 9},
		{"method", "Store: async load(id: string): Promise<User>", 11, 17},
		{"func", "export function makeStore(): Store", 19, 23},
	})
}

func TestChunkRustJavaCRubyPHPShell(t *testing.T) {
	checkChunks(t, "src/lib.rs", `use std::fmt;

/// A point.
#[derive(Debug)]
pub struct Point { x: i32 }

impl Point {
    pub fn new(x: i32) -> Self {
        Point { x }
    }
}

pub async fn serve() {
    loop {}
}
`, []want{
		{"header", "use std::fmt", 1, 1},
		{"type", "pub struct Point", 3, 5},
		{"impl", "impl Point", 7, 7},
		{"method", "Point: pub fn new(x: i32) -> Self", 8, 11},
		{"func", "pub async fn serve()", 13, 15},
	})
	checkChunks(t, "App.java", `package app;

@Service
public class App {
    private final Repo repo;

    @Override
    public String name(int id) {
        if (id > 0) {
            return "x";
        }
        return repo.find(id);
    }
}
`, []want{
		{"header", "package app", 1, 1},
		{"class", "public class App", 3, 5},
		{"method", "App: public String name(int id)", 7, 14},
	})
	checkChunks(t, "main.c", `#include <stdio.h>

struct node {
	int v;
};

/* Entry point. */
int main(int argc, char **argv)
{
	if (argc > 1) {
		return 1;
	}
	return 0;
}
`, []want{
		{"header", "#include <stdio.h>", 1, 1},
		{"type", "struct node", 3, 5},
		{"func", "int main(int argc, char **argv)", 7, 14},
	})
	checkChunks(t, "lib/user.rb", `require "json"

class User
  attr_reader :name

  def greet(other)
    puts "hi #{other}"
    other.name
  end
end
`, []want{
		{"header", `require "json"`, 1, 1},
		{"class", "class User", 3, 4},
		{"method", "User: def greet(other)", 6, 10},
	})
	checkChunks(t, "src/Repo.php", `<?php
namespace App;

final class Repo
{
    public function find(int $id): array
    {
        return $this->rows[$id] ?? [];
    }
}
`, []want{
		{"header", "<?php", 1, 2},
		{"class", "final class Repo", 4, 5},
		{"method", "Repo: public function find(int $id): array", 6, 10},
	})
	checkChunks(t, "scripts/deploy.sh", `#!/bin/sh
set -eu

# Builds the image.
build() {
	docker build -t app .
	echo done
}

function push {
	docker push app
	echo pushed
}
`, []want{
		{"header", "#!/bin/sh", 1, 2},
		{"func", "build()", 4, 8},
		{"func", "function push", 10, 13},
	})
}

func TestChunkMarkdown(t *testing.T) {
	src := "Intro line.\n\n# Project\n\nWhat it does.\n\n## Install\n\nRun this:\n\n```sh\n# not a heading\nmake install\n```\n\n" +
		"### Linux\n\nUse apt.\nMore text.\nEven more.\n\n#### Deep\n\nStays in Linux.\n\nUsage\n-----\n\nCall it.\nTwice.\nThrice.\n"
	checkChunks(t, "README.md", src, []want{
		{"text", "Intro line.", 1, 1},
		{"heading", "Project", 3, 5},
		{"heading", "Project > Install", 7, 14},
		{"heading", "Project > Install > Linux", 16, 24},
		{"heading", "Project > Usage", 26, 31},
	})
}

// A longer fence holds shorter ones, a closing fence needs the same
// character, and front matter is no heading.
func TestChunkMarkdownFencesAndFrontMatter(t *testing.T) {
	src := "---\ntitle: Guide\nlayout: doc\n---\n\n# Guide\n\nIntro.\n\n````md\n```sh\n# not a heading\n```\n~~~\n# still code\n````\n\n" +
		"## Next\n\nMore.\n\n~~~\n```\n# code\n~~~~ \n\n## Last\n\nEnd.\nReally.\n"
	checkChunks(t, "guide.md", src, []want{
		{"text", "---", 1, 4},
		{"heading", "Guide", 6, 16},
		{"heading", "Guide > Next", 18, 25},
		{"heading", "Guide > Last", 27, 30},
	})
	// An unclosed front matter block is ordinary text.
	checkChunks(t, "loose.md", "---\nnot front matter\n\n# Title\n\nBody.\n", []want{
		{"text", "---", 1, 2},
		{"heading", "Title", 4, 6},
	})
}

func TestChunkConfigAndText(t *testing.T) {
	checkChunks(t, "config.toml", "# settings\n[server]\nport = 1\nhost = \"x\"\nmode = \"y\"\n\n[db]\npath = \"a\"\nwal = true\nsize = 3\n", []want{
		{"section", "[server]", 1, 5},
		{"section", "[db]", 7, 10},
	})
	checkChunks(t, "ci.yml", "name: ci\non:\n  push:\n    branches: [main]\njobs:\n  test:\n    runs-on: ubuntu\n    steps:\n      - run: make test\n", []want{
		{"section", "name: ci", 1, 1},
		{"section", "on", 2, 4},
		{"section", "jobs", 5, 9},
	})
	var long strings.Builder
	for i := range 150 {
		fmt.Fprintf(&long, "line %d of the notes\n", i+1)
		if i%10 == 9 {
			long.WriteString("\n")
		}
	}
	got := checkChunks(t, "NOTES.txt", long.String(), []want{
		{"text", "line 1 of", 1, 32},
		{"text", "line 31 of", 34, 65},
		{"text", "line 61 of", 67, 98},
		{"text", "line 91 of", 100, 131},
		{"text", "line 121 of", 133, 164},
	})
	if got[0].Lang != "text" {
		t.Errorf("lang = %q", got[0].Lang)
	}
}

func TestChunkSplitsBigAndHandlesCRLF(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\r\n\r\nfunc Huge() {\r\n")
	for i := range 200 {
		fmt.Fprintf(&b, "\tx%d := %d\r\n", i, i)
	}
	b.WriteString("}\r\n")
	_, got := chunkFile("big.go", []byte(b.String()))
	if len(got) != 4 {
		t.Fatalf("chunks = %d", len(got))
	}
	if got[1].StartLine != 3 || got[1].EndLine != 82 || got[2].Title != "func Huge() (cont.)" || got[3].EndLine != 204 {
		t.Errorf("windows = %+v %+v %+v", got[1], got[2], got[3])
	}
	if strings.Contains(got[1].Body, "\r") {
		t.Error("carriage returns kept in the body")
	}
	_, mini := chunkFile("app.min.txt", []byte(strings.Repeat("x", 10000)))
	if len(mini) != 1 || len(mini[0].Body) > lineMaxBytes+len("…") || mini[0].StartLine != 1 || mini[0].EndLine != 1 {
		t.Errorf("long line chunk = %d bytes, lines %d-%d", len(mini[0].Body), mini[0].StartLine, mini[0].EndLine)
	}
	if _, empty := chunkFile("empty.go", nil); len(empty) != 0 {
		t.Errorf("empty file chunks = %+v", empty)
	}
}

func TestTermsSplitIdentifiers(t *testing.T) {
	if got := splitIdent("HTTPServerError2"); !slices.Equal(got, []string{"HTTP", "Server", "Error", "2"}) {
		t.Errorf("splitIdent = %v", got)
	}
	got := strings.Fields(termsOf("func NewIndexer() *knowledgeRepo { parseJSON(x); plain() }"))
	for _, w := range []string{"new", "indexer", "knowledge", "repo", "parse", "json"} {
		if !slices.Contains(got, w) {
			t.Errorf("terms %v lack %q", got, w)
		}
	}
	if slices.Contains(got, "plain") {
		t.Errorf("single-word identifiers need no extra terms: %v", got)
	}
	if n := len(termsOf(strings.Repeat("abcDef", 3000) + " " + strings.Repeat("GhiJkl ", 3000))); n > termsMax {
		t.Errorf("terms = %d bytes", n)
	}
}

func TestLangOf(t *testing.T) {
	for p, want := range map[string]string{
		"a/b.go": "go", "Makefile": "make", "x/Dockerfile": "dockerfile", "Dockerfile.dev": "dockerfile", "a.TSX": "typescript",
		"README": "text", "notes.md": "markdown", "lib.rs": "rust", "a.kt": "kotlin", "x.yml": "yaml", "LICENSE": "text",
	} {
		if got := langOf(p).name; got != want {
			t.Errorf("langOf(%s) = %s, want %s", p, got, want)
		}
	}
}
