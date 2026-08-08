package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/core"
)

// A load is one row: the files on disk, the environment around them, and what
// the configuration that comes out has to hold.
//
// Layering is the whole subject. A value can arrive from four places (the typed
// defaults, config.yaml, the config.local.yaml overlay beside it, and the
// environment), each beating the one before, and a row is how one of those
// precedences is stated. Written as separate functions the rows shared fifteen
// lines of setup and hid which layer each was actually about.
type loadCase struct {
	name string
	// yaml is config.yaml's content; empty means no file exists at that path at
	// all, which is a supported way to run castor and not an error.
	yaml string
	// local is the config.local.yaml overlay written beside it.
	local string
	env   map[string]string
	// wantErr means Load must refuse rather than hand the rest of the program a
	// configuration nothing downstream could act on.
	wantErr bool
	want    func(*testing.T, *Config)
}

func TestLoad(t *testing.T) {
	for _, tt := range []loadCase{{
		// No file at all, with the required fields supplied by the environment. The
		// typed defaults still have to fill in everything nobody mentioned.
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
			if cfg.Resolver.MaxHeight != 1080 {
				t.Errorf("resolver.max_height = %d, want the default under both", cfg.Resolver.MaxHeight)
			}
		},
	}, {
		// The footgun the shipped template walks straight into: a section header
		// whose every field is commented out parses to YAML null, and a null that
		// wiped the defaults beneath it would fail validation on fields the user
		// never touched.
		name: "a section commented out to null keeps the defaults beneath it",
		yaml: "device:\n  name: tv\n  type: dlna\nresolver:\n  # max_height: 1080\n",
		want: func(t *testing.T, cfg *Config) {
			if cfg.Resolver.MaxHeight != 1080 {
				t.Errorf("resolver.max_height = %d, want the 1080 default", cfg.Resolver.MaxHeight)
			}
			if cfg.Resolver.FFprobePath != "ffprobe" {
				t.Errorf("resolver.ffprobe_path = %q, want the default", cfg.Resolver.FFprobePath)
			}
			// Durations come from the typed defaults as real time.Duration values
			// rather than "30s" strings, so they have to survive the decode intact.
			if cfg.Resolver.HLSTimeout != 30*time.Second {
				t.Errorf("resolver.hls_timeout = %s, want the 30s default", cfg.Resolver.HLSTimeout)
			}
			if cfg.Network.Timeout != 5*time.Second {
				t.Errorf("network.timeout = %s, want the 5s default", cfg.Network.Timeout)
			}
		},
	}, {
		name: "the file beats the default, durations included",
		yaml: "device:\n  name: tv\n  type: dlna\nresolver:\n  max_height: 2160\nnetwork:\n  timeout: 12s\n",
		want: func(t *testing.T, cfg *Config) {
			if cfg.Resolver.MaxHeight != 2160 {
				t.Errorf("resolver.max_height = %d, want the file's 2160", cfg.Resolver.MaxHeight)
			}
			if cfg.Network.Timeout != 12*time.Second {
				t.Errorf("network.timeout = %s, want the file's 12s", cfg.Network.Timeout)
			}
			if cfg.Resolver.ProbeMaxConcurrency != 2 {
				t.Errorf("probe_max_concurrency = %d, want the sibling default the file never touched", cfg.Resolver.ProbeMaxConcurrency)
			}
		},
	}, {
		// A pinned host makes the name optional, and has to reach the agnostic
		// device config the connect layer actually reads.
		name: "a pinned host addresses a device with no name",
		yaml: "device:\n  type: dlna\n  host: 192.168.0.3\n",
		want: func(t *testing.T, cfg *Config) {
			if cfg.Device.Host != "192.168.0.3" {
				t.Errorf("device.host = %q, want 192.168.0.3", cfg.Device.Host)
			}
			if got := cfg.Playback().Device.Address; got != "192.168.0.3" {
				t.Errorf("device.Config.Address = %q, want the host to resolve onto it", got)
			}
		},
	}, {
		name:    "neither a name nor a host leaves the device unaddressable",
		yaml:    "device:\n  type: dlna\n",
		wantErr: true,
	}, {
		// The one cast decision the operator owns, from the file.
		name: "the file's delivery preference reaches the cast",
		yaml: "device:\n  name: tv\n  type: chromecast\ncast:\n  delivery: serve\n",
		want: func(t *testing.T, cfg *Config) {
			if got := cfg.Playback().Delivery; got != core.DeliveryServe {
				t.Errorf("cast.delivery = %q, want %q", got, core.DeliveryServe)
			}
		},
	}, {
		// And from the environment, which is the reason it is a config key rather
		// than a CLI flag: CASTOR_CAST__DELIVERY=serve makes it a one-off, and works
		// in a container where a flag does not.
		name: "the environment's delivery preference reaches the cast",
		yaml: "device:\n  name: tv\n  type: chromecast\n",
		env:  map[string]string{"CASTOR_CAST__DELIVERY": "serve"},
		want: func(t *testing.T, cfg *Config) {
			if got := cfg.Playback().Delivery; got != core.DeliveryServe {
				t.Errorf("cast.delivery = %q, want %q", got, core.DeliveryServe)
			}
		},
	}, {
		name: "an unset delivery preference decides nothing",
		yaml: "device:\n  name: tv\n  type: chromecast\n",
		want: func(t *testing.T, cfg *Config) {
			if got := cfg.Playback().Delivery; got == core.DeliveryServe {
				t.Errorf("cast.delivery = %q; nobody asked for a relay", got)
			}
		},
	}, {
		// What the enum buys over a bool: a typo fails at load instead of silently
		// meaning auto.
		name:    "an unknown delivery mode is a typo, not a default",
		yaml:    "device:\n  name: tv\n  type: chromecast\ncast:\n  delivery: relay\n",
		wantErr: true,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			// A fresh directory per row, so "no config file" means exactly that and
			// cannot be answered by whatever else is on the machine.
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
