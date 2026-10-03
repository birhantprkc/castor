package castlog

import (
	"log/slog"
	"sync"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/services/mediaserver/internal/wire"
)

// buffer is how many lines a watcher may fall behind before the newest are dropped.
const buffer = 256

// Feed sends a cast's log lines, live, to the watchers that asked for them; it keeps none.
type Feed struct {
	mu       sync.Mutex
	watchers map[chan *castorv1.WatchResponse]slog.Level
}

func NewFeed() *Feed { return &Feed{watchers: map[chan *castorv1.WatchResponse]slog.Level{}} }

// Subscribe opens a line feed from level on, or none (a nil feed) when the watcher asked for none.
func (f *Feed) Subscribe(level *castorv1.LogLevel) chan *castorv1.WatchResponse {
	if level == nil {
		return nil
	}
	lines := make(chan *castorv1.WatchResponse, buffer)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchers[lines] = wire.FromLevel(*level)
	return lines
}

func (f *Feed) Unsubscribe(lines chan *castorv1.WatchResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.watchers, lines)
}

func (f *Feed) wants(level slog.Level) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, least := range f.watchers {
		if level >= least {
			return true
		}
	}
	return false
}

// publish never waits on a watcher: one that is behind loses the line, the cast does not stall.
func (f *Feed) publish(r slog.Record, attrs []slog.Attr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var line *castorv1.WatchResponse
	for lines, least := range f.watchers {
		if r.Level < least {
			continue
		}
		if line == nil {
			line = &castorv1.WatchResponse{Update: &castorv1.WatchResponse_Line{Line: wire.Line(r, attrs)}}
		}
		select {
		case lines <- line:
		default:
		}
	}
}
