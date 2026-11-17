// SPDX-License-Identifier: GPL-3.0-or-later

package dvrweb

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"
)

// Load reads a page; ch selects the channel (1-based) of a per-channel page.
func (c *Client) Load(ctx context.Context, cred Credentials, p *Page, ch int) (*Record, error) {
	return c.Read(ctx, cred, p.Name, p.ReadChannel(ch))
}

// BuildWrite returns the parameters of a page's write request: the field set the DVR's own form sends,
// in its order, each with its edited value or else its read value. A Secret edited to "" keeps its value.
func BuildWrite(p *Page, rec *Record, ch int, edits map[string]string) ([]Param, error) {
	if p.ReadOnly {
		return nil, fmt.Errorf("%s is read only", p.Title)
	}
	known := map[string]*Field{}
	for i := range p.Fields {
		known[p.Fields[i].Name] = &p.Fields[i]
	}
	for name := range edits {
		f := known[name]
		if f == nil || f.Kind == Display || f.Kind == Hidden || f.Kind == Const {
			return nil, fmt.Errorf("%s: %q is not an editable field", p.Title, name)
		}
	}
	value := func(f *Field) (string, error) {
		if f.Name == "ch" && f.Kind == Hidden {
			return strconv.Itoa(ch), nil
		}
		if f.Kind == Const {
			return f.Value, nil
		}
		if v, ok := edits[f.Name]; ok && !(f.Kind == Secret && v == "") {
			return v, validate(f, v)
		}
		v, ok := rec.Get(f.Name)
		if !ok {
			return "", fmt.Errorf("%s: the DVR's reply has no %q", p.Title, f.Name)
		}
		return v, nil
	}
	get := func(name string) string {
		if f := known[name]; f != nil {
			v, _ := value(f)
			return v
		}
		v, _ := rec.Get(name)
		return v
	}
	var out []Param
	for i := range p.Fields {
		f := &p.Fields[i]
		if f.Kind == Display || f.When != nil && !f.When(get) {
			continue
		}
		v, err := value(f)
		if err != nil {
			return nil, err
		}
		out = append(out, Param{f.ParamName(), v})
	}
	return out, nil
}

func validate(f *Field, v string) error {
	switch f.Kind {
	case Select:
		if !slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == v }) {
			return fmt.Errorf("%s: %q is not one of the options", f.Label, v)
		}
	case Text, Secret:
		if f.Max > 0 && utf8.RuneCountInString(v) > f.Max {
			return fmt.Errorf("%s: longer than %d characters", f.Label, f.Max)
		}
		if f.Pattern != nil && !f.Pattern.MatchString(v) {
			return fmt.Errorf("%s: %q has characters the DVR does not accept", f.Label, v)
		}
	}
	return nil
}

// Change is one field that differs between two reads.
type Change struct {
	Name, Label   string
	Before, After string // "(secret)" for secret fields
}

// Diff compares two reads of a page; secret fields report only that they changed.
func Diff(p *Page, before, after *Record) []Change {
	labels := map[string]*Field{}
	for i := range p.Fields {
		labels[p.Fields[i].Name] = &p.Fields[i]
	}
	var out []Change
	for _, a := range after.Fields {
		b, _ := before.Get(a.Name)
		if b == a.Value {
			continue
		}
		c := Change{Name: a.Name, Label: a.Name, Before: b, After: a.Value}
		f := labels[a.Name]
		switch {
		case f != nil && f.Kind == Secret, IsSecret(p, a.Name):
			c.Before, c.After = "(secret)", "(secret)"
		case f == nil && len(p.Fields) > 0:
			// not part of the page's form, e.g. mail and server settings that may hold passwords
			c.Before, c.After = "(not shown)", "(not shown)"
		}
		if f != nil {
			c.Label = f.Label
		}
		out = append(out, c)
	}
	return out
}

// IsSecret reports whether a read field of the page must never be shown.
func IsSecret(p *Page, name string) bool {
	for i := range p.Fields {
		if p.Fields[i].Name == name {
			return p.Fields[i].Kind == Secret
		}
	}
	return p.SecretField != nil && p.SecretField.MatchString(name)
}
