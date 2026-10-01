package subtitle

import "testing"

// One row per signal that can end a cue.
func TestCueCut(t *testing.T) {
	for _, tt := range []struct {
		name  string
		words []Word
		want  int
	}{{
		name: "a sentence long enough to read closes on its punctuation",
		words: []Word{
			{Start: 0.0, End: 0.6, Text: "Ask"},
			{Start: 0.7, End: 1.3, Text: "not."}, // span 1.3s, at or over cueMinSeconds
			{Start: 1.4, End: 1.7, Text: "What"},
		},
		want: 2,
	}, {
		name: "a sentence too short to read stays open and coalesces",
		words: []Word{
			{Start: 0.0, End: 0.2, Text: "OK."},
			{Start: 0.3, End: 0.6, Text: "So"},
		},
		want: 0, // flashing it on its own is worse than merging it with what follows
	}, {
		name: "a silence closes the cue before it",
		words: []Word{
			{Start: 0.0, End: 0.3, Text: "before"},
			{Start: 2.0, End: 2.3, Text: "after"},
		},
		want: 1,
	}, {
		// The duration cap trips at the last word, but a pause is a better place to cut.
		name: "a cue over the duration cap falls back to the last pause",
		words: []Word{
			{Start: 0.0, End: 0.5, Text: "keep"},
			{Start: 0.6, End: 1.1, Text: "it"},
			{Start: 1.2, End: 2.0, Text: "simple"}, // 0.6s of silence follows
			{Start: 2.6, End: 3.5, Text: "and"},
			{Start: 3.6, End: 4.5, Text: "keep"},
			{Start: 4.6, End: 6.2, Text: "going"}, // span 6.2s, at or over cueMaxSeconds
		},
		want: 3,
	}, {
		name:  "an accented cue is budgeted in characters",
		words: accentedWords(10),
		want:  0,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cueCut(tt.words); got != tt.want {
				t.Errorf("cueCut = %d, want %d", got, tt.want)
			}
		})
	}
}

func accentedWords(n int) []Word {
	words := make([]Word, n)
	for i := range words {
		start := float64(i) * 0.5
		words[i] = Word{Start: start, End: start + 0.3, Text: "\u00e9\u00e9\u00e9\u00e9\u00e9"}
	}
	return words
}

func TestBuilderClosesAndOrders(t *testing.T) {
	b := &Builder{}
	words := []Word{
		{Start: 0.0, End: 0.7, Text: "First"},
		{Start: 0.8, End: 1.4, Text: "line."}, // span 1.4s ≥ cueMinSeconds
		{Start: 3.0, End: 3.7, Text: "Second"},
		{Start: 3.8, End: 4.4, Text: "line."}, // span 1.4s ≥ cueMinSeconds
	}
	b.Commit(words, 5.0)

	cues := b.cues
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d: %v", len(cues), cues)
	}
	if cues[0].start > cues[1].start {
		t.Error("cues must be appended in non-decreasing start order")
	}
	if got := b.cueAt(3.2); got != "Second line." {
		t.Errorf("CueAt(3.2) = %q", got)
	}
	if got := b.cueAt(2.5); got != "" {
		t.Errorf("CueAt in the gap should be empty, got %q", got)
	}
}

func TestBuilderSilentTailClosesParagraphFinalCue(t *testing.T) {
	b := &Builder{}
	sentence := []Word{
		{Start: 0.0, End: 0.7, Text: "The"},
		{Start: 0.8, End: 1.6, Text: "end"}, // no sentence punctuation
	}
	b.Commit(sentence, 1.6)
	if n := len(b.cues); n != 0 {
		t.Fatalf("cue without a closing signal should stay open, got %d cues", n)
	}
	b.Commit(nil, 1.6+cueGapSeconds)
	if n := len(b.cues); n != 1 {
		t.Fatalf("confirmed silence should close the trailing cue, got %d cues", n)
	}
}

func TestWrap(t *testing.T) {
	for _, tt := range []struct {
		name  string
		in    string
		width int
		want  string
	}{
		// Never inside a word: a split word is unreadable at television distance.
		{"a tight width breaks between words", "alpha beta gamma", 10, "alpha beta\ngamma"},
		{"a word longer than the width is left whole", "supercalifragilistic", 10, "supercalifragilistic"},
		// Columns, not bytes.
		{"an accented line gets the full width", "\u00e9t\u00e9 \u00e9t\u00e9 \u00e9t\u00e9", 8, "\u00e9t\u00e9 \u00e9t\u00e9\n\u00e9t\u00e9"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrap(tt.in, tt.width); got != tt.want {
				t.Errorf("wrap(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}
