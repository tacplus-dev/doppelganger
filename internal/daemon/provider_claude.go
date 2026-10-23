package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeCLI answers by running the claude CLI headless with no tools. It is
// the interim provider until every deployment has a direct API credential;
// the daemon should not depend on an external program.
type ClaudeCLI struct {
	Bin   string
	Model string // empty = the CLI's default
}

func (p *ClaudeCLI) Complete(ctx context.Context, req Request) (json.RawMessage, error) {
	args := []string{
		"-p",
		"--output-format", "json",
		"--tools", "",
		"--json-schema", req.Schema,
		"--system-prompt", req.System,
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	cmd := exec.CommandContext(ctx, p.Bin, args...)
	// The repo goes on stdin, not argv: it is too large for a command-line argument.
	cmd.Stdin = strings.NewReader("# Repository\n\n" + req.Repo + req.Turn)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	var out struct {
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
		TerminalReason   string          `json:"terminal_reason"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("claude: %w: stderr=%q stdout=%q", runErr, tail(stderr.String()), tail(stdout.String()))
		}
		return nil, fmt.Errorf("decode claude output: %w", err)
	}
	if out.Subtype == "error_max_structured_output_retries" {
		return nil, errStructuredOutput
	}
	if runErr != nil {
		// claude -p reports its own errors as JSON on stdout.
		return nil, fmt.Errorf("claude: %w: subtype=%q terminal_reason=%q result=%q", runErr, out.Subtype, out.TerminalReason, tail(out.Result))
	}
	if out.IsError || len(out.StructuredOutput) == 0 {
		return nil, fmt.Errorf("claude returned no structured answer: %s", out.Result)
	}
	return out.StructuredOutput, nil
}

// tail keeps the end of a process's output for an error message.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return "…" + s[len(s)-2000:]
	}
	return s
}
