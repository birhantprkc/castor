package chromecast

import (
	"encoding/json"
	"net"
	"net/url"
	"testing"

	castmedia "github.com/vishen/go-chromecast/cast"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/device/devicetest"
)

func TestChromecastPlaybackState(t *testing.T) {
	const content = "https://castor.test/stream.mp4"
	status := func(session int, mediaID, playerState, idleReason string) castmedia.MediaStatusResponse {
		return castmedia.MediaStatusResponse{
			PayloadHeader: castmedia.PayloadHeader{Type: "MEDIA_STATUS"},
			Status: []castmedia.Media{{
				MediaSessionId: session,
				PlayerState:    playerState,
				IdleReason:     idleReason,
				Media:          castmedia.MediaItem{ContentId: mediaID},
			}},
		}
	}
	closed := castmedia.MediaStatusResponse{PayloadHeader: castmedia.PayloadHeader{Type: "CLOSE"}}
	for _, tt := range []struct {
		name     string
		messages []castmedia.MediaStatusResponse
		want     bool
		wantErr  bool
	}{
		{"stale finished status is ignored", []castmedia.MediaStatusResponse{status(4, "https://old.test/movie.mp4", "IDLE", "FINISHED")}, false, false},
		{"finish after playing ends", []castmedia.MediaStatusResponse{status(7, content, "PLAYING", ""), status(7, content, "IDLE", "FINISHED")}, true, false},
		{"another session cannot end this one", []castmedia.MediaStatusResponse{status(7, content, "PLAYING", ""), status(8, "https://other.test/movie.mp4", "IDLE", "FINISHED")}, false, false},
		{"pause remains active", []castmedia.MediaStatusResponse{status(7, content, "PLAYING", ""), status(7, content, "PAUSED", "")}, false, false},
		{"receiver close after playing ends", []castmedia.MediaStatusResponse{status(7, content, "PLAYING", ""), closed}, true, false},
		{"receiver close while still loading does not end", []castmedia.MediaStatusResponse{closed}, false, false},
		{"receiver playback error fails", []castmedia.MediaStatusResponse{status(7, content, "PLAYING", ""), status(7, content, "IDLE", "ERROR")}, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var watch chromecastPlayback
			watch.begin(content)
			got := false
			var gotErr error
			for i := range tt.messages {
				got, gotErr = playbackOutcome(&watch, &tt.messages[i])
			}
			if got != tt.want {
				t.Errorf("playbackOutcome = %v, want %v", got, tt.want)
			}
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("playbackOutcome error = %v, want error %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestChromecastAnswersWhenTheCastEnds(t *testing.T) {
	devicetest.AwaitsTheCastsEnd(t, func() device.Device { return &chromecastDevice{done: make(chan struct{})} })
}

// receiverAnswering is a Cast receiver on the other end of a pipe; loadAnswers says what it sends once LOAD arrives.
func receiverAnswering(t *testing.T, loadAnswers func(requestID int, content string) []any) *chromecastDevice {
	t.Helper()
	near, far := net.Pipe()
	dev := &chromecastDevice{done: make(chan struct{})}
	dev.ch = newChannel(near, dev.watchMessage)
	var receiver *channel
	receiver = newChannel(far, func(payload []byte) {
		var req struct {
			Type      string              `json:"type"`
			RequestID int                 `json:"requestId"`
			Media     castmedia.MediaItem `json:"media"`
		}
		if json.Unmarshal(payload, &req) != nil {
			return
		}
		var answers []any
		switch req.Type {
		case "GET_STATUS":
			status := castmedia.ReceiverStatusResponse{PayloadHeader: castmedia.PayloadHeader{Type: "RECEIVER_STATUS", RequestId: req.RequestID}}
			status.Status.Applications = []castmedia.Application{{AppId: defaultMediaReceiver, TransportId: "transport-1"}}
			answers = []any{status}
		case "LOAD":
			answers = loadAnswers(req.RequestID, req.Media.ContentId)
		}
		for _, a := range answers {
			if receiver.send(senderID, nsMedia, a) != nil {
				return
			}
		}
	})
	t.Cleanup(func() {
		_ = dev.Close()
		_ = receiver.Close()
	})
	return dev
}

func mediaStatus(requestID int, content, playerState, idleReason string) castmedia.MediaStatusResponse {
	return castmedia.MediaStatusResponse{
		PayloadHeader: castmedia.PayloadHeader{Type: "MEDIA_STATUS", RequestId: requestID},
		Status:        []castmedia.Media{{MediaSessionId: 1, PlayerState: playerState, IdleReason: idleReason, Media: castmedia.MediaItem{ContentId: content}}},
	}
}

func TestChromecastPlayReturnsTheReceiversVerdictOnTheLoad(t *testing.T) {
	stream := &url.URL{Scheme: "http", Host: "origin.test", Path: "/stream.m3u8"}
	for _, tt := range []struct {
		name    string
		answer  func(id int, content string) []any
		wantErr bool
	}{
		{"a buffering load is accepted", func(id int, content string) []any {
			return []any{mediaStatus(id, content, "BUFFERING", "")}
		}, false},
		{"a load failure fails the hand-off", func(id int, content string) []any {
			return []any{mediaStatus(0, content, "IDLE", "ERROR"), castmedia.PayloadHeader{Type: "LOAD_FAILED", RequestId: id}}
		}, true},
		{"an invalid request fails the hand-off", func(id int, _ string) []any {
			return []any{castmedia.PayloadHeader{Type: "INVALID_REQUEST", RequestId: id}}
		}, true},
		{"a status already idle on an error fails the hand-off", func(id int, content string) []any {
			return []any{mediaStatus(id, content, "IDLE", "ERROR")}
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dev := receiverAnswering(t, tt.answer)
			err := dev.Play(t.Context(), stream, "application/x-mpegURL")
			if (err != nil) != tt.wantErr {
				t.Errorf("Play = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestChromecastClosesWhileTheReceiverKeepsTalking(t *testing.T) {
	stream := &url.URL{Scheme: "http", Host: "origin.test", Path: "/stream.mp4"}
	for range 50 {
		dev := receiverAnswering(t, func(id int, content string) []any {
			answers := []any{mediaStatus(id, content, "BUFFERING", "")}
			for range 20 {
				answers = append(answers, mediaStatus(0, content, "PLAYING", ""))
			}
			return answers
		})
		if err := dev.Play(t.Context(), stream, "video/mp4"); err != nil {
			t.Fatalf("Play = %v", err)
		}
		if err := dev.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
}
