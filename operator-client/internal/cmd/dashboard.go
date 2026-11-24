package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	buildinfo "github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version"
)

const dashRefresh = 2 * time.Second

func newDashboardCommand(opts *GlobalOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "ui",
		Aliases: []string{"dashboard"},
		Short:   "Browse and manage clusters in a full-screen terminal UI",
		Long: "A k9s-style view of your clusters that refreshes every 2 seconds. Keys run\n" +
			"the common commands on the selected cluster, : runs any adbo command, and\n" +
			"? lists them all. Running adbo with no arguments in a terminal opens it too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTerminal(cmd.InOrStdin()) || !isTerminal(cmd.OutOrStdout()) {
				return errors.New("adbo ui needs a terminal; in scripts, use `adbo cluster list`")
			}
			return runDashboard(cmd, opts)
		},
	}
}

func runDashboard(cmd *cobra.Command, opts *GlobalOptions) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	d, err := newDashboard(cmd.Root(), opts, execRunner{self: self})
	if err != nil {
		return err
	}
	return d.run(cmd.Context())
}

// dashRunner runs adbo commands for the dashboard. The real one runs this
// binary again, so every command behaves exactly as it does on the command
// line; tests swap in a fake.
type dashRunner interface {
	// Output runs args and returns what they printed.
	Output(ctx context.Context, args []string) ([]byte, error)
	// Stream runs args and writes their output to w until ctx ends.
	Stream(ctx context.Context, args []string, w io.Writer) error
	// Interactive runs args on the terminal, for editors, prompts and TUIs.
	Interactive(args []string) error
}

type execRunner struct{ self string }

func (r execRunner) Output(ctx context.Context, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, r.self, args...).CombinedOutput()
}

func (r execRunner) Stream(ctx context.Context, args []string, w io.Writer) error {
	c := exec.CommandContext(ctx, r.self, args...)
	c.Stdout, c.Stderr = w, w
	return c.Run()
}

func (r execRunner) Interactive(args []string) error {
	c := exec.Command(r.self, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	// Leave the output on screen until the user has read it.
	fmt.Fprint(os.Stderr, "\nPress Enter to go back to adbo...")
	_, _ = fmt.Fscanln(os.Stdin)
	return err
}

// dashCommand is an adbo command the dashboard can run.
type dashCommand struct {
	path  string // e.g. "cluster scale"
	short string
	// takesCluster: the command's Use has [CLUSTER_NAME], so the selected
	// cluster fills it in when none is typed.
	takesCluster bool
}

// dashCommands lists every adbo command from the cobra tree, so commands
// added later show up in the dashboard without changes here.
func dashCommands(root *cobra.Command) []dashCommand {
	var out []dashCommand
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			switch {
			case sub.Hidden || sub.Deprecated != "":
				continue
			case slices.Contains([]string{"help", "completion", "ui"}, sub.Name()):
				continue
			case sub.HasSubCommands():
				walk(sub)
			case sub.Runnable():
				out = append(out, dashCommand{
					path:         strings.TrimPrefix(sub.CommandPath(), root.Name()+" "),
					short:        sub.Short,
					takesCluster: strings.Contains(sub.Use, "[CLUSTER_NAME]"),
				})
			}
		}
	}
	walk(root)
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func findDashCommand(cmds []dashCommand, path string) (dashCommand, bool) {
	for _, c := range cmds {
		if c.path == path {
			return c, true
		}
	}
	return dashCommand{}, false
}

// commandArgs turns what was typed after ":" into adbo args. A command that
// acts on one cluster gets the selected cluster when no name was typed:
// `cluster scale --cpu 4` scales the selected cluster.
func commandArgs(cmds []dashCommand, line, selected string) ([]string, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, errors.New("type a command, e.g. cluster scale --cpu 4; ? lists them")
	}
	var match dashCommand
	n := 0
	for _, c := range cmds {
		p := strings.Fields(c.path)
		if len(p) > n && len(fields) >= len(p) && slices.Equal(fields[:len(p)], p) {
			match, n = c, len(p)
		}
	}
	if n == 0 {
		return nil, fmt.Errorf("unknown command %q; ? lists them", line)
	}
	rest := fields[n:]
	if match.takesCluster && selected != "" && (len(rest) == 0 || strings.HasPrefix(rest[0], "-")) {
		rest = append([]string{selected}, rest...)
	}
	return append(fields[:n:n], rest...), nil
}

