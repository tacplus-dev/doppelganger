// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	llmproxy_gc "github.com/tacplus-dev/doppelganger/tacplus/s2sm/grpc/clients/services/llmproxy"
	llmproxy_pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/llmproxy"
)

// LlmProxy is the fleet provider: one GenerateCompletionAsService call per
// Request through the tacplus-generated llmproxy client, which carries this
// service's own identity (the fleet build admits doppelganger as a caller).
// llmproxy holds the model credential; this service never sees one.
//
// The schema travels in the prompt (llmproxy has no output-schema field yet,
// tacplus-dev/llmproxy#2); the reply must be the JSON document alone, a fenced
// block is unwrapped, anything else is errStructuredOutput and the engine
// retries.
type LlmProxy struct {
	Client     *llmproxy_gc.LlmProxyClient
	ModelAlias string
}

func (p *LlmProxy) Complete(ctx context.Context, req Request) (json.RawMessage, error) {
	raw, _, err := p.CompleteWithModel(ctx, req)
	return raw, err
}

// CompleteWithModel is Complete for a panel seat. The model it names is
// always empty for now: llmproxy does not yet say which model a "latest"
// alias resolved to (tacplus-dev/llmproxy#13), so a panel answer records the
// alias alone until it does.
func (p *LlmProxy) CompleteWithModel(ctx context.Context, req Request) (json.RawMessage, string, error) {
	resp, err := p.Client.GenerateCompletionAsService(ctx, &llmproxy_pb.GenerateCompletionRequest{
		ModelAlias: p.ModelAlias,
		System:     req.System,
		Messages: []*llmproxy_pb.Message{{
			Role:    llmproxy_pb.Role_ROLE_USER,
			Content: []*llmproxy_pb.ContentBlock{{Block: &llmproxy_pb.ContentBlock_Text{Text: userText(req)}}},
		}},
	})
	if err != nil {
		return nil, "", fmt.Errorf("llmproxy: %w", err)
	}
	raw, err := structuredOutput(resp.GetText())
	return raw, "", err
}

// userText is the whole context repo (none for a panel seat, whose turn
// carries all it is given), the turn, and the output contract.
func userText(req Request) string {
	repo := ""
	if req.Repo != "" {
		repo = "# Repository\n\n" + req.Repo
	}
	return repo + req.Turn +
		"\n\nReply with exactly one JSON document that satisfies this JSON Schema, and nothing else:\n" + req.Schema + "\n"
}

// structuredOutput extracts the JSON document from a model reply: the text as
// is, or the body of a single ```json fence. Anything that is not one JSON
// value is a structured-output failure.
func structuredOutput(text string) (json.RawMessage, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // drop the language tag line
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("%w: %s", errStructuredOutput, tail(s))
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(s)); err != nil {
		return nil, fmt.Errorf("%w: %v", errStructuredOutput, err)
	}
	return compact.Bytes(), nil
}

// tail keeps the end of a reply for an error message.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return "…" + s[len(s)-500:]
	}
	return s
}
