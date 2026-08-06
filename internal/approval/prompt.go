package approval

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
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
// sorted by key, plus a parallel slice of the IDs to pre-select (everything,
// so bulk approval needs no per-item toggling).
func fieldSections(req PromptRequest) ([]fieldSection, [][]string) {
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
	preselected := make([][]string, len(order))
	for i, field := range order {
		items := byField[field]
		sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
		for _, it := range items {
			preselected[i] = append(preselected[i], itemID(it))
		}
		sections = append(sections, fieldSection{field: field, items: items})
	}
	return sections, preselected
}

// DefaultPrompt is the huh-based implementation. Each field type (mounts,
// environment, ports, ...) renders as a titled MultiSelect on a single
// screen; every item is pre-selected, so a full approval needs no toggling.
// The final field is an Abort/Approve button pair (Abort left and the
// default). An abort (Esc/Ctrl+C or the Abort button) is surfaced as
// "approval declined".
func DefaultPrompt(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
	if !ui.IsTTYReader(stdin) {
		return nil, fmt.Errorf("approval prompt: stdin is not a TTY")
	}

	sections, preselected := fieldSections(req)

	fields := make([]huh.Field, 0, len(sections)+1)
	sels := make([][]string, len(sections))
	for i, sec := range sections {
		opts := make([]huh.Option[string], 0, len(sec.items))
		for _, it := range sec.items {
			opts = append(opts, huh.NewOption(it.Value, itemID(it)))
		}
		sels[i] = preselected[i]
		fields = append(fields, huh.NewMultiSelect[string]().
			Title(fieldTitle(sec.field)).
			Options(opts...).
			Value(&sels[i]))
	}

	// Abort is the leftmost button (huh renders the affirmative first) and
	// the default, so approval requires an explicit toggle to Approve. The
	// accept/reject keys (y/n) would select Abort for y and Approve for n,
	// the inverse of the labels, so they are disabled; only ←/→ toggles.
	abort := true
	fields = append(fields, huh.NewConfirm().
		Title("Approve these changes?").
		Affirmative("Abort").
		Negative("Approve").
		Value(&abort))
	keymap := huh.NewDefaultKeyMap()
	keymap.Confirm.Accept = key.NewBinding(key.WithKeys("y"), key.WithDisabled())
	keymap.Confirm.Reject = key.NewBinding(key.WithKeys("n"), key.WithDisabled())

	group := huh.NewGroup(fields...).
		Title(fmt.Sprintf("Review permissions for %s", req.ProfileName)).
		Description("On launch, this profile gets access to the host resources listed below. Toggle off anything you don't want to grant, then confirm. Your choice is saved for this profile until its configuration changes.")
	form := huh.NewForm(group).WithKeyMap(keymap).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		// huh returns a specific error on user abort (Esc/Ctrl+C);
		// distinguish it from a real I/O failure.
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, fmt.Errorf("approval declined")
		}
		return nil, fmt.Errorf("approval prompt: %w", err)
	}
	if abort {
		return nil, fmt.Errorf("approval declined")
	}

	// Map the per-section selected IDs back to (field, key) choices.
	choices := map[string]map[string]bool{}
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			set = map[string]bool{}
			choices[it.Field] = set
		}
		set[it.Key] = false
	}
	for i := range sections {
		for _, id := range sels[i] {
			f, k := splitItemID(id)
			if choices[f] == nil {
				choices[f] = map[string]bool{}
			}
			choices[f][k] = true
		}
	}
	return choices, nil
}

func itemID(it SensitiveItem) string {
	return it.Field + "\x00" + it.Key
}

func splitItemID(id string) (field, key string) {
	for i := 0; i < len(id); i++ {
		if id[i] == '\x00' {
			return id[:i], id[i+1:]
		}
	}
	return id, ""
}
