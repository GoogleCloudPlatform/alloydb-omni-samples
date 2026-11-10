package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// editSession holds a DBCluster's spec while `cluster edit -i` changes it
// field by field. It never touches the cluster; the TUI sends object().
type editSession struct {
	live     *unstructured.Unstructured // as read from the cluster
	original map[string]any             // live's spec
	spec     map[string]any             // the spec with the user's changes
}

// fieldPath is a path into the spec: map keys (string) and list indexes (int).
type fieldPath []any

type changeKind int

const (
	changeNone changeKind = iota
	changeModified
	changeAdded
	changeRemoved
)

// editRow is one field of the spec, with how it differs from the cluster.
type editRow struct {
	path  fieldPath
	label string // e.g. primarySpec.resources.disks[DataDisk].size
	value any    // current value; nil when removed
	was   any    // value on the cluster, when modified or removed
	kind  changeKind
}

var (
	errNeedFieldName = errors.New("type a field name, such as primarySpec.parameters.work_mem")
	errUseEditor     = errors.New("lists and maps can't be changed here; press e to edit the YAML in $EDITOR")
)

func newEditSession(live *unstructured.Unstructured) *editSession {
	s := &editSession{}
	s.load(live)
	return s
}

func (s *editSession) load(live *unstructured.Unstructured) {
	s.live = live.DeepCopy()
	spec, _, _ := unstructured.NestedMap(s.live.Object, "spec")
	if spec == nil {
		spec = map[string]any{}
	}
	s.original = spec
	s.spec = runtime.DeepCopyJSON(spec)
}

// object is the DBCluster to send: the live object with the edited spec,
// without status and managedFields. It keeps the resourceVersion that was
// read, so a change made meanwhile is rejected with a Conflict.
func (s *editSession) object() *unstructured.Unstructured {
	obj := s.live.DeepCopy()
	unstructured.RemoveNestedField(obj.Object, "status")
	obj.SetManagedFields(nil)
	obj.Object["spec"] = runtime.DeepCopyJSON(s.spec)
	return obj
}

// rows lists every field of the spec, sorted by label. Removed fields keep
// a row so they can be restored.
func (s *editSession) rows() []editRow {
	before := map[string]leaf{}
	for _, l := range leaves(s.original) {
		before[pathKey(l.path)] = l
	}
	var rows []editRow
	seen := map[string]bool{}
	for _, l := range leaves(s.spec) {
		k := pathKey(l.path)
		seen[k] = true
		r := editRow{path: l.path, label: l.label, value: l.value}
		if o, ok := before[k]; !ok {
			r.kind = changeAdded
		} else if !equality.Semantic.DeepEqual(o.value, l.value) {
			r.kind, r.was = changeModified, o.value
		}
		rows = append(rows, r)
	}
	for k, o := range before {
		if !seen[k] {
			rows = append(rows, editRow{path: o.path, label: o.label, was: o.value, kind: changeRemoved})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].label < rows[j].label })
	return rows
}

func (s *editSession) changes() []editRow {
	var out []editRow
	for _, r := range s.rows() {
		if r.kind != changeNone {
			out = append(out, r)
		}
	}
	return out
}

func (s *editSession) dirty() bool { return len(s.changes()) > 0 }

// set changes an existing field. raw is parsed like the field's value on the
// cluster: numbers stay numbers and strings stay strings ("200" for a
// Postgres parameter), and quotes force a string.
func (s *editSession) set(p fieldPath, raw string) error {
	cur, ok := getPath(s.spec, p)
	if !ok {
		return errors.New("this field is removed; press u to restore it")
	}
	switch cur.(type) {
	case map[string]any, []any:
		return errUseEditor
	}
	like := cur
	if o, ok := getPath(s.original, p); ok {
		like = o
	}
	return setPath(s.spec, p, parseValue(raw, like))
}

