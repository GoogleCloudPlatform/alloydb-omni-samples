package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// isTerminal and runEditTUI are replaced in tests.
var (
	isTerminal = func(f any) bool {
		file, ok := f.(*os.File)
		return ok && term.IsTerminal(int(file.Fd()))
	}
	runEditTUI = func(m *editModel, in io.Reader, out io.Writer) (*editModel, error) {
		m.styles = newTUIStyles(lipgloss.NewRenderer(out))
		final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(in), tea.WithOutput(out), tea.WithContext(m.ctx)).Run()
		if fm, ok := final.(*editModel); ok {
			m = fm
		}
		return m, err
	}
)

// checkTimeout bounds each dry run, so a slow API server doesn't leave the
// TUI saying "checking" forever.
const checkTimeout = 15 * time.Second

func runClusterEditInteractive(cmd *cobra.Command, opts *GlobalOptions, args []string) error {
	// The TUI draws on stderr, so -o json can still go to a pipe.
	in, out := cmd.InOrStdin(), cmd.ErrOrStderr()
	if !isTerminal(in) || !isTerminal(out) {
		return errors.New("--interactive needs a terminal; without -i, `adbo cluster edit` opens $EDITOR")
	}
	name, err := opts.ClusterName(args)
	if err != nil {
		return err
	}
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	live, err := getDBCluster(ctx, c, name)
	if err != nil {
		return err
	}
	where := fmt.Sprintf("namespace %q", c.Namespace)
	if c.Context != "" {
		where += fmt.Sprintf(", context %q", c.Context)
	}

	m := newEditModel(ctx, newEditSession(live), clusterEditBackend{c: c, name: name}, where)
	m, err = runEditTUI(m, in, out)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("interrupted, so nothing was applied")
		}
		return err
	}
	if m.applied != nil {
		return printEdited(cmd, opts, m.applied, fmt.Sprintf("dbcluster %q edited", name))
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Edit cancelled, no changes made.")
	return nil
}

// editBackend is what the TUI needs from the cluster. Tests stub it.
type editBackend interface {
	// dryRun asks the API server to validate obj (schema, webhooks)
	// without storing it.
	dryRun(ctx context.Context, obj *unstructured.Unstructured) error
	apply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	get(ctx context.Context) (*unstructured.Unstructured, error)
}

type clusterEditBackend struct {
	c    *k8s.Clients
	name string
}

func (b clusterEditBackend) dryRun(ctx context.Context, obj *unstructured.Unstructured) error {
	_, err := b.c.In(k8s.DBClusterGVR).Update(ctx, obj, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	if err != nil {
		return b.c.FormatErr(err)
	}
	return nil
}

func (b clusterEditBackend) apply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	out, err := b.c.In(k8s.DBClusterGVR).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, b.c.FormatErr(err)
	}
	return out, nil
}

func (b clusterEditBackend) get(ctx context.Context) (*unstructured.Unstructured, error) {
	return getDBCluster(ctx, b.c, b.name)
}

type tuiMode int

const (
	modeBrowse tuiMode = iota
	modeEdit
	modeAdd
	modeFilter
	modeConfirmApply
	modeConfirmQuit
)

type checkState int

const (
	checkIdle checkState = iota
	checkRunning
	checkOK
	checkFailed
)

type (
	checkedMsg struct {
		seq int
		err error
	}
	appliedMsg struct {
		obj *unstructured.Unstructured
		err error
	}
	reloadedMsg struct {
		obj *unstructured.Unstructured
		err error
	}
	editorDoneMsg struct{ err error }
)

// editModel is the Bubble Tea model behind `cluster edit -i`.
type editModel struct {
	ctx     context.Context
	session *editSession
	backend editBackend
	where   string // namespace and kube context, for the title
	styles  tuiStyles

	statusTab      bool
	cursor, offset int
	width, height  int
	mode           tuiMode
	input          textinput.Model
	filter         string

	check    checkState
	checkErr error
	checkSeq int // ignores results of checks that a newer change replaced

	notice    string
	noticeBad bool

	editPath string                     // temp file while $EDITOR is open
	applied  *unstructured.Unstructured // set when the change was applied
}

