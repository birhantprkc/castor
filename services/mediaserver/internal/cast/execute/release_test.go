package execute

import (
	"errors"
	"slices"
	"testing"
)

// Releasing runs last acquired first, including whatever is acquired while it runs, and reports every failure.
func TestReleasesRunLastAcquiredFirstAndReportEveryFailure(t *testing.T) {
	var order []string
	first, last := errors.New("first failed"), errors.New("last failed")
	var r releases
	r.push(func() error { order = append(order, "workspace"); return first })
	r.push(func() error {
		order = append(order, "read")
		// A device connected beside the pipeline can arrive while the attempt is being released.
		r.push(func() error { order = append(order, "device"); return nil })
		return nil
	})
	r.push(func() error { order = append(order, "encoder"); return last })

	err := r.release()
	if want := []string{"encoder", "read", "device", "workspace"}; !slices.Equal(order, want) {
		t.Errorf("released %v, want %v", order, want)
	}
	if !errors.Is(err, first) || !errors.Is(err, last) {
		t.Errorf("release = %v, want both failures", err)
	}
	if err := r.release(); err != nil || len(order) != 4 {
		t.Errorf("a second release ran something again (%v, %v)", err, order)
	}
}
