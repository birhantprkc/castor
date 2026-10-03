package registry

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestAValueLingersAfterItIsDoneThenIsForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New[int](time.Minute)
		done := make(chan struct{})
		if !r.Add("a", 1, done) {
			t.Fatal("an open registry refused a value")
		}
		close(done)
		synctest.Sleep(59 * time.Second)
		if _, ok := r.Find("a"); !ok {
			t.Fatal("a done value was forgotten before it lingered")
		}
		synctest.Sleep(2 * time.Second)
		if _, ok := r.Find("a"); ok {
			t.Fatal("a done value was kept past its linger")
		}
	})
}

func TestADrainingRegistryRefusesValuesAndWaitsForThoseRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New[int](time.Minute)
		done := make(chan struct{})
		r.Add("a", 1, done)
		drained := make(chan struct{})
		go func() {
			r.Drain(t.Context())
			close(drained)
		}()
		synctest.Wait()
		if r.Add("b", 2, make(chan struct{})) {
			t.Fatal("a draining registry took a value")
		}
		select {
		case <-drained:
			t.Fatal("the drain returned while a value still ran")
		default:
		}
		close(done)
		synctest.Wait()
		select {
		case <-drained:
		default:
			t.Fatal("the drain did not return once every value was done")
		}
	})
}