// dashKey binds a key to a command on the selected cluster. Keys whose
// command isn't in this build (it lands in another PR) are left out.
type dashKey struct {
	key   rune
	label string
	path  string
	extra []string
	how   dashHow
}

type dashHow int

const (
	showOutput dashHow = iota // run it and show what it printed
	onTerminal                // hand it the terminal (editors, prompts, TUIs)
	followLogs
	scaleForm
)

var dashKeys = []dashKey{
	{'d', "Describe", "cluster describe", nil, showOutput},
	{'y', "YAML", "cluster describe", []string{"-o", "yaml"}, showOutput},
	{'e', "Edit", "cluster edit", nil, onTerminal},
	{'i', "Edit (TUI)", "cluster edit", []string{"-i"}, onTerminal},
	{'s', "Scale", "cluster scale", nil, scaleForm},
	{'l', "Logs", "logs", nil, followLogs},
	{'b', "Backups", "backup list", nil, showOutput},
	{'t', "Status", "status", nil, showOutput},
	{'c', "Connect", "connect", nil, onTerminal},
}

// dashRow is one cluster in the table.
type dashRow struct {
	ns, name                         string
	phase, primary, version, ha      string
	cpu, memory, disk, standbys, age string
	endpoint                         string
}

func (r dashRow) cells() []string {
	return []string{r.ns, r.name, r.phase, r.primary, r.version, r.cpu, r.memory, r.disk, r.standbys, r.ha, r.endpoint, r.age}
}

var dashHeaders = []string{"NAMESPACE", "NAME", "PHASE", "PRIMARY", "VERSION", "CPU", "MEMORY", "DISK", "STANDBYS", "HA", "ENDPOINT", "AGE"}

func toDashRow(db *unstructured.Unstructured, now time.Time) dashRow {
	st := clusterStatus(db) // phase, primary, endpoint, version, HA
	field := func(path ...string) string {
		v, found, _ := unstructured.NestedFieldNoCopy(db.Object, path...)
		if !found || v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	standbys := field(standbysPath...)
	if standbys == "" {
		standbys = "0"
	}
	res := func(name string) string {
		return field(append(append([]string{}, resourcesPath...), name)...)
	}
	return dashRow{
		ns: db.GetNamespace(), name: db.GetName(),
		phase: st[0], primary: st[1], endpoint: st[2], version: st[3], ha: st[4],
		cpu: res("cpu"), memory: res("memory"), disk: diskSize(db.Object, resourcesPath),
		standbys: standbys,
		age:      duration.HumanDuration(now.Sub(db.GetCreationTimestamp().Time)),
	}
}

func filterRows(rows []dashRow, query string) []dashRow {
	if query == "" {
		return rows
	}
	var out []dashRow
	for _, r := range rows {
		if strings.Contains(r.ns+"/"+r.name+" "+r.phase, query) {
			out = append(out, r)
		}
	}
	return out
}

// k9s colors.
var (
	dashBorder  = tcell.ColorDodgerBlue
	dashRowFg   = tcell.ColorAqua
	dashPending = tcell.ColorDarkOrange
	dashError   = tcell.ColorOrangeRed
)

const dashLogo = `[orange::b]           _ _
  __ _  __| | |__   ___
 / _` + "`" + ` |/ _` + "`" + ` | '_ \ / _ \
| (_| | (_| | |_) | (_) |
 \__,_|\__,_|_.__/ \___/`

type dashboard struct {
	opts    *GlobalOptions
	clients *k8s.Clients
	runner  dashRunner
	cmds    []dashCommand
	keys    []dashKey

	app      *tview.Application
	pages    *tview.Pages
	body     *tview.Flex
	table    *tview.Table
	info     *tview.TextView
	hints    *tview.TextView
	crumbs   *tview.TextView
	flash    *tview.TextView
	filter   *tview.InputField
	cmdline  *tview.InputField
	stopView context.CancelFunc // ends the log stream behind the open view

	mu       sync.Mutex
	rows     []dashRow
	allNS    bool
	query    string
	listErr  error
	updated  time.Time
	operator string
}

func newDashboard(root *cobra.Command, opts *GlobalOptions, runner dashRunner) (*dashboard, error) {
	c, err := opts.Clients()
	if err != nil {
		return nil, err
	}
	d := &dashboard{opts: opts, clients: c, runner: runner, cmds: dashCommands(root), app: tview.NewApplication()}
	for _, k := range dashKeys {
		if _, ok := findDashCommand(d.cmds, k.path); ok {
			d.keys = append(d.keys, k)
		}
	}
	d.build()
	return d, nil
}

func (d *dashboard) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.poll(ctx)
	go d.lookupOperator(ctx)
	return d.app.Run()
}

