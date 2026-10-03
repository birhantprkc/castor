package latest

import "testing"

func TestAReaderIsToldOfTheNextValueAndOnlyOfAPublishedOne(t *testing.T) {
	v := New(1)
	got, changed := v.Load()
	if got != 1 {
		t.Fatalf("loaded %d, want 1", got)
	}
	v.Update(func(n int) (int, bool) { return n + 1, false })
	select {
	case <-changed:
		t.Fatal("a declined change woke the reader")
	default:
	}
	v.Update(func(n int) (int, bool) { return n + 1, true })
	<-changed
	if got, _ := v.Load(); got != 2 {
		t.Fatalf("loaded %d after the change, want 2", got)
	}
}