func newEditModel(ctx context.Context, s *editSession, b editBackend, where string) *editModel {
	in := textinput.New()
	in.Prompt = ""
	in.Cursor.SetMode(cursor.CursorStatic)
	return &editModel{
		ctx: ctx, session: s, backend: b, where: where,
		styles: newTUIStyles(lipgloss.NewRenderer(io.Discard)),
		width:  100, height: 30, input: in,
	}
}

func (m *editModel) Init() tea.Cmd { return nil }

func (m *editModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
	case checkedMsg:
		if msg.seq == m.checkSeq {
			m.check, m.checkErr = checkOK, msg.err
			if msg.err != nil {
				m.check = checkFailed
			}
		}
	case appliedMsg:
		if msg.err == nil {
			m.applied = msg.obj
			return m, tea.Quit
		}
		if apierrors.IsConflict(msg.err) {
			m.say(true, fmt.Sprintf("dbcluster %q changed since adbo read it, so nothing was applied. Press r to reload it; your changes are re-applied on top.", m.session.live.GetName()))
		} else {
			m.say(true, "Not applied: "+msg.err.Error())
		}
	case reloadedMsg:
		if msg.err != nil {
			m.say(true, "Reload failed: "+msg.err.Error())
			return m, nil
		}
		n := len(m.session.changes())
		dropped := m.session.rebase(msg.obj)
		switch {
		case n == 0:
			m.say(false, "Reloaded.")
		case dropped > 0:
			m.say(true, fmt.Sprintf("Reloaded. %d of your changes no longer fit the cluster and were dropped; the rest are re-applied on top.", dropped))
		default:
			m.say(false, fmt.Sprintf("Reloaded. Your %s re-applied on top.", plural(n, "change is", "changes are")))
		}
		m.clamp()
		return m, m.validate()
	case editorDoneMsg:
		return m, m.editorDone(msg.err)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *editModel) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeEdit, modeAdd, modeFilter:
		return m.inputKey(k)
	case modeConfirmApply:
		m.mode = modeBrowse
		if k.String() == "y" {
			m.say(false, "Applying...")
			return m, m.applyCmd()
		}
		m.say(false, "Not applied.")
		return m, nil
	case modeConfirmQuit:
		m.mode = modeBrowse
		if k.String() == "y" {
			return m, tea.Quit
		}
		return m, nil
	}

	m.notice = ""
	rows := m.visibleRows()
	switch k.String() {
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup":
		m.cursor -= m.listHeight()
	case "pgdown":
		m.cursor += m.listHeight()
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(rows) - 1
	case "tab", "shift+tab":
		m.statusTab = !m.statusTab
		m.cursor, m.offset, m.filter = 0, 0, ""
	case "/":
		m.startInput(modeFilter, m.filter)
	case "esc":
		m.filter = ""
	case "r":
		m.say(false, "Reloading...")
		return m, m.reloadCmd()
	case "q", "ctrl+c":
		if m.session.dirty() {
			m.mode = modeConfirmQuit
			return m, nil
		}
		return m, tea.Quit
	case "enter", " ", "+", "n", "x", "delete", "u", "e", "a":
		if m.statusTab {
			m.say(false, "Status is read-only. Press tab to go back to the spec.")
			return m, nil
		}
		return m, m.specKey(k.String())
	}
	m.clamp()
	return m, nil
}