func (d *dashboard) build() {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorBlack
	tview.Styles.ContrastBackgroundColor = tcell.ColorBlack
	tview.Styles.BorderColor = dashBorder
	tview.Styles.TitleColor = tcell.ColorAqua
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	// k9s keeps single-line borders on the focused view.
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical
	tview.Borders.TopLeftFocus = tview.Borders.TopLeft
	tview.Borders.TopRightFocus = tview.Borders.TopRight
	tview.Borders.BottomLeftFocus = tview.Borders.BottomLeft
	tview.Borders.BottomRightFocus = tview.Borders.BottomRight

	d.info = tview.NewTextView().SetDynamicColors(true)
	d.hints = tview.NewTextView().SetDynamicColors(true)
	header := tview.NewFlex().
		AddItem(d.info, 40, 0, false).
		AddItem(d.hints, 0, 1, false).
		AddItem(tview.NewTextView().SetDynamicColors(true).SetText(dashLogo), 26, 0, false)

	d.table = tview.NewTable().SetFixed(1, 0).SetSelectable(true, false).
		SetSelectedStyle(tcell.StyleDefault.Background(dashRowFg).Foreground(tcell.ColorBlack).Bold(true))
	d.table.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	d.table.SetInputCapture(d.tableKeys)

	d.filter = d.prompt(" / ", func(text string) {
		d.mu.Lock()
		d.query = text
		d.mu.Unlock()
		d.render()
	})
	d.filter.SetChangedFunc(func(text string) {
		d.mu.Lock()
		d.query = text
		d.mu.Unlock()
		d.render()
	})
	d.cmdline = d.prompt(" adbo ▸ ", d.runLine)
	d.cmdline.SetAutocompleteFunc(func(text string) []string {
		var out []string
		for _, c := range d.cmds {
			if text != "" && strings.HasPrefix(c.path, text) && c.path != strings.TrimSpace(text) {
				out = append(out, c.path)
			}
		}
		return out
	})

	d.pages = tview.NewPages().AddPage("clusters", d.table, true, true)
	d.body = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(d.pages, 0, 1, true)

	d.crumbs = tview.NewTextView().SetDynamicColors(true)
	d.flash = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	footer := tview.NewFlex().AddItem(d.crumbs, 0, 1, false).AddItem(d.flash, 0, 2, false)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 6, 0, false).
		AddItem(d.body, 0, 1, true).
		AddItem(footer, 1, 0, false)
	d.app.SetRoot(root, true).SetFocus(d.table)
	d.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyCtrlC {
			d.app.Stop()
			return nil
		}
		return ev
	})
	d.showTableHints()
	d.setCrumbs("clusters")
	d.render()
}

// prompt is a one-line input under the table; enter calls done, esc cancels.
func (d *dashboard) prompt(label string, done func(string)) *tview.InputField {
	in := tview.NewInputField().SetLabel(label).SetLabelColor(tcell.ColorOrange).
		SetFieldBackgroundColor(tcell.ColorBlack).SetFieldTextColor(tcell.ColorWhite)
	in.SetDoneFunc(func(k tcell.Key) {
		text := in.GetText()
		if k == tcell.KeyEscape {
			in.SetText("")
			text = ""
		}
		d.body.RemoveItem(in)
		d.app.SetFocus(d.table)
		if k == tcell.KeyEnter || k == tcell.KeyEscape {
			done(text)
		}
	})
	return in
}

