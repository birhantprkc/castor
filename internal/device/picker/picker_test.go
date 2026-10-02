package picker

import (
	"bytes"
	"context"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

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

// q leaves at once, asking nothing, and selects nothing.
func TestQQuitsWithoutSelecting(t *testing.T) {
	m := newModel(t.Context(), func(context.Context) []device.Info { return nil }, "")
	tm, _ := m.Update(devicesDoneMsg{devices: []device.Info{{Name: "TV", Type: "dlna", Address: "10.0.0.2"}}})
	tm, cmd := tm.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q asked before quitting")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatal("q did not quit")
	}
	if m := tm.(model); m.selected != (device.Info{}) {
		t.Errorf("q left %+v selected, want nothing", m.selected)
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

func TestTheConfiguredDeviceIsPreselectedAndEnterCastsToIt(t *testing.T) {
	bedroom := device.Info{Name: "Bedroom", Type: "chromecast", Address: "10.0.0.9"}
	discover := func(context.Context) []device.Info {
		return []device.Info{{Name: "Living room", Type: "dlna", Address: "10.0.0.2"}, bedroom}
	}
	tm := teatest.NewTestModel(t, newModel(t.Context(), discover, "Bedroom"),
		teatest.WithInitialTermSize(80, 20),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)),
	)
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Bedroom")) }, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(model)
	golden.RequireEqual(t, []byte(ansi.Strip(final.View().Content)))
	if final.selected != bedroom {
		t.Errorf("selected %+v, want the configured %+v", final.selected, bedroom)
	}
}
