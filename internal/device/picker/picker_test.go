package picker

import (
	"context"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stupside/castor/internal/device"
)

// Esc must not return a zero device.Info as the selection.
func TestEscDoesNotQuit(t *testing.T) {
	m := newModel(t.Context(), func(context.Context) []device.Info { return nil }, "")
	tm, _ := m.Update(devicesDoneMsg{devices: []device.Info{{Name: "TV", Type: "dlna", Address: "10.0.0.2"}}})
	m = tm.(model)

	tm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = tm.(model)
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("esc quit the device picker (list quit binding leaked through)")
		}
	}
	if m.selected != (device.Info{}) {
		t.Fatalf("esc selected %+v", m.selected)
	}
}

// A light terminal repaints the screen with the light palette's accent (#6366F1).
func TestLightBackgroundRepaints(t *testing.T) {
	m := newModel(t.Context(), func(context.Context) []device.Info { return nil }, "")
	tm, _ := m.Update(devicesDoneMsg{devices: []device.Info{{Name: "TV", Type: "dlna", Address: "10.0.0.2"}}})
	const lightAccent = "99;102;241"
	if strings.Contains(tm.(model).View().Content, lightAccent) {
		t.Fatal("light accent painted before the terminal reported its background")
	}
	tm, _ = tm.Update(tea.BackgroundColorMsg{Color: color.White})
	if !strings.Contains(tm.(model).View().Content, lightAccent) {
		t.Fatal("light background did not repaint with the light accent")
	}
}
