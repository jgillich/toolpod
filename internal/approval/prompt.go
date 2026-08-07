package approval

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jgillich/tpd/internal/ui"
)

// Prompt renders the interactive approval dialog and returns the user's
// choices as a map[field]set[key]bool. If stdin is not a TTY, returns an
// error.
type Prompt func(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error)

// fieldSection is one field type's (mounts, environment, ports, ...) items.
// Every section renders as a titled MultiSelect on the single approval screen.
type fieldSection struct {
	field string
	items []SensitiveItem
}

// fieldTitle is the human title shown above a field type's section.
func fieldTitle(field string) string {
	switch field {
	case "env":
		return "Environment"
	case "dbus.talk":
		return "D-Bus Talk"
	case "dbus.own":
		return "D-Bus Own"
	}
	return titleCase(field)
}

// titleCase capitalizes the first letter of each underscore/space-separated
// word; field names are single lowercase words, so this yields e.g. "Mounts".
func titleCase(s string) string {
	words := strings.Fields(strings.ReplaceAll(s, "_", " "))
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// fieldSections groups req.Items by field type and returns the sections with
// mounts first, then the remaining fields in name order, each section's items
// sorted by key. Checked state is decided later in newApprovalModel from
// PriorApproved/Benign, not here.
func fieldSections(req PromptRequest) []fieldSection {
	byField := map[string][]SensitiveItem{}
	var order []string
	for _, it := range req.Items {
		if _, ok := byField[it.Field]; !ok {
			order = append(order, it.Field)
		}
		byField[it.Field] = append(byField[it.Field], it)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i] == "mounts" {
			return order[j] != "mounts"
		}
		if order[j] == "mounts" {
			return false
		}
		return order[i] < order[j]
	})

	sections := make([]fieldSection, 0, len(order))
	for _, field := range order {
		items := byField[field]
		sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
		sections = append(sections, fieldSection{field: field, items: items})
	}
	return sections
}

// Styles mirror huh's ThemeCharm so the approval list looks identical to the
// huh prompts around it (the init wizard's folder nav does the same). Colors
// come straight from huh/theme.go; riskyDetail is the one addition.
var (
	fuchsia = lipgloss.Color("#F780E2")
	indigo  = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7571F9"}
	// normalFg is the blurred-section foreground; item keys use it so rows
	// read at full strength.
	normalFg = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	green    = lipgloss.AdaptiveColor{Light: "#02BA84", Dark: "#02BF87"}

	approvalTitle     = lipgloss.NewStyle().Foreground(indigo).Bold(true)
	approvalDesc      = lipgloss.NewStyle().Foreground(normalFg)
	approvalCursor    = lipgloss.NewStyle().Foreground(fuchsia)
	approvalChecked   = lipgloss.NewStyle().Foreground(green)
	approvalUnchecked = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "243", Dark: "243"})
	approvalDetail    = lipgloss.NewStyle().Foreground(normalFg)
	// Faint dims benign rows relative to the normal text on any background,
	// without depending on light/dark detection.
	approvalBenign = lipgloss.NewStyle().Faint(true)
	// approvalWarning highlights the biggest grants (services) in amber.
	approvalWarning = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9A5B00", Dark: "#FFB000"})
	// Scrollbar: a fuchsia thumb on a faint track.
	approvalThumb = lipgloss.NewStyle().Foreground(fuchsia)
	approvalTrack = lipgloss.NewStyle().Faint(true)
	// popupStyle is the details popup: a bordered box with no background, so
	// the list around it stays visible while the popup's own cells overwrite
	// the covered rows.
	popupStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("254")).
			Padding(0, 1).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(fuchsia)
	// bubbles/help defaults are exactly what huh's ThemeCharm help uses.
	approvalHelp    = help.New()
	clearBelowFrame = "\x1b[0J" // erase from cursor to end of screen
)

// sectionStyle returns huh's field base: a left thick border plus 1-space pad
// around the content.
func sectionStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		PaddingLeft(1).
		BorderStyle(lipgloss.ThickBorder()).
		BorderLeft(true).
		BorderForeground(lipgloss.Color("238"))
}

const (
	// approvalDescription is the huh form's subtitle, explaining what the
	// grants do and that the choice persists for the profile.
	approvalDescription = "On launch, this profile gets access to the host resources listed below. Your choice is saved for this profile until its configuration changes."
	// approvalCatW is the fixed category column width (longest tag:
	// "services").
	approvalCatW = 8
	// maxListRows caps the list viewport, sized to show a good chunk of a
	// long list while keeping the inline view below the terminal height
	// (setHeights also caps by the window, so the frame never scrolls the
	// shell on draw).
	maxListRows = 20
)

