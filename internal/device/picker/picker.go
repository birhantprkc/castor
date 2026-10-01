// Package picker asks the operator which renderer to cast to, among those discovery found.
package picker

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/palette"
)

// Device blocks until device selected or quit; context cancellation doesn't interrupt raw terminal input.
func Device(ctx context.Context, discover Discover, defaultName string) (device.Info, error) {
	m := newModel(ctx, discover, defaultName)
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return device.Info{}, err
	}
	// Zero Info without error looks like success; require explicit selection.
	fm, ok := final.(model)
	if !ok || (fm.err == nil && fm.selected == (device.Info{})) {
		return device.Info{}, fmt.Errorf("cancelled")
	}
	return fm.selected, fm.err
}

type devicesDoneMsg struct {
	devices []device.Info
}

var pDim = lipgloss.AdaptiveColor{Light: "#D4D4D8", Dark: "#3F3F46"}

type model struct {
	// tea.Cmd is parameterless closure; context accessible only via model.
	ctx         context.Context
	discover    Discover
	defaultName string
	list        list.Model
	spin        spinner.Model
	loading     bool
	err         error
	selected    device.Info

	showQuitModal bool
	w             int
	h             int
}

type item device.Info

func (i item) Title() string { return i.Name }
func (i item) Description() string {
	return fmt.Sprintf("%s  %s", strings.ToUpper(string(i.Type)), i.Address)
}
func (i item) FilterValue() string { return i.Name }

func newModel(ctx context.Context, discover Discover, defaultName string) model {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(palette.Accent)

	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.Foreground(palette.FgPrimary)
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.Foreground(palette.FgMuted)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(palette.Accent).
		BorderForeground(palette.Accent).
		Bold(true)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(palette.FgSecondary).
		BorderForeground(palette.Accent)

	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	// Disable list quit binding; quit modal owns program exit.
	l.DisableQuitKeybindings()
	l.Styles.NoItems = lipgloss.NewStyle().Foreground(palette.FgMuted).Padding(0, 2)

	return model{
		ctx:         ctx,
		discover:    discover,
		defaultName: defaultName,
		spin:        sp,
		list:        l,
		loading:     true,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, discoverDevicesCmd(m.ctx, m.discover))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.list.SetSize(msg.Width-4, max(msg.Height-12, 5))
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case devicesDoneMsg:
		m.loading = false
		items := make([]list.Item, len(msg.devices))
		for i, d := range msg.devices {
			items[i] = item(d)
		}
		m.list.SetItems(items)
		if m.defaultName != "" {
			for i, d := range msg.devices {
				if d.Name == m.defaultName {
					m.list.Select(i)
					break
				}
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.showQuitModal {
			switch {
			case key.Matches(msg, keys.enter), key.Matches(msg, keys.quit):
				m.err = fmt.Errorf("cancelled")
				return m, tea.Quit
			case key.Matches(msg, keys.back):
				m.showQuitModal = false
				return m, nil
			}
			return m, nil
		}
		switch {
		case key.Matches(msg, keys.quit):
			m.showQuitModal = true
			return m, nil
		case key.Matches(msg, keys.enter):
			if it, ok := m.list.SelectedItem().(item); ok {
				m.selected = device.Info(it)
				return m, tea.Quit
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m model) View() string {
	if m.loading {
		return m.spin.View() + lipgloss.NewStyle().Foreground(palette.FgMuted).Render(" Discovering devices…")
	}
	if m.err != nil && m.selected == (device.Info{}) {
		return lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render("error: " + m.err.Error())
	}

	if m.showQuitModal {
		return m.renderModal()
	}

	header := lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "#F4F4F5", Dark: "#27272A"}).
		Foreground(palette.Accent).
		Bold(true).
		Width(m.w).
		Padding(0, 2).
		Render("castor  │  Select a device")

	body := m.list.View()

	cmds := []string{
		lipgloss.NewStyle().Foreground(palette.Accent).Bold(true).Render("j/k") + " " + lipgloss.NewStyle().Foreground(palette.FgMuted).Render("nav"),
		lipgloss.NewStyle().Foreground(palette.Accent).Bold(true).Render("↵") + " " + lipgloss.NewStyle().Foreground(palette.FgMuted).Render("select"),
		lipgloss.NewStyle().Foreground(palette.Accent).Bold(true).Render("q") + " " + lipgloss.NewStyle().Foreground(palette.FgMuted).Render("quit"),
	}
	cmdBar := lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "#E4E4E7", Dark: "#18181B"}).
		Foreground(palette.FgPrimary).
		Width(m.w).
		Padding(0, 2).
		Render(strings.Join(cmds, lipgloss.NewStyle().Foreground(pDim).Render(" · ")))

	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", cmdBar)
}

func (m model) renderModal() string {
	modalW := 44
	content := lipgloss.JoinVertical(lipgloss.Center,
		lipgloss.NewStyle().Bold(true).Foreground(palette.Accent).Render("Quit castor?"),
		"",
		lipgloss.JoinHorizontal(lipgloss.Center,
			lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render("[ Yes ]"),
			lipgloss.NewStyle().Foreground(palette.FgMuted).Render("  "),
			lipgloss.NewStyle().Foreground(palette.FgMuted).Render("[ No ]"),
		),
		"",
		lipgloss.NewStyle().Foreground(pDim).Render("↵ / q to quit  •  esc to go back"),
	)
	box := lipgloss.NewStyle().
		Width(modalW).
		Height(9).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette.Accent).
		Render(content)
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
}

type keyMap struct {
	enter key.Binding
	quit  key.Binding
	back  key.Binding
}

var keys = keyMap{
	enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "select")),
	quit:  key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("q", "quit")),
	back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
}

// discoverDevicesCmd prevents discovery sweeps from outliving app shutdown.
func discoverDevicesCmd(ctx context.Context, discover Discover) tea.Cmd {
	return func() tea.Msg {
		return devicesDoneMsg{devices: discover(ctx)}
	}
}

// Discover performs one device discovery sweep; family/registry bound at root.
type Discover func(ctx context.Context) []device.Info
