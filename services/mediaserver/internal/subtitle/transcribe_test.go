package subtitle

import (
	"testing"
)

func TestAgreedPrefix(t *testing.T) {
	prev := []Word{
		{Start: 1.0, End: 1.3, Text: "Hello,"},
		{Start: 1.4, End: 1.8, Text: "world"},
		{Start: 1.9, End: 2.2, Text: "again"},
	}
	cur := []Word{
		{Start: 1.1, End: 1.4, Text: "hello"}, // case/punct differ, times within tolerance
		{Start: 1.5, End: 1.9, Text: "world"},
		{Start: 2.0, End: 2.3, Text: "against"}, // text mismatch stops the prefix
	}
	got := agreedPrefix(prev, cur)
	if len(got) != 2 {
		t.Fatalf("agreedPrefix = %d words, want 2 (%v)", len(got), got)
	}
	if got[0].Text != "hello" {
		t.Errorf("committed word should carry the current hypothesis' surface form, got %q", got[0].Text)
	}
	if got := agreedPrefix(nil, cur); len(got) != 0 {
		t.Errorf("first hypothesis must commit nothing, got %v", got)
	}
}

func TestDropCommitted(t *testing.T) {
	words := []Word{
		{Start: 0.0, End: 0.5, Text: "old"},
		{Start: 0.6, End: 1.0, Text: "boundary"},
		{Start: 1.2, End: 1.6, Text: "new"},
	}
	got := dropCommitted(words, 1.0)
	if len(got) != 1 || got[0].Text != "new" {
		t.Fatalf("dropCommitted = %v, want just the word past the frontier", got)
	}
}
