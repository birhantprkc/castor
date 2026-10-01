package picker

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stupside/castor/internal/device"
)

// Esc must not return a zero device.Info as the selection.
func TestEscDoesNotQuit(t *testing.T) {
	m := newModel(t.Context(), func(context.Context) []device.Info { return nil }, "")
	tm, _ := m.Update(devicesDoneMsg{devices: []device.Info{{Name: "TV", Type: "dlna", Address: "10.0.0.2"}}})
	m = tm.(model)

	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
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
