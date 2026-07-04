// Package cue is the subtitle presentation model. It turns a stream of
// committed, immutable timed words — whatever a speech backend produces — into
// display cues: grouping words into readable lines, coalescing staccato
// sentences, cutting at natural boundaries when a line would overrun, and
// trimming each cue's edges to hug the audio. It answers "what line is on
// screen at time t" via CueAt.
//
// It knows nothing about whisper, ffmpeg, or files: a backend feeds words in
// through Builder.Commit, and a renderer pulls lines out through CueAt. That
// keeps all the timing and line-shaping policy here, testable without the
// cgo-linked recognizer.
package cue

import (
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// Cue shaping: a cue closes at a silence gap of cueGapSeconds, after
	// sentence-final punctuation once it has been on screen at least
	// cueMinSeconds, or — when it would otherwise overrun the cueMaxChars or
	// cueMaxSeconds budget — at the most recent natural boundary so the line
	// never splits mid-phrase. The cueMinSeconds floor also coalesces staccato
	// one-word sentences ("Yeah." "OK.") that would each otherwise flash for a
	// few frames, which reads as a burst.
	cueGapSeconds = 1.0
	cueMinSeconds = 1.2
	cueMaxSeconds = 6.0
	cueMaxChars   = 84 // two 42-column broadcast lines

	// cuePauseSeconds is the inter-word gap that marks a soft phrase boundary:
	// long enough to read as a natural break, short of the cueGapSeconds
	// silence that ends a cue outright. A budget-forced cut falls back to one
	// of these (or a clause-final word) rather than splitting mid-phrase.
	cuePauseSeconds = 0.4

	// A recognizer's word timestamps run wide of the speech: onsets report
	// early and offsets late, so a cue appears before its first word is spoken
	// and lingers after the last. Pull each cue's edges inward by these margins
	// to hug the audio. They stay well inside the over-extension, and cues too
	// short to give up the trim keep their raw edges (minCueSpan), so speech is
	// never clipped to nothing.
	cueStartTrim = 0.15
	cueEndTrim   = 0.20
	minCueSpan   = 0.30
)

// Word is one committed, timed token with absolute timestamps in seconds. It
// is the unit a speech backend feeds into a Builder.
type Word struct {
	Start, End float64
	Text       string
}

// Cue is one subtitle line with absolute timestamps in seconds.
type Cue struct {
	Start, End float64
	Text       string
}

// Builder folds a stream of committed words into display cues. It is fed from
// the transcription goroutine via Commit/Close and read from the render
// goroutine via CueAt/Cues; the mutex guards the finished cues, which is the
// only state the two goroutines share. The zero value is ready to use.
type Builder struct {
	mu   sync.Mutex
	cues []Cue

	pending []Word // committed words not yet closed into a cue (Commit-only)
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder { return &Builder{} }

// Commit folds newly committed words into cues. settledTo is the time up to
// which the audio has been fully decided: a gap between the last pending word
// and settledTo is confirmed silence, which lets a paragraph-final cue close
// without waiting for a successor word that will never come.
func (b *Builder) Commit(words []Word, settledTo float64) {
	b.pending = append(b.pending, words...)
	silentTail := len(b.pending) > 0 &&
		settledTo-b.pending[len(b.pending)-1].End >= cueGapSeconds
	b.pending = b.closeCues(b.pending, silentTail)
}

// Close flushes every remaining word into cues. Call it once, after the final
// Commit, when no more words will arrive.
func (b *Builder) Close() {
	b.pending = b.closeCues(b.pending, true)
}

// CueAt returns the text of the cue covering time tSec, or "" if none does.
func (b *Builder) CueAt(tSec float64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Words commit in time order, so cues are start-ordered and overlap at
	// most by the few ms adjacent words share: only the last cue starting at
	// or before tSec can cover it.
	i := sort.Search(len(b.cues), func(i int) bool { return b.cues[i].Start > tSec })
	if i > 0 && b.cues[i-1].End > tSec {
		return b.cues[i-1].Text
	}
	return ""
}

// Cues returns a snapshot copy of the cues committed so far.
func (b *Builder) Cues() []Cue {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Cue(nil), b.cues...)
}