func (d *dashboard) openPrompt(in *tview.InputField) {
	d.body.AddItem(in, 1, 0, true)
	d.app.SetFocus(in)
}

func (d *dashboard) poll(ctx context.Context) {
	t := time.NewTicker(dashRefresh)
	defer t.Stop()
	for {
		d.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *dashboard) refresh(ctx context.Context) {
	d.mu.Lock()
	ns := d.clients.Namespace
	if d.allNS {
		ns = metav1.NamespaceAll
	}
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := d.clients.InNamespace(k8s.DBClusterGVR, ns).List(ctx, metav1.ListOptions{})

	d.mu.Lock()
	d.listErr = nil
	if err != nil {
		d.listErr = k8s.Format(err, ns, d.clients.Context)
	} else {
		now := time.Now()
		rows := make([]dashRow, 0, len(list.Items))
		for i := range list.Items {
			rows = append(rows, toDashRow(&list.Items[i], now))
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].ns != rows[j].ns {
				return rows[i].ns < rows[j].ns
			}
			return rows[i].name < rows[j].name
		})
		d.rows, d.updated = rows, now
	}
	d.mu.Unlock()
	d.app.QueueUpdateDraw(d.render)
}

func (d *dashboard) lookupOperator(ctx context.Context) {
	text := "unknown"
	if op, err := findOperator(ctx, d.opts); err == nil && op != nil {
		switch {
		case op.Version != "":
			text = op.Version
		case op.Installed != nil && !*op.Installed:
			text = "not installed"
		}
	}
	d.mu.Lock()
	d.operator = text
	d.mu.Unlock()
	d.app.QueueUpdateDraw(d.render)
}

// render redraws the header and the table. It runs on the UI goroutine.
func (d *dashboard) render() {
	d.mu.Lock()
	defer d.mu.Unlock()

	ns := d.clients.Namespace
	if d.allNS {
		ns = "all"
	}
	kctx := d.clients.Context
	if kctx == "" {
		kctx = "(current)"
	}
	ready := 0
	for _, r := range d.rows {
		if r.phase == "DBClusterReady" {
			ready++
		}
	}
	operator := d.operator
	if operator == "" {
		operator = "..."
	}
	refreshed := "..."
	if !d.updated.IsZero() {
		refreshed = d.updated.Format("15:04:05")
	}
	d.info.SetText(fmt.Sprintf(
		"[orange]Context:   [white::b]%s[-::-]\n[orange]Namespace: [white::b]%s[-::-]\n[orange]Clusters:  [white::b]%d[-::-] [gray](%d ready)[-]\n[orange]Operator:  [white]%s\n[orange]adbo:      [white]%s [gray]%s",
		tview.Escape(kctx), tview.Escape(ns), len(d.rows), ready, tview.Escape(operator), buildinfo.Get().Version, refreshed))

	selected := ""
	if r, _ := d.table.GetSelection(); r > 0 && r < d.table.GetRowCount() {
		selected = d.table.GetCell(r, 0).Text + "/" + d.table.GetCell(r, 1).Text
	}
	rows := filterRows(d.rows, d.query)
	d.table.Clear()
	title := fmt.Sprintf(" [aqua::b]DBClusters[white](%s)[fuchsia][%d][-::-] ", tview.Escape(ns), len(rows))
	if d.query != "" {
		title += fmt.Sprintf("[orange]</%s> ", tview.Escape(d.query))
	}
	d.table.SetTitle(title)
	for i, h := range dashHeaders {
		d.table.SetCell(0, i, tview.NewTableCell(h).SetTextColor(tcell.ColorWhite).
			SetAttributes(tcell.AttrBold).SetSelectable(false).SetExpansion(1))
	}
	sel := 1
	for i, r := range rows {
		color := dashRowFg
		switch {
		case strings.Contains(r.phase, "Fail") || strings.Contains(r.phase, "Error"):
			color = dashError
		case r.phase != "DBClusterReady":
			color = dashPending
		}
		for j, v := range r.cells() {
			if v == "" {
				v = "-"
			}
			d.table.SetCell(i+1, j, tview.NewTableCell(tview.Escape(v)).SetTextColor(color).SetExpansion(1))
		}
		if r.ns+"/"+r.name == selected {
			sel = i + 1
		}
	}
	if len(rows) > 0 {
		d.table.Select(sel, 0)
	}
	if d.listErr != nil {
		d.flashf("[orangered]%s", tview.Escape(d.listErr.Error()))
	}
}

