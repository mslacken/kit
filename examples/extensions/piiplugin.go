//go:build ignore

// piiplugin.go — openSUSE/piiplugin example for Kit.
//
// Redacts PII (user names, email addresses, host names) in every context
// window before it is sent to an external LLM, then restores the original
// values in the assistant stream with a cyan-bold visual marker so the user
// can see which spans were redacted and re-substituted.
//
// Uses the CGO-free getent source for the user-name filter so the extension
// loads under CGO_ENABLED=0 builds.
//
// Option:
//
//	pii/highlight   "1" (default) | "0"  — add ANSI styling on restore
//
// (env var KIT_OPT_PIILIST_HIGHLIGHT, .kit.yml options.pii/highlight, or
// ctx.SetOption). When the session is non-interactive, styling is dropped and
// the restored output is plain text.
//
// Usage:
//
//	kit -e examples/extensions/piiplugin.go
//	KIT_OPT_PIILIST_HIGHLIGHT=0 kit -e examples/extensions/piiplugin.go

package main

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	ext "kit/ext"
	pii "kit/pii"
)

// ---------------------------------------------------------------------------
// Package-level state. Yaegi supports package-level variables captured in
// closures (see AGENTS.md — "Package-level variables in extensions"). The
// single PiiFilter is shared between the redact side (OnContextPrepare) and
// the unredact side (OnMessageRender) so the replacement table stays
// consistent for the LLM exactly as in the piiplugin README design.
//
// stateMu guards filter.Replacements (a *map[string]string):
//   - Redact writes new tokens to the table        → write lock
//   - Unredact / styled scan / max-token scan read → read lock
// ---------------------------------------------------------------------------

var (
	stateMu sync.RWMutex
	filter  *pii.PiiFilter
)

// ---------------------------------------------------------------------------
// Helpers — declared ABOVE Init so the closures registered inside Init can
// reference them by name (see AGENTS.md — "Forward-reference function bug").
// ---------------------------------------------------------------------------

// getReps returns a snapshot of the replacement table taken under the read
// lock. The copy lets the caller style a long chunk without holding the lock
// for the whole pass, and shields the scan from a concurrent Redact.
func getReps() map[string]string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	if filter == nil || filter.Replacements == nil {
		return nil
	}
	m := *filter.Replacements
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// carryLen returns the number of bytes to retain at the tail of the streaming
// buffer so a replacement token that straddles a chunk boundary is never
// half-unredacted. It is the length of the longest replacement key in the live
// table (the table is seeded by redaction, which always precedes the assistant
// stream, so the value is already present here). 0 means nothing to restore.
func carryLen() int {
	reps := getReps()
	max := 0
	for k := range reps {
		if len(k) > max {
			max = len(k)
		}
	}
	return max
}

// redactContent redacts text, holding the write lock around Redact because it
// may extend the replacement table.
func redactContent(text, fullInput string) string {
	stateMu.Lock()
	defer stateMu.Unlock()
	return filter.Redact(text, fullInput)
}

