package daemon

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed prompt.md
var systemPrompt string

// Answer is the engine's structured result. Kind and Provenance hold the enum
// suffixes the model is constrained to ("ANSWER", "QUOTED", ...).
type Answer struct {
	Kind            string   `json:"kind"`
	Answer          string   `json:"answer"`
	Reasoning       string   `json:"reasoning"`
	WouldChangeMind string   `json:"would_change_mind"`
	Provenance      string   `json:"provenance"`
	Sources         []string `json:"sources"`
	Missing         []string `json:"missing"`
}

const answerSchema = `{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["ANSWER", "NEEDS_CONTEXT", "BLOCKED_ON_HUMAN"]},
    "answer": {"type": "string"},
    "reasoning": {"type": "string"},
    "would_change_mind": {"type": "string"},
    "provenance": {"type": "string", "enum": ["QUOTED", "DERIVED"]},
    "sources": {"type": "array", "items": {"type": "string"}},
    "missing": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["kind", "answer", "reasoning", "would_change_mind", "provenance", "sources", "missing"]
}`

// Engine forms answers by running the claude CLI headless with no tools. The
// whole jody repo goes into the prompt, and the model can read nothing else.
type Engine struct {
	RepoDir   string
	ClaudeBin string
	Model     string // empty = the CLI's default
}

func (e *Engine) Answer(ctx context.Context, sess *Session, question string) (Answer, error) {
	repo, err := loadRepo(e.RepoDir)
	if err != nil {
		return Answer{}, err
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--tools", "",
		"--json-schema", answerSchema,
		"--system-prompt", systemPrompt,
	}
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}
	cmd := exec.CommandContext(ctx, e.ClaudeBin, args...)
	// The repo goes on stdin, not argv: it is too large for a command-line argument.
	cmd.Stdin = strings.NewReader("# Repository\n\n" + repo + renderTurn(sess, question))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Answer{}, fmt.Errorf("claude: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Answer{}, fmt.Errorf("decode claude output: %w", err)
	}
	if out.IsError || len(out.StructuredOutput) == 0 {
		return Answer{}, fmt.Errorf("claude returned no structured answer: %s", out.Result)
	}
	var a Answer
	if err := json.Unmarshal(out.StructuredOutput, &a); err != nil {
		return Answer{}, fmt.Errorf("decode answer: %w", err)
	}
	return a, nil
}

// renderTurn is the user message: caller context, earlier turns, the question.
func renderTurn(sess *Session, question string) string {
	var b strings.Builder
	b.WriteString("# Caller context\n\n")
	if len(sess.Context) == 0 {
		b.WriteString("(none given)\n")
	}
	for _, c := range sess.Context {
		b.WriteString("- " + c + "\n")
	}
	if len(sess.Turns) > 0 {
		b.WriteString("\n# Earlier turns in this session\n")
		for i, t := range sess.Turns {
			ans, _ := json.Marshal(t.Answer)
			fmt.Fprintf(&b, "\n## Turn %d\n\nQuestion: %s\n\nYour answer: %s\n", i+1, t.Question, ans)
		}
	}
	b.WriteString("\n# Question\n\n" + question + "\n")
	return b.String()
}

// loadRepo concatenates every Markdown file in the repo. inbox/ holds material
// Jody has not reviewed yet, so it is excluded.
func loadRepo(dir string) (string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "inbox") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read jody repo: %w", err)
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no markdown files in jody repo %s", dir)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n\n", rel, data)
	}
	return b.String(), nil
}
