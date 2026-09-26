package config

import (
	"os"
	"path/filepath"
	"testing"
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
