package media

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

func TestFormatForContentType(t *testing.T) {
	cases := []struct {
		ct       string
		muxer    string
		ext      string
		delivery DeliveryKind
		ok       bool
	}{
		{MPEGTS, "mpegts", ".ts", DeliverStream, true},
		{MP4, "mp4", ".mp4", DeliverStream, true},
		{HLS, "hls", ".m3u8", DeliverSegmented, true},
		{WebM, "", "", 0, false}, // a container castor advertises but cannot produce
		{"", "", "", 0, false},   // inert pass-through output type must not resolve
		{"bogus", "", "", 0, false},
	}
	for _, c := range cases {
		f, ok := FormatForContentType(c.ct)
		if ok != c.ok || f.Muxer != c.muxer || f.Extension != c.ext || (ok && f.Delivery != c.delivery) {
			t.Errorf("FormatForContentType(%q) = (%+v, %v), want muxer=%q ext=%q delivery=%v ok=%v", c.ct, f, ok, c.muxer, c.ext, c.delivery, c.ok)
		}
	}
}

// TestEveryFormatDeclaresItsFraming guards the zero value that would otherwise be
// free. Framing is the input to the one decision whose wrong answer is either
// fatal or silently destructive (a repack pushed at an in-band container exits 0
// and leaves 8 of 189 audio packets), and FramingUnknown exists so a row that
// forgot to answer cannot answer "in band" by accident.
func TestEveryFormatDeclaresItsFraming(t *testing.T) {
	for f := range ProducibleFormats() {
		if f.Framing == FramingUnknown {
			t.Errorf("format %q declares no framing; every producible container must say how it frames its streams", f.ContentType)
		}
	}
}

// TestFormatToContentTypeAcceptsWhatCastorMuxes covers the containers a probe
// really reports. An unrecognised one is not an error: refusing here used to
// abort the whole cast at resolution, so a raw MPEG-TS stream (ffprobe
// format_name "mpegts") could not be cast at all even though castor muxes
// MPEG-TS itself.
func TestFormatToContentTypeAcceptsWhatCastorMuxes(t *testing.T) {
	cases := map[string]string{
		"mpegts":        MPEGTS,
		"hls,applehttp": HLS,
		"applehttp":     HLS,
		// ffprobe reports this joined list for a plain .mp4 AND for a genuine .mov,
		// with "mov" first, which is exactly why there is no mov row: adding one
		// would reclassify every mp4 source as video/quicktime.
		"mov,mp4,m4a,3gp,3g2,mj2": MP4,
		"matroska,webm":           MKV,
		"avi":                     AVI,
		"flv":                     FLV,
		"":                        "",
		"nut":                     "",
		"some_future_demuxer":     "",
	}
	for format, want := range cases {
		if got := FormatToContentType(format); got != want {
			t.Errorf("FormatToContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestStreamSelfFetchable(t *testing.T) {
	audio := &url.URL{Path: "/audio.m3u8"}
	cases := []struct {
		name   string
		stream Stream
		want   bool
	}{
		{"no headers: the URL is all a renderer needs", Stream{}, true},
		{"empty header set is still self-sufficient", Stream{Headers: http.Header{}}, true},
		{"header-gated: a renderer is handed none of these", Stream{Headers: http.Header{"Referer": {"https://player.example/"}}}, false},
		{"any captured header counts", Stream{Headers: http.Header{"User-Agent": {"x"}}}, false},
		// One URL is one rendition: hand that over and the device plays the
		// picture in silence.
		{"demuxed: the program does not fit in one URL", Stream{AudioURL: audio}, false},
		// A renderer applies its own checks, and refuses what castor had to
		// relax one to read.
		{"lenient-only: no other reader will open it", Stream{NeedsLeniency: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.stream.SelfFetchable(); got != c.want {
				t.Errorf("SelfFetchable() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNormalizeStreamHeaders(t *testing.T) {
	cases := []struct {
		name string
		in   http.Header
		want http.Header
	}{
		{
			name: "derives Origin from Referer when absent",
			in:   http.Header{"Referer": {"https://player.cinezo.live/"}, "User-Agent": {"x"}},
			want: http.Header{"Referer": {"https://player.cinezo.live/"}, "User-Agent": {"x"}, "Origin": {"https://player.cinezo.live"}},
		},
		{
			name: "keeps an explicit Origin",
			in:   http.Header{"Referer": {"https://a.example/watch"}, "Origin": {"https://keep.example"}},
			want: http.Header{"Referer": {"https://a.example/watch"}, "Origin": {"https://keep.example"}},
		},
		{
			name: "drops Range and Accept-Encoding",
			in:   http.Header{"Referer": {"https://a.example/"}, "Range": {"bytes=0-99"}, "Accept-Encoding": {"br, zstd"}},
			want: http.Header{"Referer": {"https://a.example/"}, "Origin": {"https://a.example"}},
		},
		{
			name: "no Referer means no derived Origin",
			in:   http.Header{"User-Agent": {"x"}},
			want: http.Header{"User-Agent": {"x"}},
		},
		{
			name: "nil stays nil",
			in:   nil,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeStreamHeaders(c.in)
			if !maps.EqualFunc(got, c.want, slices.Equal) {
				t.Errorf("NormalizeStreamHeaders(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