// specKey handles the keys that change the spec.
func (m *editModel) specKey(key string) tea.Cmd {
	switch key {
	case "+", "n":
		prefill := ""
		if row, ok := m.selected(); ok && len(row.path) > 1 {
			// Start next to the selected field: primarySpec.parameters.
			prefill = row.label[:strings.LastIndex(row.label, ".")+1]
		}
		m.startInput(modeAdd, prefill)
		return nil
	case "e":
		return m.openEditor()
	case "a":
		switch {
		case !m.session.dirty():
			m.say(false, "Nothing to apply.")
		case m.check == checkFailed:
			m.say(true, "The API server rejected these changes; fix the error above first.")
		default:
			m.mode = modeConfirmApply
		}
		return nil
	}

	row, ok := m.selected()
	if !ok {
		return nil
	}
	switch key {
	case "u":
		m.session.revert(row.path)
		return m.validate()
	case "x", "delete":
		if err := m.session.remove(row.path); err != nil {
			m.say(true, err.Error())
			return nil
		}
		return m.validate()
	}
	// enter or space
	if row.kind == changeRemoved {
		m.say(false, "This field is removed. Press u to restore it.")
		return nil
	}
	switch v := row.value.(type) {
	case bool:
		if err := m.session.set(row.path, strconv.FormatBool(!v)); err != nil {
			m.say(true, err.Error())
			return nil
		}
		return m.validate()
	case map[string]any, []any:
		m.say(true, errUseEditor.Error())
		return nil
	}
	if key == "enter" {
		m.startInput(modeEdit, inputValue(row.value))
	}
	return nil
}

func (m *editModel) startInput(mode tuiMode, value string) {
	m.mode = mode
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *editModel) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	mode := m.mode
	switch k.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeBrowse
		m.input.Blur()
		if mode == modeFilter {
			m.filter = ""
		}
		return m, nil
	case tea.KeyEnter:
		val := m.input.Value()
		m.mode = modeBrowse
		m.input.Blur()
		switch mode {
		case modeFilter:
			m.filter = strings.TrimSpace(val)
			m.cursor, m.offset = 0, 0
			return m, nil
		case modeEdit:
			row, ok := m.selected()
			if !ok {
				return m, nil
			}
			if err := m.session.set(row.path, val); err != nil {
				m.say(true, err.Error())
				return m, nil
			}
			return m, m.validate()
		case modeAdd:
			name, raw, found := strings.Cut(val, "=")
			if !found {
				m.say(true, "Type name = value, such as primarySpec.parameters.work_mem = 64MB")
				m.startInput(modeAdd, val)
				return m, nil
			}
			before := map[string]bool{}
			for _, r := range m.session.rows() {
				before[pathKey(r.path)] = true
			}
			if err := m.session.add(strings.TrimSpace(name), strings.TrimSpace(raw)); err != nil {
				m.say(true, err.Error())
				m.startInput(modeAdd, val)
				return m, nil
			}
			m.filter = ""
			for i, r := range m.visibleRows() {
				if !before[pathKey(r.path)] {
					m.cursor = i
					break
				}
			}
			m.clamp()
			return m, m.validate()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if mode == modeFilter {
		m.filter = strings.TrimSpace(m.input.Value())
		m.cursor, m.offset = 0, 0
	}
	return m, cmd
}

// validate starts a dry run of the current changes. Only the newest result
// is shown.
func (m *editModel) validate() tea.Cmd {
	m.checkSeq++
	m.clamp()
	if !m.session.dirty() {
		m.check, m.checkErr = checkIdle, nil
		return nil
	}
	m.check, m.checkErr = checkRunning, nil
	seq, obj, b, ctx := m.checkSeq, m.session.object(), m.backend, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		return checkedMsg{seq: seq, err: b.dryRun(ctx, obj)}
	}
}

func (m *editModel) applyCmd() tea.Cmd {
	obj, b, ctx := m.session.object(), m.backend, m.ctx
	return func() tea.Msg {
		out, err := b.apply(ctx, obj)
		return appliedMsg{obj: out, err: err}
	}
}

