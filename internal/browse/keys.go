package browse

import "github.com/charmbracelet/bubbles/key"

// keyMap holds browse TUI bindings; j/k reserved for textinput on screenBrowse.
type keyMap struct {
	Up, Down         key.Binding
	PageUp, PageDown key.Binding
	Tab, ShiftTab    key.Binding
	Enter, Back      key.Binding
	Filter           key.Binding // display-only; list.Model owns the /-handling on drilldown
	Genres           key.Binding
	Sort             key.Binding
	Media            key.Binding
	Space            key.Binding
	ClearGenres      key.Binding
	OverlayMedia     key.Binding
	Help             key.Binding
	Quit             key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:           key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:         key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		PageUp:       key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Tab:          key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ShiftTab:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Enter:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "open")),
		Back:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Filter:       key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Genres:       key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("^g", "genres")),
		Sort:         key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("^s", "sort")),
		Media:        key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("^t", "movie/tv")),
		Space:        key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		ClearGenres:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear")),
		OverlayMedia: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "movie/tv")),
		Help:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:         key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("^C", "quit")),
	}
}

// screenKeys adapts keyMap to bubbles' help.KeyMap, specialized to the current screen.
type screenKeys struct {
	k        keyMap
	s        screen
	discover bool
}

func (sk screenKeys) ShortHelp() []key.Binding {
	switch sk.s {
	case screenBrowse:
		b := []key.Binding{sk.k.Up, sk.k.Down, sk.k.Tab, sk.k.Genres, sk.k.Enter}
		if sk.discover {
			b = append(b, sk.k.Sort, sk.k.Media)
		}
		return append(b, sk.k.Back, sk.k.Help, sk.k.Quit)
	case screenDrilldown:
		return []key.Binding{sk.k.Up, sk.k.Down, sk.k.Enter, sk.k.Back, sk.k.Filter, sk.k.Help, sk.k.Quit}
	}
	return nil
}

func (sk screenKeys) FullHelp() [][]key.Binding {
	nav := []key.Binding{sk.k.Up, sk.k.Down, sk.k.PageUp, sk.k.PageDown}
	switch sk.s {
	case screenBrowse:
		return [][]key.Binding{
			nav,
			{sk.k.Tab, sk.k.ShiftTab},
			{sk.k.Genres, sk.k.Sort, sk.k.Media},
			{sk.k.Enter, sk.k.Back},
			{sk.k.Help, sk.k.Quit},
		}
	case screenDrilldown:
		return [][]key.Binding{
			nav,
			{sk.k.Enter, sk.k.Back, sk.k.Filter},
			{sk.k.Help, sk.k.Quit},
		}
	}
	return nil
}
