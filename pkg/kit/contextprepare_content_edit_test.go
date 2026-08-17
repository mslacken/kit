package kit

import (
	"reflect"
	"testing"

	"github.com/mark3labs/kit/internal/extensions"
)

// TestContextMessagesToLLM_KeepsToolCallWhenTextEdited verifies the kernel fix:
// when an extension edits the text of an existing message (Index >= 0) while
// that message also carries a tool call, the rebuilt message keeps the tool
// call part and reflects the edited text.
func TestContextMessagesToLLM_KeepsToolCallWhenTextEdited(t *testing.T) {
	original := LLMMessage{
		Role: LLMRoleAssistant,
		Content: []LLMMessagePart{
			LLMTextPart{Text: "hi there"},
			LLMToolCallPart{ToolCallID: "call_1", ToolName: "bash", Input: `{"cmd":"ls"}`},
		},
	}
	cms := []extensions.ContextMessage{
		{Index: 0, Role: "assistant", Content: "hi there (redacted)"},
	}
	out := contextMessagesToLLM(cms, []LLMMessage{original})
	if len(out) != 1 {
		t.Fatalf("want 1 message, got %d", len(out))
	}
	got, ok := out[0].Content[0].(LLMTextPart)
	if !ok {
		t.Fatalf("first part is not a text part: %#v", out[0].Content[0])
	}
	if got.Text != "hi there (redacted)" {
		t.Fatalf("text not edited: %q", got.Text)
	}
	if _, ok := out[0].Content[1].(LLMToolCallPart); !ok {
		t.Fatalf("tool call lost: %#v", out[0].Content[1])
	}
}

// TestContextMessagesToLLM_OriginalTextUnchanged verifies that when the
// extension leaves the text untouched the original message value is returned
// verbatim.
func TestContextMessagesToLLM_OriginalTextUnchanged(t *testing.T) {
	original := LLMMessage{
		Role:    LLMRoleUser,
		Content: []LLMMessagePart{LLMTextPart{Text: "unchanged"}},
	}
	cms := []extensions.ContextMessage{{Index: 0, Role: "user", Content: "unchanged"}}
	out := contextMessagesToLLM(cms, []LLMMessage{original})
	if !reflect.DeepEqual(out, []LLMMessage{original}) {
		t.Fatalf("should return original when content unchanged:\ngot  %#v\nwant %#v", out, []LLMMessage{original})
	}
}

// TestContextMessagesToLLM_MultipleTextPartsFolded verifies a message with more
// than one text part is edited into a single leading text part while any
// trailing non-text part is preserved.
func TestContextMessagesToLLM_MultipleTextPartsFolded(t *testing.T) {
	original := LLMMessage{
		Role: LLMRoleUser,
		Content: []LLMMessagePart{
			LLMTextPart{Text: "hello "},
			LLMReasoningPart{Text: "thinking..."},
			LLMTextPart{Text: "world"},
		},
	}
	cms := []extensions.ContextMessage{{Index: 0, Role: "user", Content: "redacted"}}
	out := contextMessagesToLLM(cms, []LLMMessage{original})
	got, ok := out[0].Content[0].(LLMTextPart)
	if !ok {
		t.Fatalf("first part is not a text part: %#v", out[0].Content[0])
	}
	// The reasoning part is not a TextPart; its text is not part of messageText.
	if got.Text != "redacted" {
		t.Fatalf("expected folded redacted text, got %q", got.Text)
	}
	if len(out[0].Content) != 2 {
		t.Fatalf("expected 2 parts (text + reasoning), got %d: %#v", len(out[0].Content), out[0].Content)
	}
	if _, ok := out[0].Content[1].(LLMReasoningPart); !ok {
		t.Fatalf("reasoning part not preserved: %#v", out[0].Content[1])
	}
}

// TestContextMessagesToLLM_NoExistingTextPrepends verifies that a message with
// no text part receives the edited text as a new leading text part.
func TestContextMessagesToLLM_NoExistingTextPrepends(t *testing.T) {
	original := LLMMessage{
		Role:    LLMRoleAssistant,
		Content: []LLMMessagePart{LLMToolCallPart{ToolCallID: "c", ToolName: "bash", Input: `{}`}},
	}
	cms := []extensions.ContextMessage{{Index: 0, Role: "assistant", Content: "added text"}}
	out := contextMessagesToLLM(cms, []LLMMessage{original})
	got, ok := out[0].Content[0].(LLMTextPart)
	if !ok {
		t.Fatalf("expected a leading text part, got %#v", out[0].Content[0])
	}
	if got.Text != "added text" {
		t.Fatalf("expected prepended text, got %q", got.Text)
	}
	if _, ok := out[0].Content[1].(LLMToolCallPart); !ok {
		t.Fatalf("tool call lost: %#v", out[0].Content[1])
	}
}
