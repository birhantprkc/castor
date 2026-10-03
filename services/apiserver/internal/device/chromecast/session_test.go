package chromecast

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	castmedia "github.com/vishen/go-chromecast/cast"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/apiserver/internal/device"
)

// receiverAnswering is a Cast receiver on the other end of a pipe; loadAnswers says what it sends once LOAD arrives.
func receiverAnswering(t *testing.T, loadAnswers func(requestID int, load castmedia.MediaItem) []any) *session {
	t.Helper()
	return receiverSaying(t, func(requestID int) any {
		status := castmedia.ReceiverStatusResponse{Type: "RECEIVER_STATUS", RequestId: requestID}
		status.Status.Applications = []castmedia.Application{{AppId: defaultMediaReceiver, TransportId: "transport-1"}}
		return status
	}, loadAnswers)
}

// receiverSaying is receiverAnswering answering GET_STATUS with status.
func receiverSaying(t *testing.T, status func(requestID int) any, loadAnswers func(requestID int, load castmedia.MediaItem) []any) *session {
	t.Helper()
	near, far := net.Pipe()
	dev := &session{ending: newEnding()}
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
			answers = []any{status(req.RequestID)}
		case "LOAD":
			answers = loadAnswers(req.RequestID, req.Media)
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
		Type:      "MEDIA_STATUS",
		RequestId: requestID,
		Status:    []castmedia.Media{{MediaSessionId: 1, PlayerState: playerState, IdleReason: idleReason, Media: castmedia.MediaItem{ContentId: content}}},
	}
}

// A device's own answers carry fields and spellings go-chromecast's structs never produce, so they are read as captured.
func TestADevicesAnswersAreReadAsItSendsThem(t *testing.T) {
	stream := &url.URL{Scheme: "http", Host: "media.test", Path: "/stream.mp4", RawQuery: "a=1&b=2"}
	dev := receiverSaying(t, func(id int) any {
		return jsontext.Value(fmt.Sprintf(`{"requestId":%d,"status":{"applications":[{"appId":"CC1AD845","appType":"WEB","displayName":"Default Media Receiver","iconUrl":"","isIdleScreen":false,"launchedFromCloud":false,"namespaces":[{"name":"urn:x-cast:com.google.cast.media"}],"sessionId":"7E2FF513","statusText":"Default Media Receiver","transportId":"7E2FF513","universalAppId":"CC1AD845"}],"userEq":{},"volume":{"controlType":"attenuation","level":1.0,"muted":false,"stepInterval":0.05000000074505806}},"type":"RECEIVER_STATUS"}`, id))
	}, func(id int, load castmedia.MediaItem) []any {
		return []any{
			jsontext.Value(fmt.Sprintf(`{"type":"MEDIA_STATUS","status":[{"mediaSessionId":1,"playbackRate":1,"playerState":"BUFFERING","currentTime":0,"supportedMediaCommands":12303,"volume":{"level":1,"muted":false},"activeTrackIds":[],"media":{"contentId":%q,"streamType":"BUFFERED","contentType":"video/mp4","mediaCategory":"VIDEO","duration":596.474195,"tracks":[{"trackId":1,"type":"VIDEO"}]},"currentItemId":1,"repeatMode":"REPEAT_OFF"}],"requestId":%d}`, load.ContentId, id)),
			jsontext.Value(`{"type":"MEDIA_STATUS","status":[{"mediaSessionId":1,"playbackRate":1,"playerState":"PLAYING","currentTime":1.5,"supportedMediaCommands":12303,"volume":{"level":1,"muted":false},"currentItemId":1,"repeatMode":"REPEAT_OFF"}],"requestId":0}`),
			jsontext.Value(`{"type":"MEDIA_STATUS","status":[{"mediaSessionId":1,"playbackRate":1,"playerState":"IDLE","currentTime":596.4,"supportedMediaCommands":12303,"volume":{"level":1,"muted":false},"currentItemId":1,"idleReason":"FINISHED","extendedStatus":null}],"requestId":0}`),
		}
	})
	if err := dev.Play(t.Context(), stream, mediav1.Container_CONTAINER_MP4); err != nil {
		t.Fatal(err)
	}
	if err := dev.AwaitEnd(t.Context()); err != nil {
		t.Errorf("AwaitEnd = %v, want the device's finish read as a clean end", err)
	}
}