func (d *dashboard) selected() (dashRow, bool) {
	r, _ := d.table.GetSelection()
	if r <= 0 || r >= d.table.GetRowCount() {
		return dashRow{}, false
	}
	ns, name := d.table.GetCell(r, 0).Text, d.table.GetCell(r, 1).Text
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, row := range d.rows {
		if row.ns == ns && row.name == name {
			return row, true
		}
	}
	return dashRow{}, false
}

func (d *dashboard) tableKeys(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyEnter:
		if k, ok := d.key('d'); ok {
			d.withSelected(func(r dashRow) { d.runKey(k, r) })
		}
		return nil
	case tcell.KeyCtrlA:
		d.toggleAllNS()
		return nil
	}
	switch ev.Rune() {
	case 'q':
		d.app.Stop()
	case 'a', '0':
		d.toggleAllNS()
	case '/':
		d.openPrompt(d.filter)
	case ':':
		d.cmdline.SetText("")
		d.openPrompt(d.cmdline)
	case '?':
		d.showHelp()
	default:
		k, ok := d.key(ev.Rune())
		if !ok {
			return ev
		}
		d.withSelected(func(r dashRow) { d.runKey(k, r) })
	}
	return nil
}

func (d *dashboard) key(r rune) (dashKey, bool) {
	for _, k := range d.keys {
		if k.key == r {
			return k, true
		}
	}
	return dashKey{}, false
}

func (d *dashboard) withSelected(f func(dashRow)) {
	if r, ok := d.selected(); ok {
		f(r)
		return
	}
	d.flashf("[orange]No cluster selected")
}

func (d *dashboard) runKey(k dashKey, r dashRow) {
	args := append(strings.Fields(k.path), r.name)
	args = append(args, k.extra...)
	switch k.how {
	case showOutput:
		d.showOutput(fmt.Sprintf("%s(%s/%s)", k.label, r.ns, r.name), strings.ToLower(k.label), d.withScope(args, r.ns))
	case onTerminal:
		d.onTerminal(d.withScope(args, r.ns))
	case followLogs:
		d.logs(r, logSourcePostgres)
	case scaleForm:
		d.scaleForm(r)
	}
}

func (d *dashboard) toggleAllNS() {
	d.mu.Lock()
	d.allNS = !d.allNS
	d.mu.Unlock()
	d.render()
	go d.refresh(context.Background())
}

// withScope adds the namespace and the kube flags the dashboard was opened
// with, so the command talks to the same cluster.
func (d *dashboard) withScope(args []string, ns string) []string {
	out := append([]string{}, args...)
	if ns != "" {
		out = append(out, "-n", ns)
	}
	if d.clients.Context != "" {
		out = append(out, "--context", d.clients.Context)
	}
	if k := d.opts.KubeFlags; k != nil && k.KubeConfig != nil && *k.KubeConfig != "" {
		out = append(out, "--kubeconfig", *k.KubeConfig)
	}
	return out
}

// runLine runs what was typed after ":" on the terminal.
func (d *dashboard) runLine(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	r, ok := d.selected()
	ns := d.clients.Namespace
	if ok {
		ns = r.ns
	}
	args, err := commandArgs(d.cmds, line, r.name)
	if err != nil {
		d.flashf("[orangered]%s", tview.Escape(err.Error()))
		return
	}
	d.onTerminal(d.withScope(args, ns))
}

