package daemon

import (
	"regexp"
	"strings"
)

// Quote checking. A model told "only quote Jody's marked words" still merges
// agent-written text into quotes, so the rule is enforced here, not in the
// prompt. In the repo Jody's own words are marked *"like this"*; everything
// else was written by agents.

// markedQuote matches Jody's words as the repo marks them: *"..."*, possibly
// spanning lines.
var markedQuote = regexp.MustCompile(`\*"([^*]+?)"\*`)

// answerQuote matches anything the model put in straight or curly quotes.
var answerQuote = regexp.MustCompile(`"([^"]+)"|“([^”]+)”`)

// minQuoteWords is the shortest quoted phrase that counts as a quotation. A
// shorter one ("Sales Engineer", "SE") is a term, not a quote. Three words already
// catches short paraphrases such as "keep it as SE".
const minQuoteWords = 3

// markedQuotes returns the normalised text of every marked quote in the repo.
func markedQuotes(repo string) []string {
	var out []string
	for _, m := range markedQuote.FindAllStringSubmatch(repo, -1) {
		out = append(out, normalise(m[1]))
	}
	return out
}

// unattributedQuotes returns each quotation in the answer that is not, fragment
// by fragment, a verbatim part of one of Jody's marked quotes. A quotation may
// elide with "..." or "…"; each fragment is checked on its own.
func unattributedQuotes(a Answer, marked []string) []string {
	fields := append([]string{a.Answer, a.Reasoning, a.WouldChangeMind}, a.Sources...)
	var bad []string
	for _, f := range fields {
		for _, m := range answerQuote.FindAllStringSubmatch(f, -1) {
			q := m[1] + m[2]
			if len(strings.Fields(q)) < minQuoteWords {
				continue
			}
			if !isMarked(q, marked) {
				bad = append(bad, q)
			}
		}
	}
	return bad
}

func isMarked(q string, marked []string) bool {
	for _, frag := range strings.FieldsFunc(strings.ReplaceAll(q, "...", "…"), func(r rune) bool { return r == '…' }) {
		frag = normalise(frag)
		if frag == "" {
			continue
		}
		found := false
		for _, m := range marked {
			if strings.Contains(m, frag) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// normalise makes the comparison insensitive to case, line wrapping, editorial
// brackets, and trailing punctuation.
func normalise(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("[", "", "]", "", "’", "'", "‘", "'").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " .,;:!?")
}

// stripQuotes removes the quotation marks around each unattributed quotation in
// every field, leaving the words. It only ever removes a claim that Jody said
// something; it never adds or changes content.
func stripQuotes(a Answer, bad []string) Answer {
	var pairs []string
	for _, q := range bad {
		pairs = append(pairs, `"`+q+`"`, q, "“"+q+"”", q)
	}
	r := strings.NewReplacer(pairs...)
	a.Answer = r.Replace(a.Answer)
	a.Reasoning = r.Replace(a.Reasoning)
	a.WouldChangeMind = r.Replace(a.WouldChangeMind)
	sources := make([]string, len(a.Sources))
	for i, src := range a.Sources {
		sources[i] = r.Replace(src)
	}
	a.Sources = sources
	return a
}
