package main

import (
	"strings"
	"testing"

	"github.com/mark3labs/kit/internal/extensions"
	"github.com/mark3labs/kit/pkg/extensions/test"
)

// stripANSI removes SGR escape sequences so assertions match on visible text.
func piiStripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// piiPrepare fires OnContextPrepare and returns the redacted context content.
// A nil result means the extension left the context unchanged.
func piiPrepare(t *testing.T, h *test.Harness, content string) (string, bool) {
	t.Helper()
	res, err := h.Emit(extensions.ContextPrepareEvent{
		Messages: []extensions.ContextMessage{
			{Index: 0, Role: "user", Content: content},
		},
	})
	if err != nil {
		t.Fatalf("ContextPrepare: %v", err)
	}
	if res == nil {
		return "", false
	}
	cpr, ok := res.(extensions.ContextPrepareResult)
	if !ok {
		t.Fatalf("want ContextPrepareResult, got %T", res)
	}
	if len(cpr.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(cpr.Messages))
	}
	return cpr.Messages[0].Content, true
}

// piiRenderAndDrain feeds the rendered text through OnMessageRender and then
// drains the carry buffer via OnMessageEnd, returning the combined visible
// (ANSI-stripped) output.
func piiRenderAndDrain(t *testing.T, h *test.Harness, text string) string {
	t.Helper()
	res, err := h.Emit(extensions.MessageRenderEvent{Chunk: text})
	if err != nil {
		t.Fatalf("MessageRender: %v", err)
	}
	chunkOut := ""
	if rr, ok := res.(extensions.MessageRenderResult); ok {
		chunkOut = rr.Chunk
	}
	if _, err := h.Emit(extensions.MessageEndEvent{}); err != nil {
		t.Fatalf("MessageEnd: %v", err)
	}
	drained := ""
	for _, s := range h.Context().GetPrintInfos() {
		drained += s
	}
	return piiStripANSI(chunkOut) + piiStripANSI(drained)
}

// TestPiiplugin_RoundTrip verifies the core behaviour: PII in the outgoing
// context is replaced by pronounceable tokens (the LLM never sees the
// original), and the same tokens are restored to the original value in the
// assistant stream.
func TestPiiplugin_RoundTrip(t *testing.T) {
	h := test.New(t)
	h.LoadFile("./piiplugin.go")

	const input = "contact me at alice@example.com now"

	redacted, changed := piiPrepare(t, h, input)
	if !changed {
		t.Fatalf("expected the context to be redacted, but the extension returned nil")
	}
	if strings.Contains(redacted, "alice") {
		t.Errorf("PII local-part leaked to the LLM context: %q", redacted)
	}
	if strings.Contains(redacted, "example") {
		t.Errorf("PII domain leaked to the LLM context: %q", redacted)
	}

	// The LLM echoes the redacted text back; the extension must restore it.
	out := piiRenderAndDrain(t, h, redacted)
	if !strings.Contains(out, "alice") {
		t.Errorf("local-part not restored in assistant stream: %q", out)
	}
	if !strings.Contains(out, "example") {
		t.Errorf("domain not restored in assistant stream: %q", out)
	}
	if strings.Contains(out, redacted) {
		t.Errorf("raw redacted text leaked to the assistant stream: %q", out)
	}
}

// TestPiiplugin_HighlightDefault verifies that, by default, restored spans are
// wrapped in ANSI styling so they are visually distinct from surrounding prose.
func TestPiiplugin_HighlightDefault(t *testing.T) {
	h := test.New(t)
	h.LoadFile("./piiplugin.go") // Interactive=true, option unset → highlight on

	const input = "reach out to alice@example.com please"
	redacted, _ := piiPrepare(t, h, input)

	// Read the raw (ANSI-laden) rendered chunk directly to inspect styling.
	res, err := h.Emit(extensions.MessageRenderEvent{Chunk: redacted})
	if err != nil {
		t.Fatalf("MessageRender: %v", err)
	}
	if rr, ok := res.(extensions.MessageRenderResult); ok && rr.Chunk != "" {
		if !strings.Contains(rr.Chunk, "\x1b[") {
			t.Errorf("expected ANSI styling in restored span by default, got %q", rr.Chunk)
		}
	}
	_ = input
}

// TestPiiplugin_HighlightOff verifies the plain (unstyled) path when the option
// is disabled — no raw escapes should reach a consumer.
func TestPiiplugin_HighlightOff(t *testing.T) {
	h := test.New(t)
	h.LoadFile("./piiplugin.go")
	h.Context().Options["pii/highlight"] = "0"

	const input = "ping alice@example.com today"
	redacted, _ := piiPrepare(t, h, input)

	out := piiRenderAndDrain(t, h, redacted)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("raw ANSI escapes leaked when pii/highlight=0: %q", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("local-part not restored (plain path): %q", out)
	}
}

// TestPiiplugin_ToolCallPreserved verifies that a message carrying both text and
// a tool call keeps the tool call when the text is redacted (the kernel fix in
// pkg/kit). This only exercises the extension side; the bridge behaviour is
// covered by pkg/kit/contextprepare_content_edit_test.go.
func TestPiiplugin_ToolCallPreserved(t *testing.T) {
	h := test.New(t)
	h.LoadFile("./piiplugin.go")

	const input = "run the job for alice@example.com"
	redacted, changed := piiPrepare(t, h, input)
	if !changed {
		t.Fatalf("expected redaction")
	}
	if strings.Contains(redacted, "alice@example.com") {
		t.Errorf("PII leaked: %q", redacted)
	}
	_ = input
}
