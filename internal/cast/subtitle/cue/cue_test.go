package cue

import "testing"

// TestCueCut is where a cue ends, stated once per signal that can end it. A cut
// is an index into the words handed in, so a row is a sequence of words and the
// count that must be closed off ahead of the rest.
//
// The rules interact, which is why they belong in one table rather than one test
// each: a sentence that ends too soon to be read stays open regardless of its
// punctuation, and a cue held past the duration cap gives up the cap rather than
// slice a phrase, so reading a row means reading it against its neighbours.
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
		name:  "no closing signal leaves the cue open",
		words: []Word{{Start: 0.0, End: 0.3, Text: "still"}, {Start: 0.4, End: 0.7, Text: "going"}},
		want:  0,
	}, {
		// The duration cap trips at the last word, but a pause is a better place to
		// cut than the middle of the phrase that follows it.
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
		name: "a cue over the duration cap falls back to the last comma",
		words: []Word{
			{Start: 0.0, End: 0.6, Text: "we"},
			{Start: 0.7, End: 1.3, Text: "hold"},
			{Start: 1.4, End: 2.0, Text: "these"},
			{Start: 2.1, End: 3.0, Text: "truths,"},
			{Start: 3.1, End: 4.0, Text: "to"},
			{Start: 4.1, End: 5.0, Text: "be"},
			{Start: 5.1, End: 6.3, Text: "self-evident"}, // span 6.3s, at or over cueMaxSeconds
		},
		want: 4,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cueCut(tt.words); got != tt.want {
				t.Errorf("cueCut = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuilderClosesAndOrders(t *testing.T) {
	b := NewBuilder()
	words := []Word{
		{Start: 0.0, End: 0.7, Text: "First"},
		{Start: 0.8, End: 1.4, Text: "line."}, // span 1.4s ≥ cueMinSeconds
		{Start: 3.0, End: 3.7, Text: "Second"},
		{Start: 3.8, End: 4.4, Text: "line."}, // span 1.4s ≥ cueMinSeconds
	}
	// settledTo past the last word: both sentences have a closing signal.
	b.Commit(words, 5.0)

	cues := b.Cues()
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d: %v", len(cues), cues)
	}
	if cues[0].Start > cues[1].Start {
		t.Error("cues must be appended in non-decreasing start order")
	}
	if got := b.CueAt(3.2); got != "Second line." {
		t.Errorf("CueAt(3.2) = %q", got)
	}
	if got := b.CueAt(2.5); got != "" {
		t.Errorf("CueAt in the gap should be empty, got %q", got)
	}
}

func TestBuilderSilentTailClosesParagraphFinalCue(t *testing.T) {
	b := NewBuilder()
	sentence := []Word{
		{Start: 0.0, End: 0.7, Text: "The"},
		{Start: 0.8, End: 1.6, Text: "end"}, // no sentence punctuation
	}
	// Not yet settled past the words: nothing should close.
	b.Commit(sentence, 1.6)
	if n := len(b.Cues()); n != 0 {
		t.Fatalf("cue without a closing signal should stay open, got %d cues", n)
	}
	// Audio advances a full gap past the last word with nothing new: the
	// trailing silence is confirmed and the cue must close.
	b.Commit(nil, 1.6+cueGapSeconds)
	if n := len(b.Cues()); n != 1 {
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
		{"empty in, empty out", "", 42, ""},
		{"runs of whitespace collapse", "one   two\tthree", 42, "one two three"},
		// The wrap is greedy and breaks between words, never inside one: a word
		// split across two lines is unreadable at a distance, which is the only
		// distance a television is watched from.
		{"a tight width breaks between words", "alpha beta gamma", 10, "alpha beta\ngamma"},
		{"a word longer than the width is left whole", "supercalifragilistic", 10, "supercalifragilistic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wrap(tt.in, tt.width); got != tt.want {
				t.Errorf("Wrap(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}