// closeCues folds committed words into display cues, appending every cue that
// has a closing signal. When final is set the remainder is flushed
// unconditionally. It returns the words still waiting to close.
func (b *Builder) closeCues(pending []Word, final bool) []Word {
	for len(pending) > 0 {
		cut := cueCut(pending)
		if cut == 0 {
			if !final {
				break
			}
			cut = len(pending)
		}
		b.appendCue(pending[:cut])
		pending = pending[cut:]
	}
	return pending
}

func (b *Builder) appendCue(words []Word) {
	var sb strings.Builder
	for i, w := range words {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(w.Text)
	}
	start, end := trimCueEdges(words[0].Start, words[len(words)-1].End)
	cue := Cue{Start: start, End: end, Text: sb.String()}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.cues = append(b.cues, cue)
}

// cueCut returns how many leading words of pending form a complete cue, or 0
// if no closing signal has arrived yet.
func cueCut(pending []Word) int {
	chars := 0
	lastBreak := 0 // words up to the most recent clause break or pause
	for i, w := range pending {
		if i > 0 && w.Start-pending[i-1].End >= cueGapSeconds {
			return i
		}
		chars += len(w.Text)
		if i > 0 {
			chars++ // joining space
		}
		span := w.End - pending[0].Start

		// Over budget: end the line at a natural boundary, not mid-phrase.
		if (chars > cueMaxChars || span >= cueMaxSeconds) && i > 0 {
			if chars <= cueMaxChars && clauseEnd(w.Text) {
				return i + 1
			}
			// Fall back to the last boundary already passed; failing that,
			// split at the limit — the char cap before the overflowing word,
			// the duration cap after the word that tripped it.
			if lastBreak > 0 {
				return lastBreak
			}
			if chars > cueMaxChars {
				return i
			}
			return i + 1
		}

		// Hold a sentence open until it has enough duration to read, so runs
		// of short sentences coalesce into one cue instead of flashing past.
		if SentenceEnd(w.Text) && span >= cueMinSeconds {
			return i + 1
		}

		// Remember natural boundaries to fall back on if a later word forces a
		// cut: a clause-final word, or a word trailed by a readable pause.
		if clauseEnd(w.Text) ||
			(i+1 < len(pending) && pending[i+1].Start-w.End >= cuePauseSeconds) {
			lastBreak = i + 1
		}
	}
	return 0
}

// trimCueEdges pulls a cue's timestamps inward to counter a recognizer's habit
// of over-reporting word spans (early onsets, late offsets). A cue too short
// to give up cueStartTrim+cueEndTrim and still leave minCueSpan keeps its raw
// edges, so a brief utterance is never trimmed into nothing.
func trimCueEdges(start, end float64) (float64, float64) {
	if end-start < cueStartTrim+cueEndTrim+minCueSpan {
		return start, end
	}
	return start + cueStartTrim, end - cueEndTrim
}

// SentenceEnd reports whether a word closes a sentence, ignoring trailing
// quotes and brackets.
func SentenceEnd(s string) bool {
	s = strings.TrimRight(s, `"')]`+"”’")
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".?!…", r)
}

// clauseEnd reports whether a word ends a clause — sentence-final punctuation
// or a comma, semicolon, colon, or dash — ignoring trailing quotes and
// brackets. It marks the soft break points a budget-forced cue cut prefers.
func clauseEnd(s string) bool {
	s = strings.TrimRight(s, `"')]`+"”’")
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".?!…,;:—", r)
}

// Wrap normalizes whitespace and greedily wraps text at width columns so a
// renderer draws broadcast-style line breaks. Words longer than width are
// emitted as-is on their own line.
func Wrap(text string, width int) string {
	// FieldsSeq iterates the whitespace-split words without allocating the
	// intermediate slice; empty input simply yields no iterations, so the
	// builder stays empty and we return "".
	var b strings.Builder
	lineLen := 0
	for w := range strings.FieldsSeq(text) {
		switch {
		case lineLen == 0:
			b.WriteString(w)
			lineLen = len(w)
		case lineLen+1+len(w) <= width:
			b.WriteByte(' ')
			b.WriteString(w)
			lineLen += 1 + len(w)
		default:
			b.WriteByte('\n')
			b.WriteString(w)
			lineLen = len(w)
		}
	}
	return b.String()
}
