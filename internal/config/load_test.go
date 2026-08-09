package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
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
			if got := cfg.Playback().Delivery; got != media.DeliveryServe {
				t.Errorf("cast.delivery = %q, want %q", got, media.DeliveryServe)
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
			if got := cfg.Playback().Delivery; got != media.DeliveryServe {
				t.Errorf("cast.delivery = %q, want %q", got, media.DeliveryServe)
			}
		},
	}, {
		name: "an unset delivery preference decides nothing",
		yaml: "device:\n  name: tv\n  type: chromecast\n",
		want: func(t *testing.T, cfg *Config) {
			if got := cfg.Playback().Delivery; got == media.DeliveryServe {
				t.Errorf("cast.delivery = %q; nobody asked for a relay", got)
			}
		},
	}, {
		// What the enum buys over a bool: a typo fails at load instead of silently
		// meaning auto.
		name:    "an unknown delivery mode is a typo, not a default",
		yaml:    "device:\n  name: tv\n  type: chromecast\ncast:\n  delivery: relay\n",
		wantErr: true,
	}, {
		// The height ceiling has no "off" value, and this is the row the copy decision's
		// arithmetic rests on: media.HeightCap carries no zero case, so a ceiling that could
		// arrive as 0 would refuse every measured source and force a decode, a scale and a
		// re-encode on every cast castor makes. To lift the ceiling, set it above anything
		// you own.
		name:    "a height ceiling of zero is refused rather than read as no ceiling",
		yaml:    "device:\n  name: tv\n  type: chromecast\nresolver:\n  max_height: 0\n",
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

// TestOneResolverServesTheWholeProcess pins what the memo buys. A run ranks its candidates
// and then casts one of them, and those were two calls to the same constructor: two ffprobe
// adapters and, more expensively, two http.Clients with two connection pools, so the playlist
// GET that follows a candidate's probe opened a second connection and a second TLS handshake
// to a host the ranker is deliberately gentle with (see resolve's per-host probe cap).
//
// It is asserted by identity rather than by counting constructions, because identity is the
// property that matters: the same value, so the same pool, whoever asks and however often.
func TestOneResolverServesTheWholeProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("device:\n  name: tv\n  type: chromecast\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ranking := cfg.Source()
	if ranking == nil {
		t.Fatal("the process has no source resolver at all")
	}
	if again := cfg.Source(); again != ranking {
		t.Error("a second caller was handed a second resolver, so the client that probed a candidate is not the client that fetches its playlist")
	}
	// The cast phase reaches it through the configuration it is handed rather than by asking
	// again, which is the half a memo alone would not have fixed.
	if casting := cfg.Playback().Source; casting != ranking {
		t.Error("the cast runs on a different resolver than the ranking did")
	}
}

// TestTheCastLayerIsHandedTheResolverSectionsTwoAnswersAndNotTheSection is the other half of
// the same change. The cast layer reads exactly two things an operator states under
// `resolver:`, and it used to receive the whole section beside the resolver already built
// from it: two homes for one ceiling inside one struct, either of which a stage could read.
func TestTheCastLayerIsHandedTheResolverSectionsTwoAnswersAndNotTheSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "device:\n  name: tv\n  type: chromecast\nresolver:\n  max_height: 2160\n  ffprobe_path: /opt/ffprobe\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	play := cfg.Playback()
	if play.MaxHeight != 2160 {
		t.Errorf("the cast's ceiling is %d, want the 2160 the operator set: a cast that reads a different ceiling than the ranker did casts more than was asked for", play.MaxHeight)
	}
	if play.Transcode.FFprobePath != "/opt/ffprobe" {
		t.Errorf("the cast measures with %q, want the binary the operator named: a cast that cannot measure its source falls back on every axis it would have measured", play.Transcode.FFprobePath)
	}
}
