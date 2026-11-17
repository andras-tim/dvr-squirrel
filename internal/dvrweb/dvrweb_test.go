// SPDX-License-Identifier: GPL-3.0-or-later

package dvrweb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
)

func fld(name, value string) fakedvr.Field { return fakedvr.Field{Name: name, Value: value} }

func newFake(t *testing.T) (*fakedvr.WebServer, *Client) {
	t.Helper()
	admin := fakedvr.WebAccount{Password: "adminpw"}
	for i := range admin.Perms {
		admin.Perms[i] = true
	}
	viewer := fakedvr.WebAccount{Password: "viewpw"}
	viewer.Perms[8] = true
	s := &fakedvr.WebServer{Accounts: map[string]fakedvr.WebAccount{"admin": admin, "viewer": viewer}}
	s.SetRecord("Screen", 0, fld("auto", "1"), fld("type", "1"), fld("bord", "0"), fld("md", "1"), fld("vga", "1"))
	s.SetRecord("RecSet", 2, fld("ar", "3"), fld("pgu", "0"), fld("fvk", "1"), fld("video", "2"), fld("resol", "2"), fld("frame", "25"), fld("perf", "1"))
	s.SetRecord("Network", 0, fld("act", "1"), fld("ipt", "0"), fld("ppp", "pppsecret"), fld("ddnpw", "ddnssecret"), fld("spwd", "mailsecret"))
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, &Client{BaseURL: srv.URL}
}

var (
	admin  = Credentials{"admin", "adminpw"}
	viewer = Credentials{"viewer", "viewpw"}
)

