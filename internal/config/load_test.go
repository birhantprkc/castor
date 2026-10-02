package config

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
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
		name:    "neither a name nor a host leaves the device unaddressable",
		yaml:    "device:\n  type: dlna\n",
		wantErr: true,
	}, {
		name: "a server drives no device, so it needs no device section",
		want: func(t *testing.T, cfg *Config) {
			if _, err := cfg.Target(); err == nil {
				t.Error("a config naming no device produced a cast target")
			}
		},
	}, {
		name: "a cast asks for subtitles only when they are enabled, in the language set",
		yaml: "cast:\n  delivery: serve\nresolver:\n  max_height: 720\nwhisper:\n  language: fr\n",
		want: func(t *testing.T, cfg *Config) {
			want := &castorv1.Preferences{Delivery: castorv1.Delivery_DELIVERY_SERVE, MaxHeight: 720}
			if got := cfg.Preferences(); !proto.Equal(got, want) {
				t.Errorf("asked %v, want serve at 720p and no subtitles while whisper is off", got)
			}
			cfg.Whisper.Enable = true
			if got := cfg.Preferences().GetSubtitles(); got != "fr" {
				t.Errorf("asked for subtitles in %q, want the configured language", got)
			}
		},
	}, {
		name:    "renderers reach an advertised server at a URL",
		yaml:    "server:\n  advertise: my-nas\n",
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
