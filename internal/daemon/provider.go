package daemon

import (
	"context"
	"encoding/json"
	"errors"
)

// Provider is the seam between the engine and a model. The engine owns the
// prompt, the retry loop and quote enforcement; a Provider only turns one
// Request into the model's JSON answer. Every provider gets the same contract:
// no tools, the whole repo as input, output constrained to Request.Schema.
type Provider interface {
	Complete(ctx context.Context, req Request) (json.RawMessage, error)
}

// Request is one model call.
type Request struct {
	System string // system prompt
	Repo   string // the whole context repo; identical on every call, so providers may cache it
	Turn   string // caller context, earlier turns, the question, and any rejection of a previous answer
	Schema string // JSON Schema the answer must satisfy
}

// errStructuredOutput marks a model giving up on the JSON schema, which is
// transient: the same input usually succeeds on the next call.
var errStructuredOutput = errors.New("model could not produce structured output")