// add sets a field that isn't in the spec yet, creating the maps above it.
// Under a map of strings, such as primarySpec.parameters, the value is a
// string and the rest of the name is one key, so "parameters.pgaudit.log"
// adds the pgaudit.log parameter.
func (s *editSession) add(name, raw string) error {
	segs, err := parseFieldName(name)
	if err != nil {
		return err
	}
	m := s.spec
	var p fieldPath
	for i, seg := range segs {
		child, exists := m[seg]
		if !exists {
			var like any // nil: guess the type from raw
			if stringMap(m) {
				p, like = append(p, strings.Join(segs[i:], ".")), ""
			} else {
				for _, rest := range segs[i:] {
					p = append(p, rest)
				}
			}
			return setPath(s.spec, p, parseValue(raw, like))
		}
		p = append(p, seg)
		if i == len(segs)-1 {
			return fmt.Errorf("%s is already set; select it and press enter to change it", strings.Join(segs, "."))
		}
		switch next := child.(type) {
		case map[string]any:
			m = next
		case []any:
			return errUseEditor
		default:
			return fmt.Errorf("%s is not a map, so it can't have fields", strings.Join(segs[:i+1], "."))
		}
	}
	return errNeedFieldName
}

// remove deletes a field from the spec.
func (s *editSession) remove(p fieldPath) error {
	if _, ok := getPath(s.spec, p); !ok {
		return errors.New("this field is already removed")
	}
	if _, isIndex := p[len(p)-1].(int); isIndex {
		return errUseEditor
	}
	deletePath(s.spec, p)
	s.prune()
	return nil
}

// revert puts a field back to its value on the cluster, or drops it if it
// was added.
func (s *editSession) revert(p fieldPath) {
	if o, ok := getPath(s.original, p); ok {
		_ = setPath(s.spec, p, runtime.DeepCopyJSONValue(o))
	} else {
		deletePath(s.spec, p)
	}
	s.prune()
}

// replaceSpec takes a whole spec, such as one edited in $EDITOR.
func (s *editSession) replaceSpec(spec map[string]any) { s.spec = spec }

// rebase loads a newer copy of the cluster and re-applies the user's
// changes on top of it. It returns how many changes no longer fit, such as
// a field inside a list item that is gone.
func (s *editSession) rebase(fresh *unstructured.Unstructured) (dropped int) {
	changes := s.changes()
	s.load(fresh)
	for _, c := range changes {
		if c.kind == changeRemoved {
			deletePath(s.spec, c.path)
			continue
		}
		if err := setPath(s.spec, c.path, runtime.DeepCopyJSONValue(c.value)); err != nil {
			dropped++
		}
	}
	s.prune()
	return dropped
}

// prune drops maps that are empty now but weren't empty on the cluster,
// such as the parents an add created and a revert emptied.
func (s *editSession) prune() { pruneEmpty(s.spec, s.original) }

func pruneEmpty(cur map[string]any, orig any) {
	om, _ := orig.(map[string]any)
	for k, v := range cur {
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		var oc any
		if om != nil {
			oc = om[k]
		}
		pruneEmpty(child, oc)
		if ocm, wasMap := oc.(map[string]any); len(child) == 0 && (!wasMap || len(ocm) > 0) {
			delete(cur, k)
		}
	}
}

// parseValue reads what the user typed, using like (the field's current
// value) to pick the type.
func parseValue(raw string, like any) any {
	if n := len(raw); n >= 2 && raw[0] == '"' && raw[n-1] == '"' {
		if s, err := strconv.Unquote(raw); err == nil {
			return s
		}
	}
	if n := len(raw); n >= 2 && raw[0] == '\'' && raw[n-1] == '\'' {
		return raw[1 : n-1]
	}
	switch like.(type) {
	case string:
		return raw
	case bool:
		if b, err := strconv.ParseBool(raw); err == nil {
			return b
		}
	case int64:
		// Fields such as cpu take a number or a string ("500m").
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	case float64:
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return f
		}
	default:
		return parseScalar(raw)
	}
	return raw
}

func parseScalar(raw string) any {
	switch raw {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f
	}
	return raw
}

