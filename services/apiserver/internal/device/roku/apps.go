package roku

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ensureChannel guarantees Castor's own dev channel occupies the sideload slot.
func (s *session) ensureChannel(ctx context.Context, cfg Config) error {
	apps, err := s.queryApps(ctx)
	if err != nil {
		return fmt.Errorf("querying roku apps: %w", err)
	}
	if devChannelInstalled(apps) {
		return nil
	}
	if cfg.Password == "" {
		if appInstalled(apps, devAppID) {
			return errors.New("a different sideloaded channel occupies the Roku dev slot; set devices.roku.password so Castor can replace it")
		}
		return errors.New("roku channel not installed and no developer password set: enable Developer Mode on the Roku, set a web-server password, and put it in devices.roku.password")
	}
	slog.InfoContext(ctx, "sideloading roku channel", "host", s.ecp.Hostname())
	return s.sideloadChannel(ctx, cfg.Password)
}

func (s *session) queryApps(ctx context.Context) ([]byte, error) {
	return get(ctx, s.hc, s.ecp.JoinPath("query", "apps"), 1<<20)
}

type app struct {
	ID    string `xml:"id,attr"`
	Title string `xml:",chardata"`
}

func parseApps(body []byte) []app {
	var list struct {
		Apps []app `xml:"app"`
	}
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil
	}
	return list.Apps
}

func devChannelInstalled(body []byte) bool {
	for _, a := range parseApps(body) {
		if a.ID == devAppID && strings.TrimSpace(a.Title) == channelTitle {
			return true
		}
	}
	return false
}

func appInstalled(body []byte, id string) bool {
	for _, a := range parseApps(body) {
		if a.ID == id {
			return true
		}
	}
	return false
}
