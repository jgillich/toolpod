package scaffold

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Styles mirror huh's ThemeCharm so the folder nav looks identical to the
// huh prompts around it. Colors come straight from huh/theme.go.
var (
	fuchsia  = lipgloss.Color("#F780E2")
	cream    = lipgloss.Color("#FFFDF5")
	indigo   = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7571F9"}
	normalFg = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	green    = lipgloss.AdaptiveColor{Light: "#02BA84", Dark: "#02BF87"}
	buttonBG = lipgloss.AdaptiveColor{Light: "252", Dark: "237"}

	// One FocusedButton/BlurredButton style each, like huh's Confirm field.
	doneButtonFocused = lipgloss.NewStyle().
				Padding(0, 2).
				MarginRight(1).
				Foreground(cream).
				Background(fuchsia)
	doneButtonBlurred = lipgloss.NewStyle().
				Padding(0, 2).
				MarginRight(1).
				Foreground(normalFg).
				Background(buttonBG)
	// Field title: indigo bold — not bubbles/list's default filled box.
	folderTitle  = lipgloss.NewStyle().Foreground(indigo).Bold(true)
	folderCursor = lipgloss.NewStyle().Foreground(fuchsia)
	// Folder rows use huh's select option colors (green highlighted, normal
	// otherwise); fragment rows reuse the pair for picked/unpicked text.
	folderLabel    = lipgloss.NewStyle().Foreground(green)
	folderLabelDim = lipgloss.NewStyle().Foreground(normalFg)
	// huh MultiSelect pick prefixes.
	fragPickedPrefix   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#02CF92", Dark: "#02A877"})
	fragUnpickedPrefix = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "", Dark: "243"})
	// bubbles/help defaults are exactly what huh's ThemeCharm help uses.
	navHelp         = help.New()
	clearBelowFrame = "\x1b[0J" // erase from cursor to end of screen
)

// sectionStyle returns huh's field base: a left thick border plus 1-space pad
// around the content. The blurred variant swaps the visible border for a
// hidden one, so the content never shifts when focus moves between sections.
func sectionStyle(focused bool) lipgloss.Style {
	s := lipgloss.NewStyle().
		PaddingLeft(1).
		BorderStyle(lipgloss.ThickBorder()).
		BorderLeft(true).
		BorderForeground(lipgloss.Color("238"))
	if !focused {
		s = s.BorderStyle(lipgloss.HiddenBorder())
	}
	return s
}

// folderNavKeyBinds are the help bindings for each section, in huh's
// "↑ up • ↓ down • …" short-help format.
var (
	listKeyBinds = []key.Binding{
		key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "up")),
		key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
		key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter/space", "toggle")),
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "done")),
		key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "cancel")),
	}
	doneKeyBinds = []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "list")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit")),
		key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "cancel")),
	}
)

type navItemKind int

const (
	itemFolder navItemKind = iota
	itemFragment
)

// folderNavItem is one row of the folder-navigation list: a folder that
// expands on Enter to reveal its fragments, or a fragment toggled with Space.
type folderNavItem struct {
	label   string // folder name, or "leaf — desc" for a fragment
	display string // folder name, or the fragment's full display name
	kind    navItemKind
}

func (i folderNavItem) FilterValue() string {
	if i.kind == itemFragment {
		return i.display + " " + i.label
	}
	return i.label
}

// folderNavDelegate renders rows like huh: tight single lines with a fuchsia
// cursor on the highlighted row. Folders show a ▸/▾ expand marker; fragments
// indent below their folder and carry the MultiSelect ✓/• pick markers.
type folderNavDelegate struct {
	list.DefaultDelegate
	expanded map[string]bool
	picked   map[string]bool
}

func (d folderNavDelegate) Height() int  { return 1 }
func (d folderNavDelegate) Spacing() int { return 0 }

func (d folderNavDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it := item.(folderNavItem)
	cursor := "  "
	if index == m.Index() {
		cursor = folderCursor.Render(">") + " "
	}
	switch it.kind {
	case itemFolder:
		marker := "▸ "
		if d.expanded[it.display] {
			marker = "▾ "
		}
		label := folderLabelDim
		if index == m.Index() {
			label = folderLabel
		}
		fmt.Fprintf(w, "%s%s%s", cursor, marker, label.Render(it.label))
	case itemFragment:
		prefix := fragUnpickedPrefix.Render("• ")
		label := folderLabelDim
		if d.picked[it.display] {
			prefix = fragPickedPrefix.Render("✓ ")
			label = folderLabel
		}
		fmt.Fprintf(w, "%s  %s%s", cursor, prefix, label.Render(it.label))
	}
}

// folderNavModel is the bubbletea model for the folder-navigation screen. Like
// huh, it has two always-visible sections — the folder list and a Done button
// — switched with tab; the list scrolls internally while the button stays put.
type folderNavModel struct {
	list        list.Model
	nav         fragmentNav
	availHeight int  // list viewport rows once a WindowSizeMsg arrives (0 before)
	width       int  // content width, min 0 until a WindowSizeMsg arrives
	focused     bool // true when the Done button section is focused
	done        bool // true once an action has been chosen (frame clears on exit)
	expanded    map[string]bool
	picked      map[string]bool
	result      string
}

