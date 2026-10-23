package daemon

import (
	"os"
	"strings"
	"testing"
)

const quoteRepo = `**Education:** attended Texas A&M, did not graduate (observed 2026-10-08: *"I did not graduate and
nobody cares anymore"*). Leave education off outward material, and never claim a degree.
*"better to just keep it SE."*`

func TestUnattributedQuotes(t *testing.T) {
	marked := markedQuotes(quoteRepo)
	cases := []struct {
		name string
		text string
		bad  bool
	}{
		{"exact marked quote across a line wrap", `He said "I did not graduate and nobody cares anymore."`, false},
		{"elided marked quote", `"I did not graduate ... nobody cares anymore"`, false},
		{"agent text presented as a quote", `He said "Leave education off outward material, and never claim a degree."`, true},
		{"marked quote glued to agent text", `"I did not graduate and nobody cares anymore. Leave education off outward material"`, true},
		{"two-word term is not a quotation", `List it as "Sales Engineer".`, false},
		{"short paraphrase in quotes is caught", `He said to "keep it as SE".`, true},
		{"curly quotes are checked too", `“never claim a degree under any circumstances”`, true},
	}
	for _, c := range cases {
		got := unattributedQuotes(Answer{Answer: c.text}, marked)
		if (len(got) > 0) != c.bad {
			t.Errorf("%s: unattributed = %q, want bad=%v", c.name, got, c.bad)
		}
	}
}

func TestUnattributedQuotesChecksEveryField(t *testing.T) {
	marked := markedQuotes(quoteRepo)
	a := Answer{Sources: []string{`life.md — "Leave education off outward material, and never claim"`}}
	if got := unattributedQuotes(a, marked); len(got) != 1 {
		t.Fatalf("sources not checked: %q", got)
	}
}

func TestQuoteRejectionNamesTheQuote(t *testing.T) {
	msg := renderQuoteRejection([]string{"never claim a degree"})
	if !strings.Contains(msg, "never claim a degree") {
		t.Fatal(msg)
	}
}

// TestEngineRetriesFabricatedQuote: the fake claude puts agent text in quotes on
// the first call, then answers cleanly once the rejection is in its input.
func TestEngineRetriesFabricatedQuote(t *testing.T) {
	dir := t.TempDir()
	write(t, dir+"/repo/life.md", quoteRepo)
	fake := dir + "/claude"
	write(t, fake, `#!/bin/sh
if grep -q "previous answer was rejected" ; then
  echo '{"structured_output":{"kind":"ANSWER","answer":"Omit education.","reasoning":"","would_change_mind":"","provenance":"DERIVED","sources":[],"missing":[]}}'
else
  echo '{"structured_output":{"kind":"ANSWER","answer":"He said \"Leave education off outward material, and never claim a degree.\"","reasoning":"","would_change_mind":"","provenance":"QUOTED","sources":[],"missing":[]}}'
fi
`)
	chmod(t, fake)
	e := &Engine{RepoDir: dir + "/repo", Provider: &ClaudeCLI{Bin: fake}}
	a, err := e.Answer(t.Context(), &Session{}, "education?")
	if err != nil || a.Answer != "Omit education." {
		t.Fatalf("answer = %q, err = %v", a.Answer, err)
	}
}

// TestEnginePersistentFabricationIsStripped: a model that keeps misquoting gets
// its quotation marks removed on the last attempt, never a fabricated quote.
func TestEnginePersistentFabricationIsStripped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir+"/repo/life.md", quoteRepo)
	fake := dir + "/claude"
	write(t, fake, `#!/bin/sh
cat >/dev/null
echo '{"structured_output":{"kind":"ANSWER","answer":"He said \"Leave education off outward material, and never claim a degree.\" and \"better to just keep it SE.\"","reasoning":"","would_change_mind":"","provenance":"QUOTED","sources":[],"missing":[]}}'
`)
	chmod(t, fake)
	e := &Engine{RepoDir: dir + "/repo", Provider: &ClaudeCLI{Bin: fake}}
	a, err := e.Answer(t.Context(), &Session{}, "education?")
	if err != nil {
		t.Fatal(err)
	}
	want := `He said Leave education off outward material, and never claim a degree. and "better to just keep it SE."`
	if a.Answer != want {
		t.Fatalf("answer = %q\nwant     %q", a.Answer, want)
	}
	if bad := unattributedQuotes(a, markedQuotes(quoteRepo)); len(bad) != 0 {
		t.Fatalf("still misquotes: %q", bad)
	}
}

func chmod(t *testing.T, p string) {
	t.Helper()
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeCLIRetriesStructuredOutputFailure: claude giving up on the schema is
// retried, not surfaced.
func TestClaudeCLIRetriesStructuredOutputFailure(t *testing.T) {
	dir := t.TempDir()
	write(t, dir+"/repo/life.md", quoteRepo)
	fake := dir + "/claude"
	write(t, fake, `#!/bin/sh
cat >/dev/null
if [ -f "`+dir+`/called" ]; then
  echo '{"structured_output":{"kind":"ANSWER","answer":"ok","reasoning":"","would_change_mind":"","provenance":"DERIVED","sources":[],"missing":[]}}'
else
  touch "`+dir+`/called"
  echo '{"is_error":true,"subtype":"error_max_structured_output_retries","result":""}'
  exit 1
fi
`)
	chmod(t, fake)
	e := &Engine{RepoDir: dir + "/repo", Provider: &ClaudeCLI{Bin: fake}}
	if a, err := e.Answer(t.Context(), &Session{}, "q"); err != nil || a.Answer != "ok" {
		t.Fatalf("answer = %q, err = %v", a.Answer, err)
	}
}
