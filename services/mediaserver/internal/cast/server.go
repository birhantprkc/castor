// Package cast runs the media server's casts behind the media contract: each cast's lifecycle, the device lent to it, and what devices fetch from it.
package cast

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/stupside/castor/gen/castor/media/v1/mediav1connect"
	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/transport"
	"github.com/stupside/castor/services/mediaserver/internal/cast/attempt"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/execute"
	"github.com/stupside/castor/services/mediaserver/internal/castlog"
	"github.com/stupside/castor/services/mediaserver/internal/mediaroute"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// Backend is what the server casts with, as the tier's entry point assembles it.
type Backend struct {
	Extractor Extractor
	// Caster binds one cast to what it asked.
	Caster func(asked *castorv1.Preferences) Caster
}

// Extractor finds the streams pages play.
type Extractor interface {
	ExtractAll(ctx context.Context, pages []string) ([]*source.Stream, error)
}

// Caster readies and plays one cast's streams: pages' streams are ranked, a stream source measured (and identified when it says nothing).
type Caster interface {
	Rank(ctx context.Context, streams []*source.Stream) ([]*source.Stream, error)
	Measure(ctx context.Context, stream *source.Stream) (*source.Stream, error)
	Play(ctx context.Context, device execute.Device, listeners deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error
}

var errShutdown = errors.New("server shutting down")

// Server is the media server: its API, for the API server alone, and its media route, for devices.
type Server struct {
	API   http.Handler
	Media http.Handler

	sessions *sessions
	stop     context.CancelCauseFunc
}

// New serves casts with b, telling devices to reach its media route at reach.
func New(b Backend, reach *url.URL) *Server {
	casting, stop := context.WithCancelCause(context.Background())
	reg := newSessions()
	valid := transport.Checked()
	api := http.NewServeMux()
	api.Handle(mediav1connect.NewCastServiceHandler(&casts{ctx: casting, extractor: b.Extractor, caster: b.Caster, reach: reach, sessions: reg}, valid))
	api.Handle(mediav1connect.NewDeviceServiceHandler(devices{sessions: reg}, valid))
	api.Handle(mediav1connect.NewStreamServiceHandler(streams{extractor: b.Extractor, caster: b.Caster}, valid))
	transport.Introspect(api, mediav1connect.CastServiceName, mediav1connect.DeviceServiceName, mediav1connect.StreamServiceName)
	media := http.NewServeMux()
	media.Handle(mediaroute.Pattern, mediaroute.Handler(func(cast, port string) bool {
		s, ok := reg.Find(cast)
		return ok && s.deliveries.Serves(port)
	}))
	return &Server{API: castlog.Served(api), Media: castlog.Served(media), sessions: reg, stop: stop}
}

// Shutdown ends every cast, failed as the server shuts down, and waits until they have let go of what they hold, or ctx ends.
func (s *Server) Shutdown(ctx context.Context) {
	s.stop(errShutdown)
	s.sessions.Drain(ctx)
}