func newFolderNavModel(title string, nav fragmentNav) folderNavModel {
	expanded := map[string]bool{}
	picked := map[string]bool{}
	d := folderNavDelegate{expanded: expanded, picked: picked}
	d.ShowDescription = false
	items := visibleItems(nav, expanded)
	// Content-sized list: title plus one tight row per item. The Done button, a
	// blank separator, and the help render below, outside the list viewport, so
	// they stay visible even when the folder list scrolls.
	l := list.New(toListItems(items), d, 80, len(items)+1)
	l.Title = title
	l.Styles.Title = folderTitle
	l.Styles.TitleBar = lipgloss.NewStyle()
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.SetShowTitle(true)
	// Quit is handled by this model so cancelling can report back to the
	// browser loop; esc/q clear an applied filter before quitting.
	l.KeyMap.Quit = key.NewBinding(key.WithDisabled())
	return folderNavModel{list: l, nav: nav, expanded: expanded, picked: picked}
}

// visibleItems returns the rows in display order: each folder followed by its
// fragments when expanded, then the root-level fragments.
func visibleItems(nav fragmentNav, expanded map[string]bool) []folderNavItem {
	var items []folderNavItem
	for _, f := range nav.folders {
		items = append(items, folderNavItem{kind: itemFolder, label: f, display: f})
		if expanded[f] {
			items = append(items, nav.byFolder[f]...)
		}
	}
	return append(items, nav.topFrags...)
}

func toListItems(items []folderNavItem) []list.Item {
	out := make([]list.Item, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

// rebuild refreshes the list after an expand/collapse. SetItems keeps the
// cursor on the toggled folder row; the viewport grows up to the window height
// so an expanded folder's fragments show without scrolling.
func (m folderNavModel) rebuild() (folderNavModel, tea.Cmd) {
	cmd := m.list.SetItems(toListItems(visibleItems(m.nav, m.expanded)))
	m.list.SetHeight(m.listHeight())
	return m, cmd
}

func (m folderNavModel) listHeight() int {
	h := len(visibleItems(m.nav, m.expanded)) + 1
	if m.availHeight > 0 && h > m.availHeight {
		h = m.availHeight
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (m folderNavModel) Init() tea.Cmd { return nil }

func (m folderNavModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case msg.String() == "ctrl+c":
			// Take precedence over the list's ForceQuit: without this the
			// program would quit with an empty result, which the browser loop
			// misreads as "finalize" and continues the wizard.
			m.result = browserCancel
			m.done = true
			return m, tea.Quit
		case (msg.String() == "tab" || msg.String() == "shift+tab") && m.list.FilterState() == list.Unfiltered:
			m.focused = !m.focused
			return m, nil
		case msg.String() == "enter" && m.focused:
			m.result = browserDone
			m.done = true
			return m, tea.Quit
		case (msg.String() == "esc" || msg.String() == "q") && m.focused:
			m.result = browserCancel
			m.done = true
			return m, tea.Quit
		case (msg.String() == "enter" || msg.String() == " ") && !m.focused && m.list.FilterState() != list.Filtering:
			// Enter and space both toggle the highlighted row: a folder
			// expands/collapses, a fragment is picked/unpicked.
			if it, ok := m.list.SelectedItem().(folderNavItem); ok {
				switch it.kind {
				case itemFolder:
					if m.expanded[it.display] {
						delete(m.expanded, it.display)
					} else {
						m.expanded[it.display] = true
					}
					return m.rebuild()
				case itemFragment:
					if m.picked[it.display] {
						delete(m.picked, it.display)
					} else {
						m.picked[it.display] = true
					}
					return m, nil
				}
			}
		case (msg.String() == "esc" || msg.String() == "q") && !m.focused && m.list.FilterState() == list.Unfiltered:
			m.result = browserCancel
			m.done = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		m.width = msg.Width
		// Reserve rows for the blank separator, Done button, and help so
		// they never clip.
		m.availHeight = msg.Height - 4
		if m.availHeight < 1 {
			m.availHeight = 1
		}
		m.list.SetHeight(m.listHeight())
	}
	if m.focused {
		// The Done section consumes no list keys; tab/enter/esc are handled
		// above, and navigation keys are ignored while it is focused.
		return m, nil
	}
	l, cmd := m.list.Update(msg)
	m.list = l
	return m, cmd
}

func (m folderNavModel) View() string {
	if m.done {
		// The renderer moves the cursor back to the top of the frame before
		// writing this, so erasing below clears the whole folder-nav frame and
		// the next screen starts clean.
		return clearBelowFrame
	}
	// Render the list and Done sections like two huh fields in a group: the
	// focused one carries the visible border, the other a hidden one, with a
	// blank line between them and the short help below.
	var b strings.Builder
	w := m.list.Width()
	b.WriteString(sectionStyle(!m.focused).Width(w).Render(m.list.View()))
	b.WriteString("\n\n")
	btn := doneButtonBlurred
	if m.focused {
		btn = doneButtonFocused
	}
	b.WriteString(sectionStyle(m.focused).Width(w).Render(btn.Render("Done")))
	b.WriteString("\n\n")
	binds := listKeyBinds
	if m.focused {
		binds = doneKeyBinds
	}
	b.WriteString(navHelp.ShortHelpView(binds))
	return b.String()
}

// runFolderNav shows the folder-navigation screen and returns the picked
// fragment display names, sorted. Cancelling returns errNavCancelled so the
// wizard aborts like the huh prompts do.
func runFolderNav(nav fragmentNav, stdin io.Reader, stdout io.Writer) ([]string, error) {
	p := tea.NewProgram(newFolderNavModel("Fragments", nav), tea.WithInput(stdin), tea.WithOutput(stdout))
	model, err := p.Run()
	if err != nil {
		return nil, err
	}
	fm := model.(folderNavModel)
	if fm.result == browserCancel {
		return nil, errNavCancelled
	}
	return finishPicked(fm.picked), nil
}
