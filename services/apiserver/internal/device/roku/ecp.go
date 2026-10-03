package roku

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/apiserver/internal/device"
)

type rokuDevice struct {
	ecp   *url.URL
	appID string
	name  string
	hc    *http.Client
}

var _ device.Device = (*rokuDevice)(nil)

// get reads at most limit bytes of an ECP answer, refusing any status but 200.
func get(ctx context.Context, hc *http.Client, u *url.URL, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u.Path, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (r *rokuDevice) Play(ctx context.Context, streamURL *url.URL, container mediav1.Container) error {
	q := url.Values{}
	q.Set(rokuChannelParamURL, streamURL.String())
	q.Set(rokuChannelParamFormat, streamFormatFor(container))

	u := r.ecp.JoinPath("launch", r.appID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("launching roku channel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("roku launch: %s (channel %q installed?)", resp.Status, r.appID)
	}
	return nil
}

const rokuMediaPlayerQuery = "/query/media-player"

// AwaitEnd answers exactly ONE of the two things an ECP poll could establish.
func (r *rokuDevice) AwaitEnd(ctx context.Context) error {
	return device.AwaitPolledEnd(ctx, r.name, rokuMediaPlayerQuery, r.mediaPlayerAnswered)
}

// mediaPlayerAnswered reports only that somebody answered, never that playback is over (see AwaitEnd).
func (r *rokuDevice) mediaPlayerAnswered(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.ecp.JoinPath(rokuMediaPlayerQuery).String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return false, nil
}

func streamFormatFor(container mediav1.Container) string {
	switch container {
	case mediav1.Container_CONTAINER_MP4:
		return "mp4"
	case mediav1.Container_CONTAINER_MKV:
		return "mkv"
	default:
		return "hls"
	}
}

// Capabilities describes what Castor's channel plays.
func (r *rokuDevice) Capabilities() *mediav1.Capabilities {
	return &mediav1.Capabilities{
		SelfFetch:       true,
		Containers:      []mediav1.Container{mediav1.Container_CONTAINER_HLS, mediav1.Container_CONTAINER_MP4, mediav1.Container_CONTAINER_MKV},
		ServedContainer: mediav1.Container_CONTAINER_HLS,
		Video:           []*mediav1.VideoSupport{device.VideoSupport(mediav1.Codec_CODEC_H264)},
		Audio: []*mediav1.AudioSupport{
			{Codec: mediav1.Codec_CODEC_AAC, MaxChannels: 6},
			{Codec: mediav1.Codec_CODEC_AC3},
			{Codec: mediav1.Codec_CODEC_EAC3},
		},
	}
}

func (r *rokuDevice) Close() error { return nil }
