// Package api_test drives the cast contract end to end: a real server, a real client, a fake renderer between them.
package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/internal/api/client"
	"github.com/stupside/castor/internal/api/server"
	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// engine is the server's machinery: it ranks by reversing, so the order cast in proves ranking ran.
type engine struct {
	play     play
	asked    chan *castorv1.Preferences
	measured chan *source.Stream
}

func (e *engine) caster(asked *castorv1.Preferences) server.Caster {
	e.asked <- asked
	return e
}

func (*engine) Rank(ctx context.Context, streams []*source.Stream) ([]*source.Stream, error) {
	slog.InfoContext(ctx, "ranking streams", "count", len(streams))
	out := slices.Clone(streams)
	slices.Reverse(out)
	out[0].LastResort = true
	return out, nil
}

func (e *engine) Measure(_ context.Context, stream *source.Stream) (*source.Stream, error) {
	e.measured <- stream
	return stream, nil
}

func (e *engine) Play(ctx context.Context, r execute.Renderer, l deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error {
	return e.play(ctx, r, l, streams, turns)
}

// tv is the renderer on the client's network: it fetches what it is told to play.
type tv struct {
	connected chan device.Info
	handed    chan *url.URL
	played    chan []byte
	end       error
	closed    chan struct{}
}

func newTV() *tv {
	return &tv{connected: make(chan device.Info, 4), handed: make(chan *url.URL, 4), played: make(chan []byte, 4), closed: make(chan struct{}, 4)}
}

func (t *tv) Profile(device.Type) media.Capabilities { return media.Capabilities{SelfFetch: true} }

func (t *tv) Connect(_ context.Context, target device.Info) (device.Device, error) {
	t.connected <- target
	return t, nil
}

func (t *tv) Play(ctx context.Context, u *url.URL, _ string) error {
	t.handed <- u
	if u.Scheme == "https" {
		t.played <- []byte(u.String())
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	t.played <- body
	return err
}

func (t *tv) AwaitEnd(ctx context.Context) error {
	if t.end != nil {
		return t.end
	}
	<-ctx.Done()
	return ctx.Err()
}

func (t *tv) Capabilities() media.Capabilities {
	return media.Capabilities{Containers: []string{"mpegts"}, Video: []media.VideoSupport{{Codec: media.CodecH264, MaxLevel: 42}}}
}

func (t *tv) StreamHeaders(string) map[string]string {
	return map[string]string{"transferMode.dlna.org": "Streaming"}
}

func (t *tv) Close() error {
	t.closed <- struct{}{}
	return nil
}

type play func(ctx context.Context, renderer execute.Renderer, listeners deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error

func backend(p play) server.Backend { return machinery(p).backend() }

func machinery(p play) *engine {
	return &engine{play: p, asked: make(chan *castorv1.Preferences, 4), measured: make(chan *source.Stream, 1)}
}

func (e *engine) backend() server.Backend {
	return server.Backend{Caster: e.caster}
}

// asked is what this operator asks of every cast in these tests.
var asked = &castorv1.Preferences{Delivery: castorv1.Delivery_DELIVERY_SERVE, MaxHeight: 720, Subtitles: "fr"}

// stream is a found link, with the header its page fetched it with.
func stream(raw string) *castorv1.Stream {
	return &castorv1.Stream{Url: raw, Headers: map[string]*castorv1.HeaderValues{"Referer": {Values: []string{"https://page.example/"}}}}
}

func named(raw string) *castorv1.StartCastRequest {
	return &castorv1.StartCastRequest{Streams: &castorv1.StartCastRequest_Named{Named: stream(raw)}, Preferences: asked}
}

func found(raws ...string) *castorv1.StartCastRequest {
	streams := make([]*castorv1.Stream, len(raws))
	for i, raw := range raws {
		streams[i] = stream(raw)
	}
	return &castorv1.StartCastRequest{Streams: &castorv1.StartCastRequest_Found_{Found: &castorv1.StartCastRequest_Found{Streams: streams}}, Preferences: asked}
}

// bases are where a served test's API answers, and where its renderers fetch.
type bases struct{ api, media string }

func serve(t *testing.T, b server.Backend, renderers client.Renderers) (*client.Client, bases) {
	t.Helper()
	lan, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api, err := server.Embedded(t.Context(), b, lan)
	if err != nil {
		t.Fatal(err)
	}
	return client.New(api, renderers), bases{api: api, media: "http://" + lan.Addr().String()}
}

// progress is every status a watcher was shown, in order.
type progress struct{ shown []*castorv1.CastStatus }

func (p *progress) Status(s *castorv1.CastStatus) { p.shown = append(p.shown, s) }

var bedroom = device.Info{Name: "Bedroom", Type: "dlna", Address: "10.0.0.9"}

// cast starts a cast and follows it to its end, as a frontend does.
func cast(t *testing.T, c *client.Client, req *castorv1.StartCastRequest) error {
	t.Helper()
	id, err := c.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return follow(t, c, id, &progress{}, nil)
}

// follow watches cast id, its lines going to logs at logs' level, then lends it the renderer, and returns its outcome.
func follow(t *testing.T, c *client.Client, id string, shown client.Progress, logs *recorder) error {
	t.Helper()
	var lines slog.Handler
	var level slog.Level
	if logs != nil {
		lines, level = logs, logs.level
	}
	w, err := c.Watch(t.Context(), id, shown, lines, level)
	if err != nil {
		return err
	}
	driving := make(chan error, 1)
	go func() { driving <- c.Drive(t.Context(), id, bedroom) }()
	outcome := w.Outcome()
	if err := <-driving; err != nil {
		t.Errorf("driving ended with %v", err)
	}
	return outcome
}

// handoff is a cast the renderer fetches the head stream of for itself.
func handoff(ctx context.Context, r execute.Renderer, _ deliver.Listeners, streams []*source.Stream, _ attempt.Turns) error {
	dev, err := r.Connect(ctx)
	if err != nil {
		return err
	}
	defer dev.Close()
	return dev.Play(ctx, streams[0].URL, streams[0].ContentType)
}

func TestFoundStreamsAreRankedThenHandedToTheClientsRendererAsTheyAre(t *testing.T) {
	screen := newTV()
	var profile, negotiated media.Capabilities
	c, _ := serve(t, backend(func(ctx context.Context, r execute.Renderer, _ deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error {
		profile = r.Profile()
		dev, err := r.Connect(ctx)
		if err != nil {
			return err
		}
		defer dev.Close()
		negotiated = dev.Capabilities()
		turns.Attempting(1)
		return dev.Play(ctx, streams[0].URL, streams[0].ContentType)
	}), screen)

	id, err := c.Start(t.Context(), found("https://cdn.example/a.m3u8", "https://cdn.example/b.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	shown := &progress{}
	if err := follow(t, c, id, shown, nil); err != nil {
		t.Fatalf("a cast that ended cleanly reported %v", err)
	}
	if first := shown.shown[0]; first.GetPhase() != castorv1.CastStatus_PHASE_AWAITING_DEVICE {
		t.Errorf("the watcher was first shown %+v, want the cast awaiting its device", first)
	}
	if last, want := shown.shown[len(shown.shown)-1], (&castorv1.CastStatus{Phase: castorv1.CastStatus_PHASE_CASTING, Streams: 2, Castable: 2, Attempt: 1}); !proto.Equal(last, want) {
		t.Errorf("the watcher was last shown %+v, want %+v", last, want)
	}
	if got := <-screen.connected; got != bedroom {
		t.Errorf("the client connected %v, want the device it lent the cast", got)
	}
	if got := string(<-screen.played); got != "https://cdn.example/b.m3u8" {
		t.Errorf("the renderer was handed %q, want the server's ranking head as is", got)
	}
	if !profile.SelfFetch {
		t.Error("the server planned without the client's profile of its renderer")
	}
	if len(negotiated.Video) != 1 || negotiated.Video[0].Codec != media.CodecH264 || negotiated.Video[0].MaxLevel != 42 {
		t.Errorf("the server negotiated %+v, want the client renderer's own capabilities", negotiated)
	}
	select {
	case <-screen.closed:
	case <-time.After(5 * time.Second):
		t.Error("the server's close never reached the client's renderer")
	}
}

func TestANamedStreamIsMeasuredNotRankedAsTheClientAsked(t *testing.T) {
	e := machinery(handoff)
	c, _ := serve(t, e.backend(), newTV())

	if err := cast(t, c, named("https://cdn.example/direct")); err != nil {
		t.Fatal(err)
	}
	if got := <-e.asked; !proto.Equal(got, asked) {
		t.Errorf("the server cast as asked %v, want the client's %v", got, asked)
	}
	if measured := <-e.measured; measured.URL.String() != "https://cdn.example/direct" || measured.Headers.Get("Referer") != "https://page.example/" {
		t.Errorf("measured %v, want the named stream with what fetching it needs", measured)
	}
}

func TestADryRunIsTheServersRankingAsTheCastWouldAskIt(t *testing.T) {
	e := machinery(nil)
	c, _ := serve(t, e.backend(), newTV())

	ranked, err := c.Rank(t.Context(), &castorv1.RankRequest{Streams: []*castorv1.Stream{stream("https://cdn.example/a.m3u8"), stream("https://cdn.example/b.m3u8")}, Preferences: asked})
	if err != nil {
		t.Fatal(err)
	}
	want := []*castorv1.RankedStream{{Url: "https://cdn.example/b.m3u8", LastResort: true}, {Url: "https://cdn.example/a.m3u8"}}
	if !slices.EqualFunc(ranked, want, func(a, b *castorv1.RankedStream) bool { return proto.Equal(a, b) }) {
		t.Errorf("ranked %+v, want the server's order %+v", ranked, want)
	}
	if got := <-e.asked; !proto.Equal(got, asked) {
		t.Errorf("ranked as asked %v, want the client's %v", got, asked)
	}
}

func TestTheServerRefusesACastTheContractForbids(t *testing.T) {
	_, base := serve(t, backend(handoff), newTV())
	// A client that skips the contract's rules, so the server is the one to hold them.
	raw := castorv1connect.NewCastServiceClient(http.DefaultClient, base.api)

	unasked := named("https://cdn.example/direct")
	unasked.Preferences = nil
	undelivered := named("https://cdn.example/direct")
	undelivered.Preferences = &castorv1.Preferences{MaxHeight: 720}
	unspoken := named("https://cdn.example/direct")
	unspoken.Preferences = &castorv1.Preferences{Delivery: castorv1.Delivery_DELIVERY_AUTO, MaxHeight: 720, Subtitles: "not a language"}
	misheaded := named("https://cdn.example/direct")
	misheaded.GetNamed().Headers = map[string]*castorv1.HeaderValues{"Bad Header": {Values: []string{"x"}}}
	for name, req := range map[string]*castorv1.StartCastRequest{
		"no preferences":       unasked,
		"no delivery":          undelivered,
		"no subtitle language": unspoken,
		"no header name":       misheaded,
		"a relative link":      named("/movie.m3u8"),
		"no streams found":     found(),
	} {
		if _, err := raw.StartCast(t.Context(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s started with %v, want the contract's refusal", name, err)
		}
	}
}

func TestTheServerAnswersHealthAndDescribesItself(t *testing.T) {
	_, base := serve(t, backend(handoff), newTV())

	resp, err := http.Post(base.api+"/grpc.health.v1.Health/Check", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SERVING") {
		t.Errorf("health answered %s, want SERVING", body)
	}

	var h2c http.Protocols
	h2c.SetUnencryptedHTTP2(true)
	services, err := grpcreflect.NewClient(&http.Client{Transport: &http.Transport{Protocols: &h2c}}, base.api).NewStream(t.Context()).ListServices()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(services, protoreflect.FullName(castorv1connect.CastServiceName)) {
		t.Errorf("reflection listed %v, want the cast service a debugging client can call", services)
	}
}

// recorder keeps the lines it is handed at level and above.
type recorder struct {
	level slog.Level
	mu    sync.Mutex
	lines []slog.Record
}

func (r *recorder) Enabled(_ context.Context, l slog.Level) bool { return l >= r.level }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, rec)
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(string) slog.Handler      { return r }

func (r *recorder) said(message string) (slog.Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.lines {
		if rec.Message == message {
			return rec, true
		}
	}
	return slog.Record{}, false
}

// embedded routes this process's logs as an embedded engine does, for the test's lifetime.
func embedded(t *testing.T) *recorder {
	t.Helper()
	process := &recorder{level: slog.LevelDebug}
	prev := slog.Default()
	slog.SetDefault(slog.New(server.Logs(process, slog.DiscardHandler)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return process
}

func logging(ctx context.Context, _ execute.Renderer, _ deliver.Listeners, _ []*source.Stream, _ attempt.Turns) error {
	slog.InfoContext(ctx, "engine at work", "try", 1)
	return nil
}

func TestTheServersLinesForACastReachOnlyTheWatchersThatAskedForThem(t *testing.T) {
	process := embedded(t)
	c, _ := serve(t, backend(logging), newTV())

	for name, tc := range map[string]struct {
		asked *recorder
		gets  bool
	}{
		"asked for info": {&recorder{level: slog.LevelInfo}, true},
		"asked for warn": {&recorder{level: slog.LevelWarn}, false},
		"asked for none": {nil, false},
	} {
		id, err := c.Start(t.Context(), found("https://cdn.example/a.m3u8"))
		if err != nil {
			t.Fatal(err)
		}
		if err := follow(t, c, id, &progress{}, tc.asked); err != nil {
			t.Fatal(err)
		}
		if tc.asked == nil {
			continue
		}
		line, got := tc.asked.said("engine at work")
		if got != tc.gets {
			t.Errorf("%s: got the engine's line %v, want %v", name, got, tc.gets)
		}
		if got && line.NumAttrs() != 1 {
			t.Errorf("%s: the line arrived with %d attributes, want its try", name, line.NumAttrs())
		}
	}
	if _, wrote := process.said("engine at work"); wrote {
		t.Error("the embedded engine wrote its line to this process's output, beside the client's")
	}
}

func TestWhatTheServerServesTheRendererFetchesFromTheServerItself(t *testing.T) {
	screen := newTV()
	delivered, delivery := make(chan string, 1), make(chan string, 1)
	c, base := serve(t, backend(func(ctx context.Context, r execute.Renderer, listeners deliver.Listeners, _ []*source.Stream, _ attempt.Turns) error {
		// The delivery the engine opens, through the listeners the server binds it to.
		l, err := listeners.Listen(ctx)
		if err != nil {
			return err
		}
		delivery <- l.Addr().String()
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			delivered <- r.URL.RequestURI()
			_, _ = io.WriteString(w, "media bytes")
		})}
		go func() { _ = srv.Serve(l) }()
		defer srv.Close()

		dev, err := r.Connect(ctx)
		if err != nil {
			return err
		}
		defer dev.Close()
		if h := dev.StreamHeaders("video/mp2t"); h["transferMode.dlna.org"] != "Streaming" {
			return errors.New("the renderer's stream headers did not reach the server")
		}
		return dev.Play(ctx, &url.URL{Scheme: "http", Host: l.Addr().String(), Path: "/stream.ts", RawQuery: "n=1"}, "video/mp2t")
	}), screen)

	if err := cast(t, c, named("https://cdn.example/direct")); err != nil {
		t.Fatal(err)
	}
	handed, served := <-screen.handed, <-delivery
	if "http://"+handed.Host != base.media || !strings.HasPrefix(handed.Path, "/media/") {
		t.Errorf("the renderer was handed %s, want the server's media route at %s rather than its delivery at %s", handed, base.media, served)
	}
	if got := string(<-screen.played); got != "media bytes" {
		t.Errorf("the renderer fetched %q, want the served bytes", got)
	}
	if got := <-delivered; got != "/stream.ts?n=1" {
		t.Errorf("the delivery was asked for %q, want the path and query the engine served", got)
	}
}

func TestASourceOnLoopbackIsHandedToTheRendererAsItIs(t *testing.T) {
	screen := newTV()
	c, _ := serve(t, backend(handoff), screen)

	if err := cast(t, c, named("http://127.0.0.1:9/movie.mp4")); err == nil {
		t.Fatal("the renderer fetched an origin nothing serves")
	}
	if got := (<-screen.handed).String(); got != "http://127.0.0.1:9/movie.mp4" {
		t.Errorf("the renderer was handed %s, want the source itself: only what the cast serves goes through the server", got)
	}
}

func TestRenderersReachOnlyThePortsACastServesNeverTheAPI(t *testing.T) {
	released := make(chan struct{})
	c, base := serve(t, backend(func(ctx context.Context, r execute.Renderer, _ deliver.Listeners, _ []*source.Stream, _ attempt.Turns) error {
		dev, err := r.Connect(ctx)
		if err != nil {
			return err
		}
		defer dev.Close()
		<-released
		return nil
	}), newTV())

	id, err := c.Start(t.Context(), named("https://cdn.example/direct"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- follow(t, c, id, &progress{}, nil) }()

	for what, path := range map[string]string{
		"a port the cast never served": "/media/" + id + "/22/etc/passwd",
		"the API":                      "/" + castorv1connect.CastServiceName + "/StopCast",
	} {
		resp, err := http.Post(base.media+path, "application/json", strings.NewReader(`{"castId":"`+id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("renderers reaching %s were answered %d, want 404", what, resp.StatusCode)
		}
	}
	close(released)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestARendererGoneOnTheClientIsGoneToTheServersRecovery(t *testing.T) {
	screen := newTV()
	screen.end = &media.Gone{Renderer: "Bedroom", Observed: "stopped answering"}
	seen := make(chan error, 1)
	c, _ := serve(t, backend(func(ctx context.Context, r execute.Renderer, _ deliver.Listeners, _ []*source.Stream, _ attempt.Turns) error {
		dev, err := r.Connect(ctx)
		if err != nil {
			return err
		}
		defer dev.Close()
		err = dev.AwaitEnd(ctx)
		seen <- err
		return err
	}), screen)

	err := cast(t, c, named("https://cdn.example/direct"))
	if gone, ok := errors.AsType[*media.Gone](<-seen); !ok || gone.Renderer != "Bedroom" {
		t.Errorf("the server saw %v, want a *media.Gone it can classify", gone)
	}
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("the client was told %v, want the renderer's loss", err)
	}
}

func TestStoppingTheCastEndsItOnTheServerAndReleasesTheRenderer(t *testing.T) {
	screen := newTV()
	playing, stopped := make(chan struct{}), make(chan struct{})
	c, _ := serve(t, backend(func(ctx context.Context, r execute.Renderer, _ deliver.Listeners, _ []*source.Stream, _ attempt.Turns) error {
		dev, err := r.Connect(ctx)
		if err != nil {
			return err
		}
		defer dev.Close()
		close(playing)
		err = dev.AwaitEnd(ctx)
		close(stopped)
		return err
	}), screen)

	id, err := c.Start(t.Context(), named("https://cdn.example/direct"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- follow(t, c, id, &progress{}, nil) }()
	<-playing
	if err := c.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if err := <-done; !errors.Is(err, client.ErrStopped) {
		t.Errorf("the watch ended with %v, want the stop as its outcome", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the server kept casting after the stop")
	}
	select {
	case <-screen.closed:
	case <-time.After(5 * time.Second):
		t.Error("the renderer was left open after the cast stopped")
	}
}

func TestACastHasOneDeviceAndPlaysOnWhenTheClientLendingItLeaves(t *testing.T) {
	playing, ended := make(chan struct{}), make(chan struct{})
	c, _ := serve(t, backend(func(ctx context.Context, r execute.Renderer, _ deliver.Listeners, streams []*source.Stream, _ attempt.Turns) error {
		dev, err := r.Connect(ctx)
		if err != nil {
			return err
		}
		defer dev.Close()
		if err := dev.Play(ctx, streams[0].URL, streams[0].ContentType); err != nil {
			return err
		}
		close(playing)
		err = dev.AwaitEnd(ctx)
		close(ended)
		return err
	}), newTV())

	id, err := c.Start(t.Context(), named("https://cdn.example/direct"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := c.Watch(t.Context(), id, &progress{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	lending, leave := context.WithCancel(t.Context())
	go func() { _ = c.Drive(lending, id, bedroom) }()
	<-playing

	if err := c.Drive(t.Context(), id, bedroom); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a second device was met with %v, want it refused", err)
	}
	leave()
	select {
	case <-ended:
		t.Fatal("the cast ended with the client that lent its device")
	case <-time.After(300 * time.Millisecond):
	}
	if err := c.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := w.Outcome(); !errors.Is(err, client.ErrStopped) {
		t.Errorf("the watch ended with %v, want the stop that ended the cast its client had left", err)
	}
}

func TestWatchingNeverDrivesAndTheCastStartsWithItsDriver(t *testing.T) {
	screen := newTV()
	c, _ := serve(t, backend(handoff), screen)

	id, err := c.Start(t.Context(), named("https://cdn.example/direct"))
	if err != nil {
		t.Fatal(err)
	}
	shown := &progress{}
	w, err := c.Watch(t.Context(), id, shown, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	onlooker, err := c.Watch(t.Context(), id, &progress{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-screen.connected:
		t.Fatal("watching the cast drove its renderer")
	case <-time.After(200 * time.Millisecond):
	}
	if got := shown.shown; len(got) != 1 || got[0].GetPhase() != castorv1.CastStatus_PHASE_AWAITING_DEVICE {
		t.Errorf("before any driver the watcher was shown %+v, want only the wait", got)
	}

	if err := c.Drive(t.Context(), id, bedroom); err != nil {
		t.Fatal(err)
	}
	if err := w.Outcome(); err != nil {
		t.Errorf("the watch ended with %v, want the cast's clean end", err)
	}
	if err := onlooker.Outcome(); err != nil {
		t.Errorf("a second watcher ended with %v, want the same clean end", err)
	}
	if got := string(<-screen.played); got != "https://cdn.example/direct" {
		t.Errorf("played %q", got)
	}
}

// pages finds the same two streams, headers and all, on any page but one that plays nothing.
type pages struct{}

func (pages) ExtractAll(_ context.Context, urls []string) ([]*source.Stream, error) {
	if slices.Contains(urls, "https://site.example/empty") {
		return nil, errors.New("no stream extracted from 1 URL(s)")
	}
	found := make([]*source.Stream, 0, 2)
	for _, raw := range []string{"https://cdn.example/a.m3u8", "https://cdn.example/b.m3u8"} {
		u, _ := url.Parse(raw)
		found = append(found, &source.Stream{URL: u, Headers: http.Header{"Referer": {"https://site.example/watch"}}, ContentType: "application/vnd.apple.mpegurl"})
	}
	return found, nil
}

func TestPagesAreOpenedOnTheServerAndTheirStreamsReturnWithWhatFetchingThemNeeds(t *testing.T) {
	b := backend(handoff)
	b.Extractor = pages{}
	c, _ := serve(t, b, newTV())

	streams, err := c.Extract(t.Context(), &castorv1.ExtractRequest{Pages: []string{"https://site.example/watch"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 2 || streams[0].GetUrl() != "https://cdn.example/a.m3u8" {
		t.Fatalf("extracted %v, want the page's two streams", streams)
	}
	if referer := streams[1].GetHeaders()["Referer"].GetValues(); len(referer) != 1 || referer[0] != "https://site.example/watch" || streams[1].GetContentType() != "application/vnd.apple.mpegurl" {
		t.Errorf("a stream came back as %v, without what fetching it needs", streams[1])
	}

	if _, err := c.Extract(t.Context(), &castorv1.ExtractRequest{Pages: []string{"https://site.example/empty"}}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a page that plays nothing answered %v, want not found", err)
	}
	if _, err := c.Extract(t.Context(), &castorv1.ExtractRequest{Pages: []string{"not a page"}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a malformed page answered %v, want the contract's refusal", err)
	}
}

func TestTheServersOwnLinesStayOffTheProcessThatEmbedsIt(t *testing.T) {
	process := embedded(t)
	b := backend(handoff)
	b.Extractor = pages{}
	c, _ := serve(t, b, newTV())

	// A ranking logs on the server with no cast to carry it.
	if _, err := c.Rank(t.Context(), &castorv1.RankRequest{Streams: []*castorv1.Stream{stream("https://cdn.example/a.m3u8")}, Preferences: asked}); err != nil {
		t.Fatal(err)
	}
	slog.InfoContext(t.Context(), "client line")

	if _, wrote := process.said("client line"); !wrote {
		t.Error("the client's own line never reached its output")
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	for _, r := range process.lines {
		if r.Message != "client line" {
			t.Errorf("the embedded server wrote %q to the client's output", r.Message)
		}
	}
}