func (m *editModel) reloadCmd() tea.Cmd {
	b, ctx := m.backend, m.ctx
	return func() tea.Msg {
		obj, err := b.get(ctx)
		return reloadedMsg{obj: obj, err: err}
	}
}

const tuiEditorHeader = `# Edit the DBCluster and save to go back to adbo, where you can check and
# apply it. Only changes under spec are kept. Lines starting with '#' are
# ignored, and an empty file keeps things as they were.
#
`

// openEditor writes the edited DBCluster to a temp file and suspends the
// TUI while $EDITOR has it, for changes the field list can't make, such as
// adding a disk.
func (m *editModel) openEditor() tea.Cmd {
	body, err := yaml.Marshal(m.session.object().Object)
	if err != nil {
		m.say(true, err.Error())
		return nil
	}
	short := m.session.live.GetName()
	if len(short) > 64 {
		short = short[:64]
	}
	f, err := os.CreateTemp("", "adbo-edit-"+short+"-*.yaml")
	if err != nil {
		m.say(true, err.Error())
		return nil
	}
	m.editPath = f.Name()
	_, err = f.WriteString(tuiEditorHeader + string(body))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		m.say(true, err.Error())
		return nil
	}
	c, _ := editorCommand(m.editPath)
	return tea.ExecProcess(c, func(err error) tea.Msg { return editorDoneMsg{err: err} })
}

// editorDone loads the spec back from $EDITOR. Anything that doesn't parse
// leaves the session as it was.
func (m *editModel) editorDone(editorErr error) tea.Cmd {
	path := m.editPath
	m.editPath = ""
	defer os.Remove(path)
	if editorErr != nil {
		m.say(true, editorErr.Error())
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		m.say(true, err.Error())
		return nil
	}
	if onlyComments(string(raw)) {
		m.say(false, "The file was empty, so nothing changed.")
		return nil
	}
	edited, err := parseEdited(raw)
	if err == nil {
		err = sameIdentity(edited, m.session.live)
	}
	if err != nil {
		m.say(true, err.Error()+"; nothing changed")
		return nil
	}
	spec, _, err := unstructured.NestedMap(edited.Object, "spec")
	if err != nil || spec == nil {
		m.say(true, "spec is missing or not a map; nothing changed")
		return nil
	}

	before := m.session.object()
	unstructured.RemoveNestedField(before.Object, "spec")
	unstructured.RemoveNestedField(edited.Object, "spec")
	m.session.replaceSpec(spec)
	if equality.Semantic.DeepEqual(before.Object, edited.Object) {
		m.say(false, "Loaded your changes from the editor.")
	} else {
		m.say(true, "Loaded your spec changes. Changes outside spec were dropped; use `adbo cluster edit` without -i for those.")
	}
	return m.validate()
}

func (m *editModel) say(bad bool, msg string) { m.notice, m.noticeBad = msg, bad }

// visibleRows is the current tab's rows after the filter.
func (m *editModel) visibleRows() []editRow {
	var rows []editRow
	if m.statusTab {
		status, _, _ := unstructured.NestedMap(m.session.live.Object, "status")
		for _, l := range leaves(status) {
			rows = append(rows, editRow{path: l.path, label: l.label, value: l.value})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].label < rows[j].label })
	} else {
		rows = m.session.rows()
	}
	if m.filter == "" {
		return rows
	}
	f := strings.ToLower(m.filter)
	var out []editRow
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.label), f) {
			out = append(out, r)
		}
	}
	return out
}

func (m *editModel) selected() (editRow, bool) {
	rows := m.visibleRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return editRow{}, false
	}
	return rows[m.cursor], true
}

// listHeight is how many rows fit between the header and the footer.
func (m *editModel) listHeight() int {
	return max(m.height-11, 3)
}

// clamp keeps the cursor on a row and in view.
func (m *editModel) clamp() {
	n := len(m.visibleRows())
	m.cursor = min(max(m.cursor, 0), max(n-1, 0))
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = min(max(m.offset, 0), max(n-h, 0))
}