func TestChromecastPlayReturnsTheReceiversVerdictOnTheLoad(t *testing.T) {
	stream := &url.URL{Scheme: "http", Host: "origin.test", Path: "/stream.m3u8"}
	for _, tt := range []struct {
		name    string
		answer  func(id int, load castmedia.MediaItem) []any
		wantErr bool
	}{
		{"a buffering load is accepted", func(id int, load castmedia.MediaItem) []any {
			return []any{mediaStatus(id, load.ContentId, "BUFFERING", "")}
		}, false},
		{"a load failure fails the hand-off", func(id int, load castmedia.MediaItem) []any {
			return []any{mediaStatus(0, load.ContentId, "IDLE", "ERROR"), castmedia.PayloadHeader{Type: "LOAD_FAILED", RequestId: id}}
		}, true},
		{"an invalid request fails the hand-off", func(id int, _ castmedia.MediaItem) []any {
			return []any{castmedia.PayloadHeader{Type: "INVALID_REQUEST", RequestId: id}}
		}, true},
		{"a status already idle on an error fails the hand-off", func(id int, load castmedia.MediaItem) []any {
			return []any{mediaStatus(id, load.ContentId, "IDLE", "ERROR")}
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dev := receiverAnswering(t, tt.answer)
			err := dev.Play(t.Context(), stream, mediav1.Container_CONTAINER_HLS)
			if (err != nil) != tt.wantErr {
				t.Errorf("Play = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestALoadNamesItsContainerAsAReceiverKnowsIt(t *testing.T) {
	loaded := make(chan string, 1)
	dev := receiverAnswering(t, func(id int, load castmedia.MediaItem) []any {
		loaded <- load.ContentType
		return []any{mediaStatus(id, load.ContentId, "BUFFERING", "")}
	})
	if err := dev.Play(t.Context(), &url.URL{Scheme: "http", Host: "media.test", Path: "/stream.m3u8"}, mediav1.Container_CONTAINER_HLS); err != nil {
		t.Fatal(err)
	}
	if got := <-loaded; got != "application/x-mpegURL" {
		t.Errorf("the receiver was asked to load %q, want the HLS MIME type", got)
	}
}

func TestAPlayRetriedAfterARefusedOneIsAwaitedToItsOwnEnd(t *testing.T) {
	refused := &url.URL{Scheme: "http", Host: "media.test", Path: "/attempt-1.mp4"}
	retried := &url.URL{Scheme: "http", Host: "media.test", Path: "/attempt-2.mp4"}
	dev := receiverAnswering(t, func(id int, load castmedia.MediaItem) []any {
		if load.ContentId == refused.String() {
			return []any{mediaStatus(0, load.ContentId, "IDLE", "ERROR"), castmedia.PayloadHeader{Type: "LOAD_FAILED", RequestId: id}}
		}
		return []any{mediaStatus(id, load.ContentId, "PLAYING", ""), mediaStatus(0, load.ContentId, "IDLE", "FINISHED")}
	})
	if err := dev.Play(t.Context(), refused, mediav1.Container_CONTAINER_MP4); err == nil {
		t.Fatal("the receiver refused the first load, yet Play accepted it")
	}
	if err := dev.Play(t.Context(), retried, mediav1.Container_CONTAINER_MP4); err != nil {
		t.Fatal(err)
	}
	if err := dev.AwaitEnd(t.Context()); err != nil {
		t.Errorf("AwaitEnd = %v, want the retried playback's clean finish rather than the refused one's error", err)
	}
}

func TestAPlayAbandonedForANewerOneLeavesTheNewerOneWatched(t *testing.T) {
	abandoned := &url.URL{Scheme: "http", Host: "media.test", Path: "/attempt-1.mp4"}
	current := &url.URL{Scheme: "http", Host: "media.test", Path: "/attempt-2.mp4"}
	loading := make(chan struct{})
	dev := receiverAnswering(t, func(id int, load castmedia.MediaItem) []any {
		if load.ContentId == abandoned.String() {
			close(loading)
			return nil
		}
		return []any{mediaStatus(id, load.ContentId, "BUFFERING", "")}
	})
	ctx, abandon := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- dev.Play(ctx, abandoned, mediav1.Container_CONTAINER_MP4) }()
	<-loading
	if err := dev.Play(t.Context(), current, mediav1.Container_CONTAINER_MP4); err != nil {
		t.Fatal(err)
	}
	abandon()
	if err := <-first; err == nil {
		t.Fatal("the abandoned Play reported the load it never heard back about as accepted")
	}
	dev.watchMu.Lock()
	defer dev.watchMu.Unlock()
	if !dev.watch.armed || dev.watch.content != current.String() {
		t.Errorf("after the abandoned Play failed the watch is %+v, want it still on the newer Play's media", dev.watch)
	}
}

func TestAPlayOnADroppedConnectionIsGone(t *testing.T) {
	near, far := net.Pipe()
	dev := &session{name: "Living Room", ending: newEnding()}
	dev.ch = newChannel(near, dev.watchMessage)
	t.Cleanup(func() { _ = dev.Close() })
	_ = far.Close()
	<-dev.ch.gone
	if _, gone := errors.AsType[*device.Gone](dev.Play(t.Context(), &url.URL{Scheme: "http", Host: "media.test"}, mediav1.Container_CONTAINER_MP4)); !gone {
		t.Error("a Play on a connection the receiver dropped is not reported gone, so its lender never connects again")
	}
}

func TestChromecastClosesWhileTheReceiverKeepsTalking(t *testing.T) {
	stream := &url.URL{Scheme: "http", Host: "origin.test", Path: "/stream.mp4"}
	for range 50 {
		dev := receiverAnswering(t, func(id int, load castmedia.MediaItem) []any {
			answers := []any{mediaStatus(id, load.ContentId, "BUFFERING", "")}
			for range 20 {
				answers = append(answers, mediaStatus(0, load.ContentId, "PLAYING", ""))
			}
			return answers
		})
		if err := dev.Play(t.Context(), stream, mediav1.Container_CONTAINER_MP4); err != nil {
			t.Fatalf("Play = %v", err)
		}
		if err := dev.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
}
