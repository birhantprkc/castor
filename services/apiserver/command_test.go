package apiserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/settings"
)

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

func TestEveryKeyTheAPIServerReadsLandsWhereItReadsIt(t *testing.T) {
	for k, v := range map[string]string{
		"CASTOR_CAST__DELIVERY":          "serve",
		"CASTOR_CAST__MAX_HEIGHT":        "720",
		"CASTOR_CAST__SUBTITLES":         "fr",
		"CASTOR_API__LISTEN":             ":9411",
		"CASTOR_API__TOKEN":              "api-secret",
		"CASTOR_SERVER__URL":             "http://my-nas:8410",
		"CASTOR_SERVER__TOKEN":           "server-secret",
		"CASTOR_NETWORK__TIMEOUT":        "15s",
		"CASTOR_DEVICES__ROKU__APP_ID":   "dev",
		"CASTOR_DEVICES__ROKU__PASSWORD": "roku-secret",
	} {
		t.Setenv(k, v)
	}
	cfg, err := settings.Read(shipped(t), defaults())
	if err != nil {
		t.Fatal(err)
	}
	for key, ok := range map[string]bool{
		"cast.delivery":         cfg.Cast.Delivery == "serve",
		"cast.max_height":       cfg.Cast.MaxHeight == 720,
		"cast.subtitles":        cfg.Cast.Subtitles == "fr",
		"api.listen":            cfg.API.Listen == ":9411",
		"api.token":             cfg.API.Token == "api-secret",
		"server.url":            cfg.Server.URL == "http://my-nas:8410",
		"server.token":          cfg.Server.Token == "server-secret",
		"network.timeout":       cfg.Network.Timeout == 15*time.Second,
		"devices.roku.app_id":   cfg.Devices.Roku.AppID == "dev",
		"devices.roku.password": cfg.Devices.Roku.Password == "roku-secret",
	} {
		if !ok {
			t.Errorf("%s did not land where the API server reads it", key)
		}
	}
}

func TestCastDefaultsAreLeftForTheAPIServerToHoldToTheContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("cast:\n  delivery: fast\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := settings.Read(path, defaults())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cast.Delivery != "fast" {
		t.Errorf("cast.delivery = %q, want it as written, for apiserver.New to refuse", cfg.Cast.Delivery)
	}
}