// onTerminal hands the terminal to an adbo command and takes it back after.
func (d *dashboard) onTerminal(args []string) {
	var err error
	d.app.Suspend(func() { err = d.runner.Interactive(args) })
	what := "adbo " + strings.Join(args, " ")
	if err != nil {
		d.flashf("[orangered]%s: %s", tview.Escape(what), tview.Escape(err.Error()))
	} else {
		d.flashf("[greenyellow]%s: done", tview.Escape(what))
	}
	go d.refresh(context.Background())
}

// detail opens a scrolling view; esc or q goes back to the table.
func (d *dashboard) detail(title, crumb, hints string) *tview.TextView {
	tv := tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWrap(false)
	tv.SetBorder(true).SetBorderPadding(0, 0, 1, 1).SetTitle(" [aqua::b]" + title + " ")
	tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || ev.Rune() == 'q' {
			d.back()
			return nil
		}
		return ev
	})
	d.pages.AddAndSwitchToPage("detail", tv, true)
	d.app.SetFocus(tv)
	d.setCrumbs("clusters", crumb)
	d.hints.SetText(hints)
	return tv
}

func (d *dashboard) back() {
	if d.stopView != nil {
		d.stopView()
		d.stopView = nil
	}
	d.pages.RemovePage("detail")
	d.pages.SwitchToPage("clusters")
	d.app.SetFocus(d.table)
	d.setCrumbs("clusters")
	d.showTableHints()
}

// showOutput runs an adbo command and shows what it printed.
func (d *dashboard) showOutput(title, crumb string, args []string) {
	tv := d.detail(title, crumb, keyHints([][2]string{{"esc", "Back"}, {"↑↓", "Scroll"}}))
	tv.SetText("[gray]loading...")
	go func() {
		out, err := d.runner.Output(context.Background(), args)
		d.app.QueueUpdateDraw(func() {
			tv.Clear()
			_, _ = tview.ANSIWriter(tv).Write(out)
			if err != nil && len(strings.TrimSpace(string(out))) == 0 {
				fmt.Fprintf(tv, "[orangered]%s", tview.Escape(err.Error()))
			}
			tv.ScrollToBeginning()
		})
	}()
}

// logs follows `adbo logs -f`; 1-3 switch the source.
func (d *dashboard) logs(r dashRow, source string) {
	if d.stopView != nil {
		d.stopView()
	}
	hints := keyHints([][2]string{{"esc", "Back"}, {"1", "postgres"}, {"2", "internal"}, {"3", "dbdaemon"}, {"↑↓", "Scroll"}})
	tv := d.detail(fmt.Sprintf("Logs(%s/%s)[orange] %s", r.ns, r.name, source), "logs", hints)
	tv.SetMaxLines(5000).SetChangedFunc(func() { d.app.Draw() })
	tv.ScrollToEnd() // follow new lines until the user scrolls up
	back := tv.GetInputCapture()
	tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if src, ok := map[rune]string{'1': logSourcePostgres, '2': logSourceInternal, '3': logSourceDBDaemon}[ev.Rune()]; ok {
			d.logs(r, src)
			return nil
		}
		return back(ev)
	})

	ctx, cancel := context.WithCancel(context.Background())
	d.stopView = cancel
	args := d.withScope([]string{"logs", r.name, "-f", "--tail", "200", "--source", source}, r.ns)
	w := tview.ANSIWriter(tv)
	go func() {
		if err := d.runner.Stream(ctx, args, w); err != nil && ctx.Err() == nil {
			d.app.QueueUpdateDraw(func() { fmt.Fprintf(tv, "\n[orangered]%s", tview.Escape(err.Error())) })
		}
	}()
}