// approvalBinds are the help bindings for the list, in huh's
// "↑ up • ↓ down • …" short-help format.
var approvalBinds = []key.Binding{
	key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "up")),
	key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
	key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space", "toggle")),
	key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve all")),
	key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "deny all")),
	key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "details")),
	key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")),
	key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "cancel")),
}

// fieldTag is the short category label shown per row; dbus talk/own share
// one tag and differ via the detail column ("talk" vs "own").
func fieldTag(field string) string {
	if field == "dbus.talk" || field == "dbus.own" {
		return "dbus"
	}
	return field
}

// approvalItem is one row of the approval list. id keys it to the underlying
// row so toggling resolves correctly.
type approvalItem struct {
	item    SensitiveItem
	id      string
	checked bool
}

// FilterValue satisfies list.Item (filtering is off, but the interface
// requires it).
func (i approvalItem) FilterValue() string {
	return strings.Join([]string{i.item.Field, i.item.Key, i.item.Detail}, " ")
}

// approvalDelegate renders rows like huh: tight single lines with a fuchsia
// cursor on the highlighted row, an [x]/[ ] checkbox, and the risk-relevant
// detail right-aligned in the risk color for riskier grants.
type approvalDelegate struct {
	list.DefaultDelegate
}

func (d approvalDelegate) Height() int  { return 1 }
func (d approvalDelegate) Spacing() int { return 0 }

func (d approvalDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	renderRow(w, item.(approvalItem), index == m.Index(), m.Width())
}

// renderRow draws one approval row: cursor, checkbox, category, key, and the
// detail right-aligned. The box width is content-sized, so the row spans the
// widest row's content (capped by the terminal). Benign rows render grey.
func renderRow(w io.Writer, it approvalItem, selected bool, contentW int) {
	cursor := "  "
	if selected {
		cursor = approvalCursor.Render(">") + " "
	}
	cat := padRight(fieldTag(it.item.Field), approvalCatW)
	// Fixed layout before the key: cursor + "[x] " + category + " ", plus
	// one separating space after the key.
	fixed := lipgloss.Width(cursor) + 3 + 1 + approvalCatW + 1
	if contentW < 1 {
		contentW = 1
	}
	// The key gets the whole width; the detail takes whatever remains, so a
	// row whose key is the widest is never truncated at the natural box width.
	keyW := contentW - fixed - 1
	if keyW < 1 {
		keyW = 1
	}
	keyStr := ansi.Truncate(it.item.Key, keyW, "…")
	detailW := contentW - fixed - 1 - lipgloss.Width(keyStr)
	if detailW < 0 {
		detailW = 0
	}
	detail := it.item.Detail
	if lipgloss.Width(detail) > detailW {
		detail = ansi.Truncate(detail, detailW, "…")
	}
	detail = padLeft(detail, detailW)
	cb := "[ ]"
	if it.checked {
		cb = approvalChecked.Render("[x]")
	} else {
		cb = approvalUnchecked.Render("[ ]")
	}
	switch {
	case it.item.Warning:
		keyStr = approvalWarning.Render(keyStr)
		cat = approvalWarning.Render(cat)
		detail = approvalWarning.Render(detail)
	case it.item.Benign:
		keyStr = approvalBenign.Render(keyStr)
		cat = approvalBenign.Render(cat)
		detail = approvalBenign.Render(detail)
	default:
		detail = approvalDetail.Render(detail)
	}
	fmt.Fprintf(w, "%s%s %s %s %s", cursor, cb, cat, keyStr, detail)
}

// approvalModel is the bubbletea model for the approval screen: a scrollable
// flat list of every permission. Prior-approved items start checked; new ones
// do not. Enter submits the current selections (there is no separate
// confirmation — checking items is the explicit opt-in); esc cancels.
// Pressing d on an item opens a read-only details view for it.
type approvalModel struct {
	req        PromptRequest
	rows       []approvalItem
	list       list.Model
	width      int // window width
	height     int // window height
	boxW       int // content-sized box width (list + pane), capped by width
	inspecting bool
	done       bool
	cancelled  bool
	result     map[string]map[string]bool
}

// rowTier ranks a row's prominence for the list order: warnings first,
// then normal grants, then benign rows.
func rowTier(it SensitiveItem) int {
	switch {
	case it.Warning:
		return 0
	case it.Benign:
		return 2
	default:
		return 1
	}
}

