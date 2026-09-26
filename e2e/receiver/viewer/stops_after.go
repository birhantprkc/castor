package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/receiver"
)

// StopsAfter builds a viewer who stops playback a while after it began, as in `stops-after: 3s`.
type StopsAfter struct{}

func (StopsAfter) Name() string { return "stops-after" }

func (StopsAfter) Build(settings yaml.Node) (receiver.Viewer, error) {
	var after time.Duration
	if err := settings.Decode(&after); err != nil || after <= 0 {
		return nil, fmt.Errorf("stops-after: want a positive duration such as 3s (%v)", err)
	}
	return stopper{after: after}, nil
}

// viewingRate is how fast a TV drains its buffer, slow enough that the stop lands mid-stream.
const viewingRate = 256 << 10

type stopper struct{ after time.Duration }

func (stopper) Name() string { return "stops-after" }

func (s stopper) Watch(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.after)
}

func (stopper) Stopped(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func (stopper) WillStop() bool { return true }

func (stopper) Pace(r io.Reader) io.Reader { return &paced{r: r, start: time.Now()} }

func (stopper) Realtime() bool { return true }

// paced holds reads to viewingRate on average since the first.
type paced struct {
	r     io.Reader
	start time.Time
	read  int64
}

func (p *paced) Read(b []byte) (int, error) {
	n, err := p.r.Read(b[:min(len(b), 16<<10)])
	p.read += int64(n)
	due := p.start.Add(time.Duration(p.read) * time.Second / viewingRate)
	time.Sleep(time.Until(due))
	return n, err
}