func (m *editModel) View() string {
	st := m.styles
	var b strings.Builder
	line := func(s string) { b.WriteString(truncate(s, m.width) + "\n") }

	live := m.session.live
	line(st.title.Render("adbo cluster edit "+live.GetName()) + st.dim.Render("  "+m.where))
	line(st.dim.Render(clusterSummary(live)))
	b.WriteString("\n")

	specTab, statusTab := st.tabOn.Render(" Spec "), st.tabOff.Render(" Status ")
	if m.statusTab {
		specTab, statusTab = st.tabOff.Render(" Spec "), st.tabOn.Render(" Status ")
	}
	tabs := specTab + " " + statusTab
	if m.mode == modeFilter {
		tabs += "   filter: " + m.input.View()
	} else if m.filter != "" {
		tabs += st.dim.Render("   filter: " + m.filter + " (esc clears)")
	}
	line(tabs)

	rows := m.visibleRows()
	labelW := 0
	for _, r := range rows {
		labelW = max(labelW, len(r.label))
	}
	labelW = min(labelW, max(m.width/2, 20))
	h := m.listHeight()
	for i := m.offset; i < len(rows) && i < m.offset+h; i++ {
		line(m.renderRow(rows[i], i == m.cursor, labelW))
	}
	if len(rows) == 0 {
		line(st.dim.Render("  no fields match"))
	}
	for i := len(rows); i < m.offset+h; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if !m.statusTab {
		line(m.checkLine())
	}
	if m.notice != "" {
		style := st.dim
		if m.noticeBad {
			style = st.bad
		}
		b.WriteString(wrap(style, m.notice, m.width))
	}
	switch m.mode {
	case modeEdit:
		row, _ := m.selected()
		line(st.title.Render(row.label+" = ") + m.input.View())
		line(st.dim.Render("enter sets it · esc cancels · quotes force a string"))
	case modeAdd:
		line(st.title.Render("add: ") + m.input.View())
		line(st.dim.Render("name = value, e.g. primarySpec.parameters.work_mem = 64MB · enter adds · esc cancels"))
	case modeConfirmApply:
		n := len(m.session.changes())
		line(st.title.Render(fmt.Sprintf("Apply %s to dbcluster %q? [y/N]", plural(n, "change", "changes"), live.GetName())))
		if w := m.restartWarning(); w != "" {
			b.WriteString(wrap(st.bad, w, m.width))
		}
	case modeConfirmQuit:
		line(st.title.Render(fmt.Sprintf("Discard %s and quit? [y/N]", plural(len(m.session.changes()), "change", "changes"))))
	default:
		help := "↑↓ move · enter edit · space toggle · + add · x remove · u undo · / filter · e $EDITOR · r reload · tab status · a apply · q quit"
		if m.statusTab {
			help = "↑↓ move · / filter · r reload · tab spec · q quit"
		}
		line(st.dim.Render(help))
	}
	return b.String()
}

func (m *editModel) renderRow(r editRow, selected bool, labelW int) string {
	st := m.styles
	marker, style, value, note := "  ", lipgloss.NewStyle(), formatValue(r.value), ""
	switch r.kind {
	case changeModified:
		marker, style, note = "~ ", st.mod, "was "+formatValue(r.was)
	case changeAdded:
		marker, style, note = "+ ", st.add, "added"
	case changeRemoved:
		marker, style, value, note = "- ", st.del, formatValue(r.was), "removed"
	}
	label := truncate(r.label, labelW)
	text := marker + label + strings.Repeat(" ", labelW-len([]rune(label))) + "  " + value
	if selected {
		text = st.sel.Render("›" + text)
	} else {
		text = " " + style.Render(text)
	}
	if note != "" {
		text += "  " + st.dim.Render(note)
	}
	return text
}