// rowFixed is the fixed row overhead before the key and its separating
// spaces: cursor (2) + checkbox (3) + space (1) + category (8) + space (1) +
// key + space (1) + detail.
const rowFixed = 2 + 3 + 1 + approvalCatW + 1 + 1

// naturalBoxW is the box width that fits every row's key + detail without
// truncation, plus the scrollbar column and the border and padding.
func (m approvalModel) naturalBoxW() int {
	w := 0
	for _, r := range m.rows {
		n := rowFixed + lipgloss.Width(r.item.Key) + lipgloss.Width(r.item.Detail)
		if n > w {
			w = n
		}
	}
	return w + 4 // scrollbar padding + scrollbar column + border + padding
}

func newApprovalModel(req PromptRequest) approvalModel {
	sections := fieldSections(req)
	rows := make([]approvalItem, 0, len(req.Items))
	for _, sec := range sections {
		for _, it := range sec.items {
			rows = append(rows, approvalItem{
				item: it,
				id:   itemID(it),
				// Benign items are safe to default-approve; everything else
				// starts unchecked until the user opts in.
				checked: it.PriorApproved || it.Benign,
			})
		}
	}
	// Stable-sort rows by prominence: warnings (services) first, then the
	// grants worth reviewing, then benign rows at the bottom. Anything
	// unknown from a remote import lands in the middle tier.
	sort.SliceStable(rows, func(i, j int) bool {
		return rowTier(rows[i].item) < rowTier(rows[j].item)
	})
	items := make([]list.Item, len(rows))
	for i, r := range rows {
		items[i] = r
	}
	m := approvalModel{req: req, rows: rows, width: 80, height: 20}
	m.boxW = m.naturalBoxW()
	if m.boxW > m.width {
		m.boxW = m.width
	}
	d := approvalDelegate{}
	d.ShowDescription = false
	// The list renders inside a sectionStyle box (border + padding), with a
	// scrollbar column on the right, so its width is the box's item content:
	// boxW minus 4.
	l := list.New(items, d, m.boxW-4, 20)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	// Quit is handled by this model so cancelling can distinguish esc from
	// Ctrl+C.
	l.KeyMap.Quit = key.NewBinding(key.WithDisabled())
	m.list = l
	return m
}

func (m approvalModel) Init() tea.Cmd { return nil }

func (m approvalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.boxW = m.naturalBoxW()
		if m.boxW > m.width {
			m.boxW = m.width
		}
		if m.boxW < 1 {
			m.boxW = 1
		}
		lw := m.boxW - 4
		if lw < 1 {
			lw = 1
		}
		m.list.SetWidth(lw)
		m.setHeights()
	case tea.KeyMsg:
		if m.done {
			return m, nil
		}
		if msg.String() == "ctrl+c" {
			m.cancelled = true
			m.done = true
			return m, tea.Quit
		}
		if m.inspecting {
			// Read-only inspection: only back/cancel keys do anything.
			switch msg.String() {
			case "esc", "q", "d":
				m.inspecting = false
			}
			return m, nil
		}
		switch msg.String() {
		case "a":
			// Toggle: a selects all, or deselects all when everything is
			// already selected.
			if allChecked(m.rows) {
				m.setChecked(false)
			} else {
				m.setChecked(true)
			}
			return m, m.syncList()
		case "n":
			// Toggle: n deselects all, or selects all when nothing is
			// selected.
			if noneChecked(m.rows) {
				m.setChecked(true)
			} else {
				m.setChecked(false)
			}
			return m, m.syncList()
		case " ", "x":
			return m.toggleSelected()
		case "d":
			m.inspecting = true
			return m, nil
		case "enter":
			m.result = m.buildChoices()
			m.done = true
			return m, tea.Quit
		case "esc", "q":
			m.cancelled = true
			m.done = true
			return m, tea.Quit
		}
	}
	l, cmd := m.list.Update(msg)
	m.list = l
	m.setHeights()
	return m, cmd
}

func (m approvalModel) View() string {
	if m.done {
		// The renderer moves the cursor back to the top of the frame before
		// writing this, so erasing below clears the whole approval frame and
		// the launch output starts clean.
		return clearBelowFrame
	}
	// The title and description span the window; the list is boxed at the
	// content-sized boxW, which grows to fit the widest row (capped by the
	// window) instead of stretching short rows.
	var b strings.Builder
	w := m.width
	if w < 1 {
		w = 80
	}
	b.WriteString(approvalTitle.Render("Review permissions for " + m.req.ProfileName))
	b.WriteString("\n")
	b.WriteString(approvalDesc.Width(w).Render(approvalDescription))
	b.WriteString("\n\n")
	b.WriteString(sectionStyle().Width(m.boxW).Render(m.renderRows()))
	b.WriteString("\n\n")
	b.WriteString(approvalHelp.ShortHelpView(approvalBinds))
	// One trailing blank line, like huh, so the cursor rests on a clean row.
	b.WriteString("\n")
	if m.inspecting {
		// Overlay the details popup on top of the list frame.
		return overlayPopup(b.String(), m.popupBox(), w)
	}
	return b.String()
}