// styledUnredact returns text with each known replacement token replaced by
// its original value wrapped in bold bright cyan, and each table key with no
// usable mapping (defensive) wrapped in strikethrough yellow so an
// un-restorable span is visible.
//
// Keys are sorted length-descending so a longer key (e.g. "example.com") wins
// over a shorter one ("com"), matching the matching order of filter.Unredact.
func styledUnredact(text string, reps map[string]string) string {
	if len(reps) == 0 {
		return text
	}
	keys := make([]string, 0, len(reps))
	for k := range reps {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	b := strings.Builder{}
	b.Grow(len(text))
	for i := 0; i < len(text); {
		matched := ""
		for _, k := range keys {
			if strings.HasPrefix(text[i:], k) {
				matched = k
				break
			}
		}
		if matched == "" {
			b.WriteByte(text[i])
			i++
			continue
		}
		if orig, ok := reps[matched]; ok && orig != "" {
			b.WriteString("\x1b[1m\x1b[38;5;51m" + orig + "\x1b[0m") // bold bright cyan
		} else {
			b.WriteString("\x1b[9m\x1b[38;5;180m" + matched + "\x1b[0m") // strike yellow
		}
		i += len(matched)
	}
	return b.String()
}

// unredactChunk restores the original values of a piece of streamed text and
// applies styling when enabled. It snapshots the table under the read lock and
// never touches the write path, so it is safe to call concurrently with redact.
func unredactChunk(text string, highlight bool) string {
	if highlight {
		return styledUnredact(text, getReps())
	}
	return plainUnredact(text)
}

// plainUnredact restores values without any ANSI styling (used when highlighting
// is off or the session is non-interactive).
func plainUnredact(text string) string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return filter.Unredact(text)
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func Init(api ext.API) {
	api.RegisterOption(ext.OptionDef{
		Name:        "pii/highlight",
		Description: "Add ANSI highlighting to restored PII spans",
		Default:     "1",
	})

	// Build the filter once with the getent user-name source so the build works
	// with CGO_ENABLED=0. All three filters (username, email, host) are enabled
	// by default.
	filter = pii.NewPiiFilter(pii.WithUsernameSource(pii.SourceGetent))

	// --- Redact on the way out to the LLM ---
	api.OnContextPrepare(func(e ext.ContextPrepareEvent, ctx ext.Context) *ext.ContextPrepareResult {
		// fullInput must be the whole payload so generated tokens are guaranteed
		// not to collide with any real text already present.
		var full strings.Builder
		for _, m := range e.Messages {
			full.WriteString(m.Content)
			full.WriteString("\n")
		}
		fullInput := full.String()

		msgs := make([]ext.ContextMessage, 0, len(e.Messages))
		changed := false
		for _, m := range e.Messages {
			redacted := redactContent(m.Content, fullInput)
			if redacted == m.Content {
				// Untouched — keep the original (Index >= 0) so non-text parts
				// (tool calls, tool results) are preserved verbatim by the bridge.
				msgs = append(msgs, m)
				continue
			}
			changed = true
			msgs = append(msgs, ext.ContextMessage{
				Index:   m.Index, // Index >= 0 + changed Content → bridge edits text, keeps parts
				Role:    m.Role,
				Content: redacted,
			})
		}
		if !changed {
			return nil // nothing edited — leave the context alone
		}
		return &ext.ContextPrepareResult{Messages: msgs}
	})

	// --- Unredact + highlight on the way in, chunk by chunk ---
	//
	// A replacement token can split across two chunks (chunk N ends "…icav",
	// chunk N+1 starts "yast…"). To avoid half-restoring it we keep a carry
	// tail sized to the longest token and only commit a prefix that cannot be
	// the head of a pending token. The carry is drained at OnMessageEnd.
	carry := ""

	api.OnMessageRender(func(e ext.MessageRenderEvent, ctx ext.Context) *ext.MessageRenderResult {
		carry += e.Chunk
		cl := carryLen()
		if len(carry) <= cl {
			// Not yet safe to commit; hold the text in carry and emit nothing.
			return &ext.MessageRenderResult{Chunk: ""}
		}
		safe, tail := carry[:len(carry)-cl], carry[len(carry)-cl:]
		carry = tail

		highlight := highlightEnabled(ctx)
		if highlight {
			return &ext.MessageRenderResult{Chunk: unredactChunk(safe, true)}
		}
		return &ext.MessageRenderResult{Chunk: plainUnredact(safe)}
	})

	// Drain the carry tail at message end so the final span is never lost.
	api.OnMessageEnd(func(_ ext.MessageEndEvent, ctx ext.Context) {
		if carry == "" {
			return
		}
		out := ""
		if highlightEnabled(ctx) {
			out = unredactChunk(carry, true)
		} else {
			out = plainUnredact(carry)
		}
		carry = ""
		if out != "" {
			ctx.PrintInfo(out)
		}
	})
}

// highlightEnabled resolves the pii/highlight option and only allows ANSI
// styling in an interactive session (a non-TUI consumer would see raw escapes).
func highlightEnabled(ctx ext.Context) bool {
	if !ctx.Interactive {
		return false
	}
	v, err := strconv.ParseBool(ctx.GetOption("pii/highlight"))
	if err != nil {
		return true // default on
	}
	return v
}
