// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"errors"
	"strings"
	"testing"
)

func TestStructuredOutputAcceptsBareAndFencedJSON(t *testing.T) {
	for _, in := range []string{
		`{"kind":"ANSWER","answer":"ok"}`,
		"```json\n{\"kind\": \"ANSWER\",\n \"answer\": \"ok\"}\n```",
		"  ```\n{\"kind\":\"ANSWER\",\"answer\":\"ok\"}\n```\n",
	} {
		got, err := structuredOutput(in)
		if err != nil || string(got) != `{"kind":"ANSWER","answer":"ok"}` {
			t.Fatalf("structuredOutput(%q) = %s, %v", in, got, err)
		}
	}
}

func TestStructuredOutputRejectsProseAsRetryable(t *testing.T) {
	_, err := structuredOutput("Sure! Here is the answer: {\"kind\": \"ANSWER\"} hope that helps")
	if !errors.Is(err, errStructuredOutput) {
		t.Fatalf("err = %v, want errStructuredOutput so the engine retries", err)
	}
}

func TestUserTextCarriesRepoTurnAndSchema(t *testing.T) {
	s := userText(Request{Repo: "REPO", Turn: "\nTURN", Schema: `{"type":"object"}`})
	for _, want := range []string{"# Repository", "REPO", "TURN", `{"type":"object"}`, "nothing else"} {
		if !strings.Contains(s, want) {
			t.Errorf("user text missing %q", want)
		}
	}
}
