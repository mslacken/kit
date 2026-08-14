package extensions

import "testing"

// upper is a handler that shouts whatever text it is given.
func upperRender(e Event, c Context) Result {
	mr := e.(MessageRenderEvent)
	text := mr.Chunk + "!"
	return MessageRenderResult{Chunk: text}
}

func TestRunner_MessageRenderChainsRewrites(t *testing.T) {
	// The second handler must see what the first produced, not the original.
	ext := makeHandlerExt("render.go", map[EventType][]HandlerFunc{
		MessageRender: {upperRender, upperRender},
	})

	r := makeRunner(ext)
	result, err := r.Emit(MessageRenderEvent{Chunk: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mr, ok := result.(MessageRenderResult)
	if !ok {
		t.Fatalf("expected MessageRenderResult, got %T", result)
	}
	if mr.Chunk != "hi!!" {
		t.Errorf("expected chained rewrite %q, got %q", "hi!!", mr.Chunk)
	}
}

func TestRunner_MessageRenderNoOpKeepsEarlierRewrite(t *testing.T) {
	// A later handler that returns nil (or doesn't modify) keeps previous rewrites.
	ext := makeHandlerExt("render.go", map[EventType][]HandlerFunc{
		MessageRender: {
			upperRender,
			func(e Event, c Context) Result { return nil },
		},
	})

	r := makeRunner(ext)
	result, _ := r.Emit(MessageRenderEvent{Chunk: "hi"})
	mr := result.(MessageRenderResult)
	if mr.Chunk != "hi!" {
		t.Errorf("expected the earlier rewrite to survive, got %q", mr.Chunk)
	}
}

func TestRunner_MessageRenderNilResultPassesThrough(t *testing.T) {
	ext := makeHandlerExt("render.go", map[EventType][]HandlerFunc{
		MessageRender: {
			func(e Event, c Context) Result { return nil },
		},
	})

	r := makeRunner(ext)
	result, _ := r.Emit(MessageRenderEvent{Chunk: "hi"})
	if result != nil {
		t.Errorf("expected nil result, got %#v", result)
	}
}

func TestRunner_MessageRenderSkipStopsChain(t *testing.T) {
	var secondCalled bool
	ext := makeHandlerExt("render.go", map[EventType][]HandlerFunc{
		MessageRender: {
			func(e Event, c Context) Result { return MessageRenderResult{Skip: true} },
			func(e Event, c Context) Result {
				secondCalled = true
				return nil
			},
		},
	})

	r := makeRunner(ext)
	result, _ := r.Emit(MessageRenderEvent{Chunk: "hi"})
	mr, ok := result.(MessageRenderResult)
	if !ok || !mr.Skip {
		t.Fatalf("expected a skipping MessageRenderResult, got %#v", result)
	}
	if secondCalled {
		t.Error("expected Skip to stop the handler chain")
	}
}