// parseFieldName splits a field name such as primarySpec.parameters.work_mem
// or primarySpec.parameters["pgaudit.log"] into keys. A leading "spec." is
// optional. List items can't be named here.
func parseFieldName(name string) ([]string, error) {
	text := strings.TrimPrefix(strings.TrimSpace(name), "spec.")
	var segs []string
	for text != "" {
		if text[0] == '[' {
			end := strings.Index(text, "]")
			if end < 0 {
				return nil, fmt.Errorf("missing ] in %q", name)
			}
			key, err := strconv.Unquote(text[1:end])
			if err != nil {
				return nil, errUseEditor
			}
			segs = append(segs, key)
			text = strings.TrimPrefix(text[end+1:], ".")
			continue
		}
		i := strings.IndexAny(text, ".[")
		seg := text
		switch {
		case i < 0:
			text = ""
		case text[i] == '.':
			seg, text = text[:i], text[i+1:]
			if text == "" {
				return nil, errNeedFieldName
			}
		default:
			seg, text = text[:i], text[i:]
		}
		if seg == "" {
			return nil, errNeedFieldName
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return nil, errNeedFieldName
	}
	return segs, nil
}

// stringMap reports whether m holds only strings, like primarySpec.parameters.
func stringMap(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for _, v := range m {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

type leaf struct {
	path  fieldPath
	label string
	value any
}

// leaves flattens a spec into its scalar fields. Empty maps and lists are
// leaves too, so they still show up.
func leaves(spec map[string]any) []leaf {
	var out []leaf
	var walk func(v any, p fieldPath, label string)
	walk = func(v any, p fieldPath, label string) {
		switch v := v.(type) {
		case map[string]any:
			if len(v) == 0 && len(p) > 0 {
				out = append(out, leaf{p, label, v})
				return
			}
			for k, child := range v {
				walk(child, append(append(fieldPath{}, p...), k), label+keyLabel(k, len(p) == 0))
			}
		case []any:
			if len(v) == 0 {
				out = append(out, leaf{p, label, v})
				return
			}
			for i, child := range v {
				walk(child, append(append(fieldPath{}, p...), i), label+indexLabel(i, child))
			}
		default:
			out = append(out, leaf{p, label, v})
		}
	}
	walk(spec, nil, "")
	return out
}

func keyLabel(k string, first bool) string {
	switch {
	case k == "" || strings.ContainsAny(k, ".[]"):
		return "[" + strconv.Quote(k) + "]"
	case first:
		return k
	default:
		return "." + k
	}
}

// indexLabel names list items by their name or type field when they have
// one, as disks and conditions do: disks[DataDisk], conditions[Ready].
func indexLabel(i int, item any) string {
	if m, ok := item.(map[string]any); ok {
		for _, k := range []string{"name", "type"} {
			if n, ok := m[k].(string); ok && n != "" {
				return "[" + n + "]"
			}
		}
	}
	return "[" + strconv.Itoa(i) + "]"
}

// pathKey identifies a path. Keys can contain dots, so they are joined with
// a byte that can't appear in one.
func pathKey(p fieldPath) string {
	parts := make([]string, len(p))
	for i, e := range p {
		parts[i] = fmt.Sprint(e)
		if _, isIndex := e.(int); isIndex {
			parts[i] = "#" + parts[i]
		}
	}
	return strings.Join(parts, "\x00")
}

func getPath(root any, p fieldPath) (any, bool) {
	cur := root
	for _, e := range p {
		switch e := e.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[e]; !ok {
				return nil, false
			}
		case int:
			l, ok := cur.([]any)
			if !ok || e < 0 || e >= len(l) {
				return nil, false
			}
			cur = l[e]
		}
	}
	return cur, true
}

// setPath sets the value at p, creating missing maps on the way. It can't
// create list items.
func setPath(root map[string]any, p fieldPath, v any) error {
	var cur any = root
	for i, e := range p {
		last := i == len(p)-1
		switch e := e.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return fmt.Errorf("can't set %q: its parent is not a map", e)
			}
			if last {
				m[e] = v
				return nil
			}
			next, exists := m[e]
			if !exists {
				next = map[string]any{}
				m[e] = next
			}
			cur = next
		case int:
			l, ok := cur.([]any)
			if !ok || e < 0 || e >= len(l) {
				return fmt.Errorf("list item %d no longer exists", e)
			}
			if last {
				l[e] = v
				return nil
			}
			cur = l[e]
		}
	}
	return nil
}

func deletePath(root map[string]any, p fieldPath) {
	if len(p) == 0 {
		return
	}
	parent, ok := getPath(root, p[:len(p)-1])
	if !ok {
		return
	}
	if m, ok := parent.(map[string]any); ok {
		if k, ok := p[len(p)-1].(string); ok {
			delete(m, k)
		}
	}
}
