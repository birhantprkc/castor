package mediaserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/gen/castor/media/v1/mediav1connect"
	"github.com/stupside/castor/internal/settings"
	"github.com/stupside/castor/internal/transport"
	"github.com/stupside/castor/services/mediaserver/internal/cast"
)

func TestAServerWithATokenAnswersOnlyThoseCarryingItButHealthChecksAndDevices(t *testing.T) {
	srv := cast.New(cast.Backend{}, &url.URL{Scheme: "http", Host: "127.0.0.1:9"})
	ts := httptest.NewServer(onePort(srv, "secret"))
	t.Cleanup(ts.Close)

	stop := &mediav1.StopRequest{CastId: "none"}
	for _, tc := range []struct {
		name   string
		client *http.Client
		want   connect.Code
	}{
		{"without the token", http.DefaultClient, connect.CodeUnauthenticated},
		{"with another token", transport.Bearer("guess"), connect.CodeUnauthenticated},
		{"with the token, past the door to a cast not found", transport.Bearer("secret"), connect.CodeNotFound},
	} {
		_, err := mediav1connect.NewCastServiceClient(tc.client, ts.URL).Stop(t.Context(), stop)
		if connect.CodeOf(err) != tc.want {
			t.Errorf("a request %s was met with %v, want %v", tc.name, err, tc.want)
		}
	}

	health, err := grpchealth.NewClient(http.DefaultClient, ts.URL).Check(t.Context(), &grpchealth.CheckRequest{})
	if err != nil || health.Status != grpchealth.StatusServing {
		t.Errorf("a health check without the token was met with %v %v, want serving", health, err)
	}

	resp, err := http.Get(ts.URL + "/media/none/1/stream.ts")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || strings.Contains(string(body), "token") {
		t.Errorf("a device fetching the media route was answered %d %q, want not found rather than asked for a token it never has", resp.StatusCode, body)
	}
}

// shipped is the repository's config.yaml, read apart from any git-ignored overlay beside it.
func shipped(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEveryKeyTheMediaServerReadsLandsWhereItReadsIt(t *testing.T) {
	for k, v := range map[string]string{
		"CASTOR_TRANSCODE__FFMPEG_PATH":          "/opt/ffmpeg",
		"CASTOR_TRANSCODE__RW_TIMEOUT":           "11s",
		"CASTOR_RESOLVER__PROBE_MAX_CONCURRENCY": "3",
		"CASTOR_RESOLVER__PLAYLIST_TIMEOUT":      "12s",
		"CASTOR_RESOLVER__FFPROBE_PATH":          "/opt/ffprobe",
		"CASTOR_RESOLVER__PROBE_TIMEOUT":         "13s",
		"CASTOR_BROWSER__TIMEOUT":                "14s",
		"CASTOR_BROWSER__HEADLESS":               "false",
		"CASTOR_BROWSER__NO_SANDBOX":             "true",
		"CASTOR_BROWSER__CHROME_PATH":            "/opt/chrome",
		"CASTOR_CAPTURE__MAX_CONCURRENCY":        "5",
		"CASTOR_SERVER__LISTEN":                  ":9410",
		"CASTOR_SERVER__ADVERTISE":               "http://castor.example.com:8410",
		"CASTOR_SERVER__TOKEN":                   "server-secret",
		"CASTOR_NETWORK__INTERFACE":              "en0",
		"CASTOR_WHISPER__MODEL_PATH":             "/opt/ggml.bin",
	} {
		t.Setenv(k, v)
	}
	cfg, err := settings.Read(shipped(t), defaults())
	if err != nil {
		t.Fatal(err)
	}
	for key, ok := range map[string]bool{
		"transcode.ffmpeg_path":          cfg.Transcode.FFmpegPath == "/opt/ffmpeg",
		"transcode.rw_timeout":           cfg.Transcode.RWTimeout == 11*time.Second,
		"resolver.probe_max_concurrency": cfg.Resolver.ProbeMaxConcurrency == 3,
		"resolver.playlist_timeout":      cfg.Resolver.PlaylistTimeout == 12*time.Second,
		"resolver.ffprobe_path":          cfg.Resolver.FFprobePath == "/opt/ffprobe",
		"resolver.probe_timeout":         cfg.Resolver.ProbeTimeout == 13*time.Second,
		"browser.timeout":                cfg.Browser.Timeout == 14*time.Second,
		"browser.headless":               !cfg.Browser.Headless,
		"browser.no_sandbox":             cfg.Browser.NoSandbox,
		"browser.chrome_path":            cfg.Browser.ChromePath == "/opt/chrome",
		"capture.max_concurrency":        cfg.Capture.MaxConcurrency == 5,
		"server.listen":                  cfg.Server.Listen == ":9410",
		"server.advertise":               cfg.Server.Advertise == "http://castor.example.com:8410",
		"server.token":                   cfg.Server.Token == "server-secret",
		"network.interface":              cfg.Network.Interface == "en0",
		"whisper.model_path":             cfg.Whisper.ModelPath == "/opt/ggml.bin",
	} {
		if !ok {
			t.Errorf("%s did not land where the media server reads it", key)
		}
	}
}

func TestDevicesReachAnAdvertisedServerOnlyAtAURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  advertise: my-nas\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Read(path, defaults()); err == nil {
		t.Error("an advertised address devices cannot fetch from was accepted")
	}
}
