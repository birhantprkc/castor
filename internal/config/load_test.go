package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/probe"
)

type loadCase struct {
	name string
	// yaml is config.yaml's content; empty means no file exists.
	yaml    string
	local   string
	env     map[string]string
	wantErr bool
	want    func(*testing.T, *Config)
}

func TestLoad(t *testing.T) {
	for _, tt := range []loadCase{{
		name: "the environment alone configures a cast",
		env:  map[string]string{"CASTOR_DEVICE__NAME": "Xiaomi TV Box", "CASTOR_DEVICE__TYPE": "chromecast"},
		want: func(t *testing.T, cfg *Config) {
			if cfg.Device.Name != "Xiaomi TV Box" {
				t.Errorf("device.name = %q, want it from the environment", cfg.Device.Name)
			}
			if cfg.Device.Type != "chromecast" {
				t.Errorf("device.type = %q, want it from the environment", cfg.Device.Type)
			}
			if cfg.Network.Timeout != 5*time.Second {
				t.Errorf("network.timeout = %s, want the 5s default", cfg.Network.Timeout)
			}
			if cfg.Resolver.MaxHeight != 1080 {
				t.Errorf("resolver.max_height = %d, want the 1080 default", cfg.Resolver.MaxHeight)
			}
		},
	}, {
		name:  "the local overlay beats the file it sits beside",
		yaml:  "device:\n  name: tv\n  type: dlna\ntmdb:\n  api_key: placeholder\n",
		local: "tmdb:\n  api_key: real\n",
		want: func(t *testing.T, cfg *Config) {
			if cfg.TMDB.APIKey != "real" {
				t.Errorf("tmdb.api_key = %q, want the overlay's value", cfg.TMDB.APIKey)
			}
			if cfg.Device.Name != "tv" {
				t.Errorf("device.name = %q, want the base value the overlay never mentioned", cfg.Device.Name)
			}
		},
	}, {
		name: "the file beats the default, durations included",
		yaml: "device:\n  name: tv\n  type: dlna\nresolver:\n  max_height: 2160\n  ffprobe_path: /opt/ffprobe\nnetwork:\n  timeout: 12s\n",
		want: func(t *testing.T, cfg *Config) {
			if cfg.Resolver.MaxHeight != 2160 {
				t.Errorf("resolver.max_height = %d, want the file's 2160", cfg.Resolver.MaxHeight)
			}
			// The cast reads the same ceiling the ranker did.
			if play := cfg.Playback(cfg.Target(), nil).Execute; play.MaxHeight != 2160 || play.Probes != probe.FFprobe("/opt/ffprobe") {
				t.Errorf("cast max_height = %d, probes = %v, want the file's 2160 and /opt/ffprobe", play.MaxHeight, play.Probes)
			}
			if cfg.Network.Timeout != 12*time.Second {
				t.Errorf("network.timeout = %s, want the file's 12s", cfg.Network.Timeout)
			}
		},
	}, {
		name:    "neither a name nor a host leaves the device unaddressable",
		yaml:    "device:\n  type: dlna\n",
		wantErr: true,
	}, {
		name:    "an unknown device type is a typo, not a discovery failure",
		yaml:    "device:\n  name: tv\n  type: firetv\n",
		wantErr: true,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if tt.yaml != "" {
				if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.local != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.local.yaml"), []byte(tt.local), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg, err := Load(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want a validation error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tt.want(t, cfg)
		})
	}
}

func TestAPinnedHostIsWhereTheCastConnects(t *testing.T) {
	var asked atomic.Bool
	ecp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(true)
		http.NotFound(w, r)
	}))
	defer ecp.Close()

	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "device:\n  type: roku\n  host: " + ecp.Listener.Addr().String() + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, _ = cfg.Playback(cfg.Target(), nil).Execute.Renderer.Connect(t.Context())
	if !asked.Load() {
		t.Error("connecting never reached device.host, so a pinned device is not the one the cast talks to")
	}
}
