package extract

import (
	"context"
	"errors"
	"testing"
)

// countingActions returns n actions that record the order they ran in.
func countingActions(n int, ran *[]int) []action {
	actions := make([]action, n)
	for i := range n {
		actions[i] = action{name: "step", do: func() error {
			*ran = append(*ran, i)
			return nil
		}}
	}
	return actions
}

// This is the whole of stage 11 on the pipeline side: a collector holding only a media
// playlist has captured nothing worth stopping for, so every remaining action still
// gets its turn. Stopping here is what left a 4K cast with one rendition, 3840x1600 at
// 18505 kb/s, and no ladder to degrade to when the link delivered 0.39x realtime.
func TestActionPipelineRunsPastAChunklist(t *testing.T) {
	c := testCollector(t)
	captureWithBody(t, c, "https://cdn.example/hls/index-s2160p-v1-a1.m3u8", "req-1", mediaDocument)

	var ran []int
	actions := countingActions(4, &ran)
	got := runActions(context.Background(), c, actions, func(int) {})

	if got != len(actions) {
		t.Errorf("runActions ran %d of %d actions, want the list exhausted", got, len(actions))
	}
	if len(ran) != len(actions) {
		t.Errorf("actions run: %v, want all %d", ran, len(actions))
	}
}

// The early exit that survives: a master enumerates every variant, so there is nothing
// left for the remaining actions to win and holding a signed URL longer only ages it.
func TestActionPipelineStopsOnAMaster(t *testing.T) {
	c := testCollector(t)

	var ran []int
	// The first action is what leaks the master, so the pipeline must stop before the
	// second: the stop is checked ahead of every action, not only at the top.
	actions := []action{
		{"leak the master", func() error {
			captureWithBody(t, c, "https://cdn.example/hls/index.m3u8", "req-1", masterDocument)
			return nil
		}},
		{"must not run", func() error {
			ran = append(ran, 1)
			return nil
		}},
		{"must not run", func() error {
			ran = append(ran, 2)
			return nil
		}},
	}

	got := runActions(context.Background(), c, actions, func(int) {})

	if got != 1 {
		t.Errorf("runActions ran %d actions, want 1 before the master stopped it", got)
	}
	if len(ran) != 0 {
		t.Errorf("actions ran after a master was captured: %v", ran)
	}
}

// A failing action is best-effort: the page that needs a click is rarely the page that
// needs the iframe descent, so a step with nothing to do must not deny the next one its
// turn, and the turnstile bypass must still run behind a failed descent.
func TestAFailedActionDoesNotEndThePipeline(t *testing.T) {
	c := testCollector(t)

	var ran []int
	actions := []action{
		{"fails", func() error { return errors.New("no iframe here") }},
		{"runs anyway", func() error { ran = append(ran, 1); return nil }},
	}

	if got := runActions(context.Background(), c, actions, func(int) {}); got != 2 {
		t.Errorf("runActions ran %d actions, want 2", got)
	}
	if len(ran) != 1 {
		t.Errorf("the action after a failure did not run: %v", ran)
	}
}

// observe is the debug snapshot hook, and it must fire once per action that ran and not
// for the ones the early exit skipped: a snapshot of a step that never happened is
// worse than no snapshot when a real extraction is being diagnosed from .debug.
func TestPipelineObservesOnlyTheActionsThatRan(t *testing.T) {
	c := testCollector(t)

	var observed []int
	actions := []action{
		{"leak the master", func() error {
			captureWithBody(t, c, "https://cdn.example/hls/index.m3u8", "req-1", masterDocument)
			return nil
		}},
		{"must not run", func() error { return nil }},
	}

	runActions(context.Background(), c, actions, func(i int) { observed = append(observed, i) })

	if len(observed) != 1 || observed[0] != 0 {
		t.Errorf("observed %v, want only action 0", observed)
	}
}