// renderRows draws the visible rows as a sliding window that always fills the
// list viewport, so the last page is full instead of trailing blank rows.
// The window hugs the items when they fit; otherwise it scrolls lazily —
// the cursor moves within the window until it reaches the edge, then the
// window shifts, end-aligned at the bottom. Each row carries a scrollbar
// cell on the right so it's clear more items exist above/below.
func (m approvalModel) renderRows() string {
	items, start, end, idx := m.window()
	if len(items) == 0 {
		return ""
	}
	bar := newScrollBar(len(items), start, end-start)
	var b strings.Builder
	for i := start; i < end; i++ {
		renderRow(&b, items[i].(approvalItem), i == idx, m.list.Width())
		b.WriteString(bar.cell(i - start))
		if i != end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// window returns the visible slice of items plus the cursor's absolute index.
// The window hugs the items when they fit; otherwise it scrolls lazily and
// stays end-aligned at the bottom.
func (m approvalModel) window() (items []list.Item, start, end, idx int) {
	items = m.list.VisibleItems()
	avail := m.list.Height()
	if avail < 1 {
		avail = 1
	}
	idx = m.list.Index()
	if len(items) <= avail {
		return items, 0, len(items), idx
	}
	start = min(max(idx-(avail-1), 0), len(items)-avail)
	return items, start, start + avail, idx
}

// scrollBar is a 1-column scrollbar for the list box: a thumb sized and
// positioned by the visible window within the full list.
type scrollBar struct {
	start, end int
	active     bool
}

func newScrollBar(total, start, win int) scrollBar {
	if total <= win {
		return scrollBar{}
	}
	// Fixed 1-row thumb that tracks the window's position in the list.
	maxStart := win - 1
	pos := 0
	if total > win {
		pos = start * maxStart / (total - win)
	}
	return scrollBar{start: pos, end: pos + 1, active: true}
}

func (s scrollBar) cell(row int) string {
	if !s.active {
		return "  "
	}
	if row >= s.start && row < s.end {
		return " " + approvalThumb.Render("█")
	}
	return " " + approvalTrack.Render("│")
}

// setChecked marks every row checked or unchecked.
func (m *approvalModel) setChecked(v bool) {
	for i := range m.rows {
		m.rows[i].checked = v
	}
}

func allChecked(rows []approvalItem) bool {
	for _, r := range rows {
		if !r.checked {
			return false
		}
	}
	return true
}

func noneChecked(rows []approvalItem) bool {
	for _, r := range rows {
		if r.checked {
			return false
		}
	}
	return true
}

// toggleSelected flips the highlighted row, resolving it by id so the right
// item is toggled.
func (m approvalModel) toggleSelected() (tea.Model, tea.Cmd) {
	sel, ok := m.list.SelectedItem().(approvalItem)
	if !ok {
		return m, nil
	}
	for i := range m.rows {
		if m.rows[i].id == sel.id {
			m.rows[i].checked = !m.rows[i].checked
			return m, m.syncList()
		}
	}
	return m, nil
}

// syncList copies the authoritative checked state into the list so rows
// re-render. SetItems preserves the cursor.
func (m *approvalModel) syncList() tea.Cmd {
	items := make([]list.Item, len(m.rows))
	for i, r := range m.rows {
		items[i] = r
	}
	return m.list.SetItems(items)
}

// buildChoices maps every row (checked or not) back to the
// map[field]set[key]bool contract, so the result is always complete and the
// fail-closed completeness check in Launch is trivially satisfied.
func (m approvalModel) buildChoices() map[string]map[string]bool {
	choices := map[string]map[string]bool{}
	for _, r := range m.rows {
		set, ok := choices[r.item.Field]
		if !ok {
			set = map[string]bool{}
			choices[r.item.Field] = set
		}
		set[r.item.Key] = r.checked
	}
	return choices
}

// setHeights sizes the list viewport: it hugs the items up to maxListRows
// (huh's multi-select height), capped by the window minus the chrome and one
// trailing blank line. A taller box would push the inline view past the
// bottom of the terminal and scroll the shell.
func (m *approvalModel) setHeights() {
	overhead := m.descLines() + 4    // title, description, blanks, and help
	avail := m.height - overhead - 1 // leave one blank line below
	if avail < 1 {
		avail = 1
	}
	rows := len(m.list.VisibleItems())
	if rows > maxListRows {
		rows = maxListRows
	}
	if rows > avail {
		rows = avail
	}
	if rows < 1 {
		rows = 1
	}
	m.list.SetHeight(rows)
}

// descLines is how many rows the wrapped description occupies at the window
// width.
func (m approvalModel) descLines() int {
	w := m.width
	if w < 1 {
		return 1
	}
	return strings.Count(approvalDesc.Width(w).Render(approvalDescription), "\n") + 1
}

// detailsContent is the highlighted item's full value for the details popup:
// the formatted multi-line Body when set (services), else the pre-expansion
// Value.
func (m approvalModel) detailsContent() string {
	it, _ := m.list.SelectedItem().(approvalItem)
	if it.item.Body != "" {
		return it.item.Body
	}
	return it.item.Value
}

// popupBox renders the details popup: a header (category, key, and the
// contributing entry) above the full value, wrapped to a fixed content width,
// with an esc-close hint. All plain text — popupStyle colors it as a solid
// box so it masks the list behind it.
func (m approvalModel) popupBox() string {
	it, _ := m.list.SelectedItem().(approvalItem)
	contentW := 60
	if m.width > 0 && contentW > m.width-6 {
		contentW = m.width - 6
	}
	if contentW < 20 {
		contentW = 20
	}
	header := fieldTag(it.item.Field) + " " + it.item.Key
	if it.item.Source.FullName != "" {
		header += "  · from " + it.item.Source.FullName
	}
	lines := strings.Split(lipgloss.NewStyle().Width(contentW).Render(m.detailsContent()), "\n")
	if len(lines) > 10 {
		lines = lines[:10]
		lines[9] = ansi.Truncate(lines[9], contentW-1, "…")
	}
	body := header + "\n\n" + strings.Join(lines, "\n") + "\n\n" + "esc close"
	return popupStyle.Width(contentW + 4).Render(body)
}

// overlayPopup splices a popup box onto a base frame of the given width,
// centered vertically. The base rows the popup covers become plain text
// outside the popup (the popup's background masks them); every other row keeps
// its styling.
func overlayPopup(base string, popup string, width int) string {
	baseLines := padLinesTo(strings.Split(base, "\n"), width)
	popupLines := strings.Split(popup, "\n")
	popupW := 0
	for _, l := range popupLines {
		if w := lipgloss.Width(l); w > popupW {
			popupW = w
		}
	}
	popupH := len(popupLines)
	x := (width - popupW) / 2
	if x < 0 {
		x = 0
	}
	y := (len(baseLines) - popupH) / 2
	if y < 0 {
		y = 0
	}
	for k := 0; k < popupH && y+k < len(baseLines); k++ {
		plain := []rune(ansi.Strip(baseLines[y+k]))
		var b strings.Builder
		if x > 0 && x <= len(plain) {
			b.WriteString(string(plain[:x]))
		}
		b.WriteString(padToWidth(popupLines[k], popupW))
		if x+popupW < len(plain) {
			b.WriteString(string(plain[x+popupW:]))
		}
		baseLines[y+k] = b.String()
	}
	return strings.Join(baseLines, "\n")
}

func padToWidth(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLinesTo(lines []string, w int) []string {
	for i, l := range lines {
		lines[i] = padToWidth(l, w)
	}
	return lines
}

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// DefaultPrompt is the bubbletea implementation: a scrollable flat list of
// every sensitive item, with previously approved items pre-checked and newly
// introduced ones unchecked. Enter submits the visible state as the choices
// map; esc/Ctrl+C aborts and surfaces as "approval declined".
func DefaultPrompt(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
	if !ui.IsTTYReader(stdin) {
		return nil, fmt.Errorf("approval prompt: stdin is not a TTY")
	}
	// Inline rendering (no alt screen): the shell stays visible around the
	// prompt. The box height is capped below the terminal height so the view
	// never scrolls the shell on redraw.
	p := tea.NewProgram(newApprovalModel(req), tea.WithInput(stdin), tea.WithOutput(stdout))
	model, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("approval prompt: %w", err)
	}
	am := model.(approvalModel)
	if am.cancelled {
		return nil, fmt.Errorf("approval declined")
	}
	return am.result, nil
}

func itemID(it SensitiveItem) string {
	return it.Field + "\x00" + it.Key
}
