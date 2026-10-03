package health

import (
	"fmt"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Undelivered is a delivery the device did not take most of.
type Undelivered struct {
	Handed   time.Duration
	Produced time.Duration
}

// Error states the shortfall in words matching the numbers.
func (u *Undelivered) Error() string {
	if u.Handed <= 0 {
		return fmt.Sprintf("the device was handed none of the %s this cast produced: it accepted the stream URL and never came for a byte of the program, all of which reached nobody",
			u.Produced.Round(time.Second))
	}
	return fmt.Sprintf("the device was handed %s of the %s this cast produced: it stopped taking the stream while castor was still serving it, so most of the program (%s) reached nobody",
		u.Handed.Round(time.Second), u.Produced.Round(time.Second), (u.Produced - u.Handed).Round(time.Second))
}

// handedAtLeast is the share of what a delivery made that the device must take for it to count as delivered.
const handedAtLeast = 0.5

// Shortfall states whether the device took what this delivery made (bytes-based, not wall-clock).
func Shortfall(sent int64, made media.Progress) error {
	if made.Bytes <= 0 || made.Position <= 0 {
		// Nothing produced: artifact gate and encoder status to report; never blame the device here.
		return nil
	}
	share := float64(sent) / float64(made.Bytes)
	if share >= handedAtLeast {
		return nil
	}
	return &Undelivered{Handed: time.Duration(float64(made.Position) * share), Produced: made.Position}
}

// NoneFetched states whether the device fetched anything of what a segmented delivery made.
func NoneFetched(served int, made media.Progress) error {
	if made.Position <= 0 || served > 0 {
		return nil
	}
	return &Undelivered{Produced: made.Position}
}