// scaleForm edits CPU, memory, disk and standbys, then runs `cluster scale`
// with only the fields that changed.
func (d *dashboard) scaleForm(r dashRow) {
	form := tview.NewForm()
	fields := []struct{ label, flag, was string }{
		{"CPU", "--cpu", r.cpu}, {"Memory", "--memory", r.memory},
		{"Disk", "--disk", r.disk}, {"Standbys", "--standbys", r.standbys},
	}
	for _, f := range fields {
		form.AddInputField(f.label, f.was, 14, nil, nil)
	}
	closeForm := func() {
		d.pages.RemovePage("scale")
		d.app.SetFocus(d.table)
	}
	form.AddButton("Apply", func() {
		args := []string{"cluster", "scale", r.name}
		for _, f := range fields {
			v := strings.TrimSpace(form.GetFormItemByLabel(f.label).(*tview.InputField).GetText())
			if v != "" && v != f.was {
				args = append(args, f.flag, v)
			}
		}
		closeForm()
		if len(args) == 3 {
			d.flashf("[orange]Nothing to change")
			return
		}
		args = append(args, "-y", "--no-wait")
		d.showOutput(fmt.Sprintf("Scale(%s/%s)", r.ns, r.name), "scale", d.withScope(args, r.ns))
	})
	form.AddButton("Cancel", closeForm)
	form.SetCancelFunc(closeForm)
	form.SetBorder(true).SetTitle(fmt.Sprintf(" [aqua::b]Scale(%s/%s) ", r.ns, r.name))
	form.SetFieldBackgroundColor(tcell.ColorDarkSlateGray).SetButtonBackgroundColor(dashBorder).
		SetLabelColor(tcell.ColorOrange)

	modal := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(form, 13, 0, true).
			AddItem(nil, 0, 1, false), 46, 0, true).
		AddItem(nil, 0, 1, false)
	d.pages.AddPage("scale", modal, true, true)
	d.app.SetFocus(form)
}

// showHelp lists every key and every adbo command.
func (d *dashboard) showHelp() {
	tv := d.detail("Help", "help", keyHints([][2]string{{"esc", "Back"}, {"↑↓", "Scroll"}}))
	var b strings.Builder
	b.WriteString("[orange::b]Keys on the selected cluster[-::-]\n")
	fmt.Fprintf(&b, "  [dodgerblue]%-8s[white]%s\n", "<enter>", "Describe")
	for _, k := range d.keys {
		fmt.Fprintf(&b, "  [dodgerblue]%-8s[white]%s  [gray]adbo %s\n", "<"+string(k.key)+">", k.label, strings.Join(append(strings.Fields(k.path), k.extra...), " "))
	}
	b.WriteString("\n[orange::b]Run any command with :[-::-] [gray](the selected cluster fills in " + tview.Escape("[CLUSTER_NAME]") + ")\n")
	for _, c := range d.cmds {
		fmt.Fprintf(&b, "  [aqua]%-22s[white]%s\n", c.path, tview.Escape(c.short))
	}
	tv.SetText(b.String())
}

func (d *dashboard) showTableHints() {
	h := [][2]string{{"enter", "Describe"}}
	for _, k := range d.keys {
		if k.key != 'd' {
			h = append(h, [2]string{string(k.key), k.label})
		}
	}
	h = append(h, [][2]string{{":", "Any command"}, {"?", "Help"}, {"/", "Filter"}, {"a", "All namespaces"}, {"q", "Quit"}}...)
	d.hints.SetText(keyHints(h))
}

// keyHints lays hints out in columns of five rows, like k9s.
func keyHints(h [][2]string) string {
	const rows = 5
	cols := (len(h) + rows - 1) / rows
	var b strings.Builder
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if i := c*rows + r; i < len(h) {
				fmt.Fprintf(&b, "[dodgerblue::b]%-8s[gray::-]%-17s", "<"+h[i][0]+">", h[i][1])
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (d *dashboard) setCrumbs(parts ...string) {
	var b strings.Builder
	for i, p := range parts {
		bg := "dodgerblue"
		if i == len(parts)-1 {
			bg = "orange"
		}
		fmt.Fprintf(&b, "[black:%s:b] <%s> [-:-:-] ", bg, p)
	}
	d.crumbs.SetText(b.String())
}

func (d *dashboard) flashf(format string, a ...any) {
	d.flash.SetText(fmt.Sprintf(format, a...) + " ")
}
