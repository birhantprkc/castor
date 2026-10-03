package subtitle

import (
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	cueGapSeconds = 1.0
	cueMinSeconds = 1.2
	cueMaxSeconds = 6.0
	cueMaxChars   = 84 // two 42-column broadcast lines

	cuePauseSeconds = 0.4

	cueStartTrim = 0.15
	cueEndTrim   = 0.20
	minCueSpan   = 0.30
)

// Word is one committed, timed token with absolute timestamps in seconds.
type Word struct {
	Start, End float64
	Text       string
}

// cue is one subtitle line with absolute timestamps in seconds.
type cue struct {
	start, end float64
	text       string
}

// Cues folds a stream of committed words into display cues.
type Cues struct {
	mu   sync.Mutex
	cues []cue

	pending []Word // committed words not yet closed into a cue (Commit-only)
}

// Commit folds newly committed words into cues.
func (c *Cues) Commit(words []Word, settledTo float64) {
	c.pending = append(c.pending, words...)
	silentTail := len(c.pending) > 0 &&
		settledTo-c.pending[len(c.pending)-1].End >= cueGapSeconds
	c.pending = c.closeCues(c.pending, silentTail)
}

// Close flushes every remaining word into cues. Call it once, after the final Commit.
func (c *Cues) Close() {
	c.pending = c.closeCues(c.pending, true)
}

// cueAt returns the text of the cue covering time tSec, or "" if none does.
func (c *Cues) cueAt(tSec float64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, _ := slices.BinarySearchFunc(c.cues, tSec, func(entry cue, t float64) int {
		if entry.start > t {
			return 1
		}
		return -1
	})
	if i > 0 && c.cues[i-1].end > tSec {
		return c.cues[i-1].text
	}
	return ""
}

func (c *Cues) closeCues(pending []Word, final bool) []Word {
	for len(pending) > 0 {
		cut := cueCut(pending)
		if cut == 0 {
			if !final {
				break
			}
			cut = len(pending)
		}
		c.appendCue(pending[:cut])
		pending = pending[cut:]
	}
	return pending
}

func (c *Cues) appendCue(words []Word) {
	var sb strings.Builder
	for i, w := range words {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(w.Text)
	}
	start, end := trimCueEdges(words[0].Start, words[len(words)-1].End)
	cue := cue{start: start, end: end, text: sb.String()}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cues = append(c.cues, cue)
}

func cueCut(pending []Word) int {
	chars := 0
	lastBreak := 0 // words up to the most recent clause break or pause
	for i, w := range pending {
		if i > 0 && w.Start-pending[i-1].End >= cueGapSeconds {
			return i
		}
		chars += utf8.RuneCountInString(w.Text)
		if i > 0 {
			chars++ // joining space
		}
		span := w.End - pending[0].Start

		// Over budget: end the line at a natural boundary, not mid-phrase.
		if (chars > cueMaxChars || span >= cueMaxSeconds) && i > 0 {
			if chars <= cueMaxChars && clauseEnd(w.Text) {
				return i + 1
			}
			if lastBreak > 0 {
				return lastBreak
			}
			if chars > cueMaxChars {
				return i
			}
			return i + 1
		}

		if sentenceEnd(w.Text) && span >= cueMinSeconds {
			return i + 1
		}

		// Remember natural boundaries to fall back on if a later word forces a cut.
		if clauseEnd(w.Text) ||
			(i+1 < len(pending) && pending[i+1].Start-w.End >= cuePauseSeconds) {
			lastBreak = i + 1
		}
	}
	return 0
}

// trimCueEdges counters a recognizer's habit of over-reporting word spans.
func trimCueEdges(start, end float64) (float64, float64) {
	if end-start < cueStartTrim+cueEndTrim+minCueSpan {
		return start, end
	}
	return start + cueStartTrim, end - cueEndTrim
}

// sentenceEnd reports whether a word closes a sentence, ignoring trailing quotes and brackets.
func sentenceEnd(s string) bool {
	s = strings.TrimRight(s, `"')]`+"”’")
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".?!…", r)
}

// clauseEnd reports whether a word ends a clause, ignoring trailing quotes and brackets.
func clauseEnd(s string) bool {
	s = strings.TrimRight(s, `"')]`+"”’")
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".?!…,;:—", r)
}
