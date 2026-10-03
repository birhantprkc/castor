package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stupside/castor/internal/settings"
)

func TestEveryKeyTheCommandLineReadsLandsWhereItReadsIt(t *testing.T) {
	data, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"CASTOR_DEVICE__HOST":  "10.0.0.9",
		"CASTOR_TMDB__API_KEY": "key",
		"CASTOR_API__URL":      "http://my-nas:8411",
		"CASTOR_API__TOKEN":    "api-secret",
	} {
		t.Setenv(k, v)
	}
	cfg, err := settings.Read(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for key, ok := range map[string]bool{
		"device.name":  cfg.Device.Name == "Samsung Q80CA 55 TV",
		"device.type":  cfg.Device.Type == "dlna",
		"device.host":  cfg.Device.Host == "10.0.0.9",
		"sources":      len(cfg.Sources.MovieURLs("tt1")) == 2,
		"tmdb.api_key": cfg.TMDB.APIKey == "key",
		"api.url":      cfg.API.URL == "http://my-nas:8411",
		"api.token":    cfg.API.Token == "api-secret",
	} {
		if !ok {
			t.Errorf("%s did not land where the command line reads it", key)
		}
	}
}

func TestADeviceNamedNeitherByNameNorByHostIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("device:\n  type: dlna\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Read(path, Config{}); err == nil {
		t.Error("a device castor cannot find by name or address was accepted")
	}
}