func (m *editModel) checkLine() string {
	st := m.styles
	n := len(m.session.changes())
	count := plural(n, "change", "changes")
	switch m.check {
	case checkRunning:
		return st.dim.Render(count + ", checking with the API server...")
	case checkOK:
		return st.good.Render("✓ "+count+", accepted by the API server (dry run).") + st.dim.Render(" Press a to apply.")
	case checkFailed:
		msg := "✗ " + count + ", rejected by the API server: " + m.checkErr.Error()
		if apierrors.IsConflict(m.checkErr) {
			msg += " Press r to reload."
		}
		return strings.TrimSuffix(wrap(st.bad, msg, m.width), "\n")
	}
	return st.dim.Render("No changes yet.")
}

// restartWarning says what applying costs when CPU or memory change, like
// `cluster scale` does.
func (m *editModel) restartWarning() string {
	restarts := false
	for _, c := range m.session.changes() {
		if len(c.path) == 3 && c.path[0] == "primarySpec" && c.path[1] == "resources" && (c.path[2] == "cpu" || c.path[2] == "memory") {
			restarts = true
		}
	}
	if !restarts {
		return ""
	}
	w := "Changing CPU or memory restarts the database."
	if n, _ := getPath(m.session.original, fieldPath{"availability", "numberOfStandbys"}); fmt.Sprint(n) == "0" || n == nil {
		w += " This cluster has no standbys, so it is unavailable until the restart finishes."
	}
	return w
}

// clusterSummary is the line under the title: where the cluster is at.
func clusterSummary(obj *unstructured.Unstructured) string {
	parts := []string{"Phase " + orDefault(nestedString(obj.Object, "status", "phase"), "unknown")}
	if p := nestedString(obj.Object, "status", "primary", "phase"); p != "" {
		parts = append(parts, "Primary "+p)
	}
	gen := obj.GetGeneration()
	if og, found, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration"); found {
		s := fmt.Sprintf("generation %d, operator has seen %d", gen, og)
		if og < gen {
			s += " (not picked up yet)"
		}
		parts = append(parts, s)
	} else if gen > 0 {
		parts = append(parts, fmt.Sprintf("generation %d", gen))
	}
	return strings.Join(parts, " · ")
}

// formatValue shows a value the way it would be typed back: strings that
// look like another type are quoted, so "200" and 200 look different.
func formatValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		if _, isString := parseScalar(v).(string); !isString || v == "" || strings.TrimSpace(v) != v {
			return strconv.Quote(v)
		}
		return v
	case map[string]any:
		if len(v) == 0 {
			return "{}"
		}
	case []any:
		if len(v) == 0 {
			return "[]"
		}
	}
	return fmt.Sprint(v)
}

// inputValue is what the edit box starts with. The field's type is kept
// when it's set, so strings don't need quotes.
func inputValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return formatValue(v)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > width-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// wrap renders msg in style, broken into lines that fit width.
func wrap(style lipgloss.Style, msg string, width int) string {
	return style.Width(max(width-1, 20)).Render(msg) + "\n"
}

type tuiStyles struct {
	title, dim, good, bad, add, mod, del, sel, tabOn, tabOff lipgloss.Style
}

func newTUIStyles(r *lipgloss.Renderer) tuiStyles {
	return tuiStyles{
		title:  r.NewStyle().Bold(true),
		dim:    r.NewStyle().Faint(true),
		good:   r.NewStyle().Foreground(lipgloss.Color("2")),
		bad:    r.NewStyle().Foreground(lipgloss.Color("1")),
		add:    r.NewStyle().Foreground(lipgloss.Color("2")),
		mod:    r.NewStyle().Foreground(lipgloss.Color("3")),
		del:    r.NewStyle().Foreground(lipgloss.Color("1")).Strikethrough(true),
		sel:    r.NewStyle().Reverse(true),
		tabOn:  r.NewStyle().Bold(true).Reverse(true),
		tabOff: r.NewStyle().Faint(true),
	}
}