func TestParse(t *testing.T) {
	rec, code, err := parse([]byte(`<?xml version="1.0"?><r><lang>0</lang><name>a &amp; b</name><empty></empty></r>`))
	if err != nil || code != 0 || len(rec.Fields) != 3 || rec.Fields[1] != (Param{"name", "a & b"}) || rec.Fields[2].Value != "" {
		t.Fatalf("%+v %d %v", rec, code, err)
	}
	if rec, code, err := parse([]byte(`<r>30</r>`)); rec != nil || code != 30 || err != nil {
		t.Fatalf("%+v %d %v", rec, code, err)
	}
	for _, bad := range []string{`<r><a><b>1</b></a></r>`, `<r>x</r>`, `<r>`} {
		if _, _, err := parse([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestReadPermsAndDenied(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()
	rec, err := c.Read(ctx, admin, "RecSet", 2)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := rec.Get("frame"); v != "25" {
		t.Fatalf("%+v", rec)
	}
	if _, err := c.Read(ctx, viewer, "Screen", 0); !IsDenied(err) {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := c.Read(ctx, Credentials{"admin", "wrong"}, "Screen", 0); !IsDenied(err) {
		t.Fatalf("wrong password: %v", err)
	}
	p, err := c.Perms(ctx, viewer)
	if err != nil || !p[8] || p[7] || p[0] {
		t.Fatalf("viewer perms %v %v", p, err)
	}
	if p, _ := c.Perms(ctx, Credentials{"viewer", "nope"}); p != [MaxPerm + 1]bool{} {
		t.Fatalf("wrong password perms %v", p)
	}
}

func TestNoQueryString(t *testing.T) {
	s, _ := newFake(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/gw/Port.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "<r>11</r>") {
		t.Fatal(string(b))
	}
	if _, code, _ := parse(b); code != CodeNoQuery {
		t.Fatal(code)
	}
}

func TestBuildWrite(t *testing.T) {
	rec := &Record{Fields: []Param{{"lang", "0"}, {"auto", "1"}, {"type", "1"}, {"bord", "0"}, {"md", "1"}, {"vga", "1"}}}
	screen := FindPage("Screen")

	// auto=1 disables "type" in the DVR's form, so it is not sent
	got, err := BuildWrite(screen, rec, 0, map[string]string{"bord": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "auto=1 bord=1 md=1 vga=1"; joinParams(got) != want {
		t.Fatalf("got %s, want %s", joinParams(got), want)
	}
	got, _ = BuildWrite(screen, rec, 0, map[string]string{"auto": "0"})
	if want := "auto=0 type=1 bord=0 md=1 vga=1"; joinParams(got) != want {
		t.Fatalf("got %s, want %s", joinParams(got), want)
	}

	for name, edits := range map[string]map[string]string{
		"bad option": {"vga": "9"},
		"unknown":    {"nope": "1"},
		"display":    {"lang": "1"},
	} {
		if _, err := BuildWrite(screen, rec, 0, edits); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := BuildWrite(FindPage("CgiAuth"), rec, 0, nil); err == nil {
		t.Error("read-only page written")
	}
	if _, err := BuildWrite(screen, &Record{}, 0, nil); err == nil {
		t.Error("missing fields not reported")
	}
}

func TestBuildWriteSpecialNames(t *testing.T) {
	// View: read as recl, written as srecl
	view := FindPage("View")
	var rec Record
	for _, f := range view.Fields {
		rec.Fields = append(rec.Fields, Param{f.Name, "0"})
	}
	got, err := BuildWrite(view, &rec, 0, map[string]string{"recl": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinParams(got), " srecl=1 ") || strings.Contains(joinParams(got), " recl=") {
		t.Fatal(joinParams(got))
	}

	// RecSet: ch is the selected channel, perf is left out while pgu=0
	recset := FindPage("RecSet")
	r := &Record{Fields: []Param{{"ar", "3"}, {"pgu", "0"}, {"fvk", "1"}, {"video", "2"}, {"resol", "2"}, {"frame", "25"}, {"perf", "1"}}}
	got, _ = BuildWrite(recset, r, 3, nil)
	if want := "ar=3 pgu=0 resol=2 video=2 frame=25 ch=3"; joinParams(got) != want {
		t.Fatalf("got %s, want %s", joinParams(got), want)
	}
}

func TestSecretsKeptAndMasked(t *testing.T) {
	s, c := newFake(t)
	net := FindPage("Network")
	ctx := context.Background()
	before, _ := c.Load(ctx, admin, net, 0)
	// a minimal Network record lacks most fields, so write a reduced page with the same secret handling
	p := &Page{Name: "Network", Title: "Network", SecretField: net.SecretField, Fields: []Field{
		sel("act", "Network Enable", enable),
		sel("ipt", "IP Type", opts("0=Static", "1=DHCP", "2=PPPoE")),
		{Name: "ppp", Label: "PPPoE Password", Kind: Secret, Max: 64, When: is("ipt", "2")},
		{Name: "ddnpw", Label: "DDNS: Password", Kind: Secret, Max: 27},
	}}
	params, err := BuildWrite(p, before, 0, map[string]string{"ipt": "2", "ddnpw": ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := "act=1 ipt=2 ppp=pppsecret ddnpw=ddnssecret"; joinParams(params) != want {
		t.Fatalf("got %s", joinParams(params))
	}
	params, _ = BuildWrite(p, before, 0, map[string]string{"ddnpw": "new"})
	if _, err := c.Write(ctx, admin, "Network", params); err != nil {
		t.Fatal(err)
	}
	after, _ := c.Load(ctx, admin, net, 0)
	for _, ch := range Diff(net, before, after) {
		if strings.Contains(ch.Before+ch.After, "secret") && ch.Before != "(secret)" || strings.Contains(ch.After, "new") {
			t.Fatalf("secret in diff: %+v", ch)
		}
	}
	if len(s.Writes()) != 1 {
		t.Fatal(s.Writes())
	}
	// fields outside the page's form are never shown in a diff
	d := Diff(net, &Record{Fields: []Param{{"spwd", "a"}}}, &Record{Fields: []Param{{"spwd", "b"}}})
	if len(d) != 1 || d[0].After != "(secret)" {
		t.Fatalf("%+v", d)
	}
}

func joinParams(ps []Param) string {
	var b []string
	for _, p := range ps {
		b = append(b, p.Name+"="+p.Value)
	}
	return strings.Join(b, " ")
}

func TestPagesWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Pages {
		if seen[p.Name] {
			t.Errorf("page %s twice", p.Name)
		}
		seen[p.Name] = true
		names := map[string]bool{}
		for _, f := range p.Fields {
			if f.Name == "" || names[f.Name] {
				t.Errorf("%s: field %q empty or twice", p.Name, f.Name)
			}
			names[f.Name] = true
			if f.Kind == Select && len(f.Options) == 0 {
				t.Errorf("%s.%s: select without options", p.Name, f.Name)
			}
		}
	}
}
