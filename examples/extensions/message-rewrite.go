//go:build ignore

// message-rewrite.go — OnMessageRender example extension for Kit.
//
// Demonstrates api.OnMessageRender(), which rewrites assistant text on its
// way to the display. The change is display-only: the session transcript and
// the context sent back to the model keep the original text.
//
// This extension shows how a plugin can intercept and rewrite streaming chunks,
// or buffer them and decide what is printed.
//
// Usage: kit -e examples/extensions/message-rewrite.go

package main

import (
	"strings"

	ext "kit/ext"
)

func Init(api ext.API) {
	// Replace "utilize" with "use" on a chunk-by-chunk basis
	api.OnMessageRender(func(e ext.MessageRenderEvent, ctx ext.Context) *ext.MessageRenderResult {
		text := strings.ReplaceAll(e.Chunk, "utilize", "use")
		text = strings.ReplaceAll(text, "Utilize", "Use")
		if text == e.Chunk {
			return nil // Let chunk pass through unchanged
		}
		return &ext.MessageRenderResult{Chunk: text}
	})
}
