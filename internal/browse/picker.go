package browse

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
)

// PickDevice blocks until device selected or quit; context cancellation doesn't interrupt raw terminal input.
func PickDevice(ctx context.Context, discover Discover, defaultName string) (device.Info, error) {
	m := newPickerModel(ctx, discover, defaultName)
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return device.Info{}, err
	}
	// Zero Info without error looks like success; require explicit selection.
	fm, ok := final.(pickerModel)
	if !ok || (fm.err == nil && fm.selected == (device.Info{})) {
		return device.Info{}, fmt.Errorf("cancelled")
	}
	return fm.selected, fm.err
}

type devicesDoneMsg struct {
	devices []device.Info
}

var pDim = lipgloss.AdaptiveColor{Light: "#D4D4D8", Dark: "#3F3F46"}

type pickerModel struct {
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

type pickerItem device.Info

func (i pickerItem) Title() string { return i.Name }
func (i pickerItem) Description() string {
	return fmt.Sprintf("%s  %s", strings.ToUpper(string(i.Type)), i.Address)
}
func (i pickerItem) FilterValue() string { return i.Name }

func newPickerModel(ctx context.Context, discover Discover, defaultName string) pickerModel {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(accent)

	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.Foreground(fgPrimary)
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.Foreground(fgMuted)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(accent).
		BorderForeground(accent).
		Bold(true)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(fgSecondary).
		BorderForeground(accent)

	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	// Disable list quit binding; quit modal owns program exit.
	l.DisableQuitKeybindings()
	l.Styles.NoItems = lipgloss.NewStyle().Foreground(fgMuted).Padding(0, 2)

	return pickerModel{
		ctx:         ctx,
		discover:    discover,
		defaultName: defaultName,
		spin:        sp,
		list:        l,
		loading:     true,
	}
}

func (m pickerModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, discoverDevicesCmd(m.ctx, m.discover))
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			items[i] = pickerItem(d)
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
			case key.Matches(msg, pickKeys.Enter), key.Matches(msg, pickKeys.Quit):
				m.err = fmt.Errorf("cancelled")
				return m, tea.Quit
			case key.Matches(msg, pickKeys.Back):
				m.showQuitModal = false
				return m, nil
			}
			return m, nil
		}
		switch {
		case key.Matches(msg, pickKeys.Quit):
			m.showQuitModal = true
			return m, nil
		case key.Matches(msg, pickKeys.Enter):
			if it, ok := m.list.SelectedItem().(pickerItem); ok {
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

func (m pickerModel) View() string {
	if m.loading {
		return m.spin.View() + lipgloss.NewStyle().Foreground(fgMuted).Render(" Discovering devices…")
	}
	if m.err != nil && m.selected == (device.Info{}) {
		return lipgloss.NewStyle().Foreground(errorColor).Bold(true).Render("error: " + m.err.Error())
	}

	if m.showQuitModal {
		return m.renderModal()
	}

	header := lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "#F4F4F5", Dark: "#27272A"}).
		Foreground(accent).
		Bold(true).
		Width(m.w).
		Padding(0, 2).
		Render("castor  │  Select a device")

	body := m.list.View()

	cmds := []string{
		lipgloss.NewStyle().Foreground(accent).Bold(true).Render("j/k") + " " + lipgloss.NewStyle().Foreground(fgMuted).Render("nav"),
		lipgloss.NewStyle().Foreground(accent).Bold(true).Render("↵") + " " + lipgloss.NewStyle().Foreground(fgMuted).Render("select"),
		lipgloss.NewStyle().Foreground(accent).Bold(true).Render("q") + " " + lipgloss.NewStyle().Foreground(fgMuted).Render("quit"),
	}
	cmdBar := lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "#E4E4E7", Dark: "#18181B"}).
		Foreground(fgPrimary).
		Width(m.w).
		Padding(0, 2).
		Render(strings.Join(cmds, lipgloss.NewStyle().Foreground(pDim).Render(" · ")))

	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", cmdBar)
}

func (m pickerModel) renderModal() string {
	modalW := 44
	content := lipgloss.JoinVertical(lipgloss.Center,
		lipgloss.NewStyle().Bold(true).Foreground(accent).Render("Quit castor?"),
		"",
		lipgloss.JoinHorizontal(lipgloss.Center,
			lipgloss.NewStyle().Foreground(errorColor).Bold(true).Render("[ Yes ]"),
			lipgloss.NewStyle().Foreground(fgMuted).Render("  "),
			lipgloss.NewStyle().Foreground(fgMuted).Render("[ No ]"),
		),
		"",
		lipgloss.NewStyle().Foreground(pDim).Render("↵ / q to quit  •  esc to go back"),
	)
	box := lipgloss.NewStyle().
		Width(modalW).
		Height(9).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Render(content)
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
}

type pickerKeyMap struct {
	Enter key.Binding
	Quit  key.Binding
	Back  key.Binding
}

var pickKeys = pickerKeyMap{
	Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "select")),
	Quit:  key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("q", "quit")),
	Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
}

// discoverDevicesCmd prevents discovery sweeps from outliving app shutdown.
func discoverDevicesCmd(ctx context.Context, discover Discover) tea.Cmd {
	return func() tea.Msg {
		return devicesDoneMsg{devices: discover(ctx)}
	}
}

// Discover performs one device discovery sweep; family/registry bound at root.
type Discover func(ctx context.Context) []device.Info
