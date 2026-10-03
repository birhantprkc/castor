package chromecast

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	castmedia "github.com/vishen/go-chromecast/cast"

	"github.com/stupside/castor/services/apiserver/internal/device"
)

// ending is one Play's end: done closes once the receiver says how that playback ended.
type ending struct {
	once sync.Once
	done chan struct{}
	err  error
}

func newEnding() *ending { return &ending{done: make(chan struct{})} }

// AwaitEnd observes the Cast channel rather than polling it.
func (c *chromecastDevice) AwaitEnd(ctx context.Context) error {
	c.watchMu.Lock()
	e := c.ending
	c.watchMu.Unlock()
	select {
	case <-e.done:
		return e.err
	case <-c.ch.gone:
		// A status read before the connection dropped still decides the ending.
		select {
		case <-e.done:
			return e.err
		default:
		}
		return &device.Gone{Device: c.name, Observed: "the Cast connection closed while the cast was playing"}
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *chromecastDevice) watchMessage(payload []byte) {
	var response castmedia.MediaStatusResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return
	}
	c.watchMu.Lock()
	over, playErr := playbackOutcome(&c.watch, &response)
	if over {
		e := c.ending
		e.once.Do(func() {
			e.err = playErr
			close(e.done)
		})
	}
	c.watchMu.Unlock()
}

type chromecastPlayback struct {
	armed   bool
	content string
	session int
	active  bool
}

func (w *chromecastPlayback) begin(content string) {
	w.armed = true
	w.content = content
	w.session = 0
	w.active = false
}

func (w *chromecastPlayback) disarm() {
	w.armed = false
	w.content = ""
	w.session = 0
	w.active = false
}

func (w *chromecastPlayback) observe(status castmedia.Media) (bool, error) {
	if w.session == 0 {
		if status.Media.ContentId != w.content || status.MediaSessionId == 0 {
			return false, nil
		}
		w.session = status.MediaSessionId
	} else if status.MediaSessionId != w.session {
		return false, nil
	}

	switch status.PlayerState {
	case "BUFFERING", "PLAYING", "PAUSED":
		w.active = true
	case stateIdle:
		switch status.IdleReason {
		case idleError:
			if w.active {
				return true, fmt.Errorf("chromecast playback ended with receiver error")
			}
			return true, fmt.Errorf("chromecast refused the media: the receiver went idle with an error before playback began")
		case "FINISHED", "CANCELLED", "INTERRUPTED":
			return w.active, nil
		}
	}
	return false, nil
}

func playbackOutcome(w *chromecastPlayback, response *castmedia.MediaStatusResponse) (bool, error) {
	if !w.armed {
		return false, nil
	}
	switch response.Type {
	case msgClose:
		return w.active, nil
	case "LOAD_FAILED", "LOAD_CANCELLED":
		return true, fmt.Errorf("chromecast refused the media (%s)", response.Type)
	case msgMediaStatus:
		for _, status := range response.Status {
			if over, err := w.observe(status); over {
				return true, err
			}
		}
		return false, nil
	default:
		return false, nil
	}
}
