package cue

import "testing"

func TestCueCut(t *testing.T) {
	// A sentence long enough to read closes on its final punctuation.
	sentence := []Word{
		{Start: 0.0, End: 0.6, Text: "Ask"},
		{Start: 0.7, End: 1.3, Text: "not."}, // span 1.3s ≥ cueMinSeconds
		{Start: 1.4, End: 1.7, Text: "What"},
	}
	if cut := cueCut(sentence); cut != 2 {
		t.Errorf("a readable sentence should close after its period, got %d", cut)
	}

	// A staccato sentence under the minimum stays open so it coalesces with
	// what follows instead of flashing on its own.
	staccato := []Word{
		{Start: 0.0, End: 0.2, Text: "OK."},
		{Start: 0.3, End: 0.6, Text: "So"},
	}
	if cut := cueCut(staccato); cut != 0 {
		t.Errorf("a sub-minimum sentence should stay open to coalesce, got %d", cut)
	}

	gap := []Word{
		{Start: 0.0, End: 0.3, Text: "before"},
		{Start: 2.0, End: 2.3, Text: "after"},
	}
	if cut := cueCut(gap); cut != 1 {
		t.Errorf("a silence gap should close before it, got %d", cut)
	}

	open := []Word{{Start: 0.0, End: 0.3, Text: "still"}, {Start: 0.4, End: 0.7, Text: "going"}}
	if cut := cueCut(open); cut != 0 {
		t.Errorf("no closing signal should leave the cue open, got %d", cut)
	}
}

func TestCueCutPrefersNaturalBreaks(t *testing.T) {
	// The duration cap trips at the last word, but a pause after "simple"
	// (gap 0.6s) is a better cut than slicing the phrase that follows it.
	pause := []Word{
		{Start: 0.0, End: 0.5, Text: "keep"},
		{Start: 0.6, End: 1.1, Text: "it"},
		{Start: 1.2, End: 2.0, Text: "simple"}, // 0.6s pause before the next word
		{Start: 2.6, End: 3.5, Text: "and"},
		{Start: 3.6, End: 4.5, Text: "keep"},
		{Start: 4.6, End: 6.2, Text: "going"}, // span 6.2s ≥ cueMaxSeconds
	}
	if cut := cueCut(pause); cut != 3 {
		t.Errorf("forced cut should fall back to the pause after 3 words, got %d", cut)
	}

	// Same, but the natural boundary is a comma rather than a silence.
	comma := []Word{
		{Start: 0.0, End: 0.6, Text: "we"},
		{Start: 0.7, End: 1.3, Text: "hold"},
		{Start: 1.4, End: 2.0, Text: "these"},
		{Start: 2.1, End: 3.0, Text: "truths,"},
		{Start: 3.1, End: 4.0, Text: "to"},
		{Start: 4.1, End: 5.0, Text: "be"},
		{Start: 5.1, End: 6.3, Text: "self-evident"}, // span 6.3s ≥ cueMaxSeconds
	}
	if cut := cueCut(comma); cut != 4 {
		t.Errorf("forced cut should fall back to the comma after 4 words, got %d", cut)
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
	if got := Wrap("", 42); got != "" {
		t.Errorf("empty input should wrap to empty, got %q", got)
	}
	if got := Wrap("one   two\tthree", 42); got != "one two three" {
		t.Errorf("whitespace should collapse, got %q", got)
	}
	// Greedy wrap at a tight width breaks between words, never inside one.
	if got := Wrap("alpha beta gamma", 10); got != "alpha beta\ngamma" {
		t.Errorf("Wrap = %q", got)
	}
}
