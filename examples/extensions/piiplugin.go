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
// Options:
//
//	pii/highlight   "1" (default) | "0"  — add ANSI styling on restore
//	pii/log         "1" (default) | "0"  — write a redaction/restoration log
//	pii/log-file    path (default ~/.kit/pii.log)
//
// (env var KIT_OPT_PIILIST_HIGHLIGHT, .kit.yml options.pii/highlight, or
// ctx.SetOption). When the session is non-interactive, styling is dropped and
// the restored output is plain text. Every redacted span and every restored
// span is appended to the log file as
//
//	2026-08-17T10:02:11Z | REDACTED | <token> → <original>
//	2026-08-17T10:02:12Z | RESTORED | <token> → <original>
//
// Inspect it with /pii-log (show | tail [n] | clear|reset | path).
//
// Usage:
//
//	kit -e examples/extensions/piiplugin.go
//	KIT_OPT_PIILIST_HIGHLIGHT=0 kit -e examples/extensions/piiplugin.go

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

	logMu       sync.Mutex
	logRedacted int
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
// may extend the replacement table. It also returns any newly-added spans so
// the caller can log them after the lock is released (avoids I/O under lock).
func redactContent(text, fullInput string) (string, map[string]string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	// Copy the map by value: maps are reference types, so assigning
	// `before = *filter.Replacements` would alias the live map and the diff
	// below would see Redact's own insertions. A value copy keeps `before` as
	// a true snapshot of the pre-redaction state.
	before := map[string]string{}
	if filter.Replacements != nil {
		for k, v := range *filter.Replacements {
			before[k] = v
		}
	}
	out := filter.Redact(text, fullInput)
	added := map[string]string{}
	if filter.Replacements != nil {
		for token, orig := range *filter.Replacements {
			if _, ok := before[token]; !ok && orig != "" {
				added[token] = orig
			}
		}
	}
	return out, added
}

// logRedactions writes one REDACTED line per newly-added span. Called with the
// state lock released.
func logRedactions(ctx ext.Context, added map[string]string) {
	for token, orig := range added {
		logRedaction(ctx, token, orig)
	}
}

// ---------------------------------------------------------------------------
// Redaction logging helper. Every span is appended to a file (one line each):
//
//	2026-08-17T10:02:11Z | REDACTED | <token> → <original>
//	2026-08-17T10:02:12Z | RESTORED | <token> → <original>
// ---------------------------------------------------------------------------

// logPath resolves the log file path: the pii/log-file option (default
// "~/.kit/pii.log"), then ensures the parent directory exists.
func logPath(ctx ext.Context) string {
	path := ctx.GetOption("pii/log-file")
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + "/.kit/pii.log"
		}
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	return path
}

// logEnabled reads the pii/log option (default on).
func logEnabled(ctx ext.Context) bool {
	v, err := strconv.ParseBool(ctx.GetOption("pii/log"))
	if err != nil {
		return true
	}
	return v
}

// logRedaction records a REDACTED line: the replacement token that replaced
// the original span.
func logRedaction(ctx ext.Context, token, original string) {
	writeLog(ctx, "REDACTED", token, original)
}

// logRestoration records a RESTORED line: a token found in assistant output
// and replaced by its original value.
func logRestoration(ctx ext.Context, token, original string) {
	writeLog(ctx, "RESTORED", token, original)
}

// writeLog appends one timestamped line. All failures are swallowed — a log
// write must never disturb the redact/restore pass.
func writeLog(ctx ext.Context, kind, fieldA, fieldB string) {
	if !logEnabled(ctx) {
		return
	}
	line := time.Now().Format(time.RFC3339) + " | " + kind +
		" | " + fieldA + " -> " + fieldB + "\n"
	path := logPath(ctx)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	logMu.Lock()
	if kind == "REDACTED" {
		logRedacted++
	}
	f.WriteString(line)
	logMu.Unlock()
}

// logRedactedCount returns the number of redaction lines written this process.
func logRedactedCount() int {
	logMu.Lock()
	defer logMu.Unlock()
	return logRedacted
}

// styledUnredact returns text with each known replacement token replaced by
// its original value wrapped in bold bright cyan, and each table key with no
// usable mapping (defensive) wrapped in strikethrough yellow so an
// un-restorable span is visible. Every restoration is also logged.
//
// Keys are sorted length-descending so a longer key (e.g. "example.com") wins
// over a shorter one ("com"), matching the matching order of filter.Unredact.
func styledUnredact(text string, reps map[string]string, ctx ext.Context) string {
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
			logRestoration(ctx, matched, orig)
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
func unredactChunk(text string, highlight bool, ctx ext.Context) string {
	if highlight {
		return styledUnredact(text, getReps(), ctx)
	}
	return plainUnredact(text, ctx)
}

// plainUnredact restores values without any ANSI styling (used when highlighting
// is off or the session is non-interactive). Each token it hits is logged.
func plainUnredact(text string, ctx ext.Context) string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	if filter.Replacements != nil {
		for token, orig := range *filter.Replacements {
			if strings.Contains(text, token) && orig != "" {
				logRestoration(ctx, token, orig)
			}
		}
	}
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
	api.RegisterOption(ext.OptionDef{
		Name:        "pii/log",
		Description: "Write a redaction/restoration log",
		Default:     "1",
	})
	api.RegisterOption(ext.OptionDef{
		Name:        "pii/log-file",
		Description: "Log file path (default ~/.kit/pii.log)",
		Default:     "",
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
			redacted, added := redactContent(m.Content, fullInput)
			if len(added) > 0 {
				logRedactions(ctx, added)
			}
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
			return &ext.MessageRenderResult{Chunk: unredactChunk(safe, true, ctx)}
		}
		return &ext.MessageRenderResult{Chunk: plainUnredact(safe, ctx)}
	})

	// Drain the carry tail at message end so the final span is never lost.
	api.OnMessageEnd(func(_ ext.MessageEndEvent, ctx ext.Context) {
		if carry == "" {
			return
		}
		out := ""
		if highlightEnabled(ctx) {
			out = unredactChunk(carry, true, ctx)
		} else {
			out = plainUnredact(carry, ctx)
		}
		carry = ""
		if out != "" {
			ctx.PrintInfo(out)
		}
	})

	// --- /pii-log: inspect the redaction/restoration log ---
	api.RegisterCommand(ext.CommandDef{
		Name:        "pii-log",
		Description: "Show the PII log (tail [n] | clear | path)",
		Complete: func(prefix string, ctx ext.Context) []string {
			return []string{"tail", "clear", "path"}
		},
		Execute: func(args string, ctx ext.Context) (string, error) {
			words := strings.Fields(strings.TrimSpace(args))
			cmd := "tail"
			n := 15
			if len(words) > 0 {
				cmd = words[0]
				if len(words) > 1 {
					if v, err := strconv.Atoi(words[1]); err == nil && v > 0 {
						n = v
					}
				} else if cmd == "tail" {
					if v, err := strconv.Atoi(words[0]); err == nil && v > 0 {
						n = v
					}
				}
			}
			path := logPath(ctx)
			switch cmd {
			case "path":
				return path, nil
			case "clear":
				os.Remove(path)
				logMu.Lock()
				logRedacted = 0
				logMu.Unlock()
				return "log cleared: " + path, nil
			default:
				data, err := os.ReadFile(path)
				if err != nil {
					return "no log at " + path + " (redactions logged this session: " +
						strconv.Itoa(logRedactedCount()) + ")", nil
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) > n {
					lines = lines[len(lines)-n:]
				}
				return strings.Join(lines, "\n"), nil
			}
		},
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
