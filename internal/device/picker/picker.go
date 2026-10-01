// Package picker asks the operator which renderer to cast to, among those discovery found.
package picker

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/palette"
)

// Device blocks until device selected or quit; context cancellation doesn't interrupt raw terminal input.
func Device(ctx context.Context, discover Discover, defaultName string) (device.Info, error) {
	final, err := tea.NewProgram(newModel(ctx, discover, defaultName), tea.WithContext(ctx)).Run()
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

type model struct {
	// tea.Cmd is parameterless closure; context accessible only via model.
	ctx         context.Context
	discover    Discover
	defaultName string
	pal         palette.Palette
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
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	// Disable list quit binding; quit modal owns program exit.
	l.DisableQuitKeybindings()

	m := model{
		ctx:         ctx,
		discover:    discover,
		defaultName: defaultName,
		spin:        spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		list:        l,
		loading:     true,
	}
	m.restyle(true)
	return m
}

// restyle repaints for the terminal background; dark until the terminal reports it.
func (m *model) restyle(dark bool) {
	m.pal = palette.New(dark)
	m.spin.Style = lipgloss.NewStyle().Foreground(m.pal.Accent)
	m.list.SetDelegate(m.pal.Delegate())
	m.pal.StyleList(&m.list)
	m.list.Styles.NoItems = lipgloss.NewStyle().Foreground(m.pal.FgMuted).Padding(0, 2)
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.spin.Tick, discoverDevicesCmd(m.ctx, m.discover))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.restyle(msg.IsDark())
		return m, nil

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

	case tea.KeyPressMsg:
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

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m model) render() string {
	if m.loading {
		return m.spin.View() + lipgloss.NewStyle().Foreground(m.pal.FgMuted).Render(" Discovering devices…")
	}
	if m.err != nil && m.selected == (device.Info{}) {
		return lipgloss.NewStyle().Foreground(m.pal.Error).Bold(true).Render("error: " + m.err.Error())
	}

	if m.showQuitModal {
		return m.renderModal()
	}

	header := lipgloss.NewStyle().
		Background(m.pal.Bar).
		Foreground(m.pal.Accent).
		Bold(true).
		Width(m.w).
		Padding(0, 2).
		Render("castor  │  Select a device")

	body := m.list.View()

	cmds := []string{
		lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true).Render("j/k") + " " + lipgloss.NewStyle().Foreground(m.pal.FgMuted).Render("nav"),
		lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true).Render("↵") + " " + lipgloss.NewStyle().Foreground(m.pal.FgMuted).Render("select"),
		lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true).Render("q") + " " + lipgloss.NewStyle().Foreground(m.pal.FgMuted).Render("quit"),
	}
	cmdBar := lipgloss.NewStyle().
		Background(m.pal.Bar).
		Foreground(m.pal.FgPrimary).
		Width(m.w).
		Padding(0, 2).
		Render(strings.Join(cmds, lipgloss.NewStyle().Foreground(m.pal.Rule).Render(" · ")))

	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", cmdBar)
}

func (m model) renderModal() string {
	modalW := 44
	content := lipgloss.JoinVertical(lipgloss.Center,
		lipgloss.NewStyle().Bold(true).Foreground(m.pal.Accent).Render("Quit castor?"),
		"",
		lipgloss.JoinHorizontal(lipgloss.Center,
			lipgloss.NewStyle().Foreground(m.pal.Error).Bold(true).Render("[ Yes ]"),
			lipgloss.NewStyle().Foreground(m.pal.FgMuted).Render("  "),
			lipgloss.NewStyle().Foreground(m.pal.FgMuted).Render("[ No ]"),
		),
		"",
		lipgloss.NewStyle().Foreground(m.pal.Rule).Render("↵ / q to quit  •  esc to go back"),
	)
	box := lipgloss.NewStyle().
		Width(modalW).
		Height(9).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.pal.Accent).
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
