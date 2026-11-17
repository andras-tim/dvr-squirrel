// SPDX-License-Identifier: GPL-3.0-or-later

package webui

import (
	"fmt"
	"html/template"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
	"github.com/andras-tim/dvr-squirrel/internal/placeholder"
)

var funcs = template.FuncMap{
	"clock": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Local().Format("15:04")
	},
	"clockSec": func(t time.Time) string { return t.Local().Format("15:04:05") },
	// elapsed is the time since t, "12 min" or "1 h 5 min".
	"elapsed": func(now, t time.Time) string { return placeholder.Elapsed(now.Sub(t)) },
	// ago is a short age, "0.4 s ago", "12 s ago", "5 min ago".
	"ago": func(now, t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		d := now.Sub(t)
		switch {
		case d < 10*time.Second:
			return fmt.Sprintf("%.1f s ago", max(d.Seconds(), 0))
		case d < time.Minute:
			return fmt.Sprintf("%d s ago", int(d.Seconds()))
		default:
			return placeholder.Elapsed(d) + " ago"
		}
	},
	"num":      func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"int":      func(f float64) string { return fmt.Sprintf("%.0f", f) },
	"isSelect": func(f *dvrweb.Field) bool { return f.Kind == dvrweb.Select },
	"isText":   func(f *dvrweb.Field) bool { return f.Kind == dvrweb.Text },
	"isSecret": func(f *dvrweb.Field) bool { return f.Kind == dvrweb.Secret },
	"optText": func(f *dvrweb.Field, v string) string {
		for _, o := range f.Options {
			if o.Value == v {
				return o.Text
			}
		}
		return v
	},
}

// The design: warm light theme by default, the night palette under prefers-color-scheme: dark; no JavaScript,
// no external resources (the CSP allows neither). Muted text and buttons meet WCAG AA in both themes.
const templates = `
{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.}} - DVR Squirrel</title><link rel="icon" href="/favicon.ico">
<style>
:root {
  color-scheme: light dark;
  --ground: #f4f1ec; --surface: #fbfaf7; --border: #e4ded5; --input: #cbc2b6; --text: #2a2522; --muted: #5e554d;
  --accent: #a14a22; --accent-hover: #7d3818; --button: #a14a22; --button-text: #ffffff; --blue: #3f6a96;
  --current: #f1e7de; --shadow: 0 1px 2px rgba(60,40,20,.05);
  --ok: #2f7d48; --ok-ring: rgba(47,125,72,.15); --bad: #b3261e; --bad-ring: rgba(179,38,30,.12);
  --live-bg: #e6f1e6; --live-fg: #2a6b3c; --off-bg: #f6e3d9; --off-fg: #8c3a17;
  --notice-bg: #f6ece4; --notice-border: #ead6c6; --notice-text: #5e4a3c;
  --success-bg: #eef7f1; --success-border: #c4e2cf; --success-text: #1e6b35;
  --error-bg: #fbeceb; --error-border: #efc6c2; --error-text: #a3261b;
  --line-art: #e4ddd2;
  --f1: #a14a22; --f2: #d98552; --f3: #c96a3a; --f4: #4f7aa6; --f5: #3f6a96; --f6: #6b93bd; --f7: #b4552a; --f8: #d98552;
}
@media (prefers-color-scheme: dark) {
  :root {
    --ground: #11151c; --surface: #161b24; --border: #262d39; --input: #3a4352; --text: #e6e9ee; --muted: #a4adbb;
    --accent: #e8936a; --accent-hover: #f2b393; --button: #bb5a32; --button-text: #ffffff; --blue: #8fb4e0;
    --current: #212835; --shadow: none;
    --ok: #4cc47a; --ok-ring: rgba(76,196,122,.18); --bad: #ff8a80; --bad-ring: rgba(255,138,128,.16);
    --live-bg: rgba(76,196,122,.16); --live-fg: #7fdba2; --off-bg: rgba(232,147,106,.16); --off-fg: #f2a07a;
    --notice-bg: #1d1a1a; --notice-border: #3a2c25; --notice-text: #d8c3b6;
    --success-bg: #15231b; --success-border: #24402e; --success-text: #8fd9a8;
    --error-bg: #2a1716; --error-border: #4a2723; --error-text: #ff9b8a;
    --line-art: #232a35;
    --f1: #b4552a; --f2: #e8936a; --f3: #c96a3a; --f4: #5f8ab8; --f5: #4f7aa6; --f6: #8fb4e0; --f7: #c96a3a; --f8: #e8936a;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--ground); color: var(--text);
  font: 15px/1.45 system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif; }
a { color: var(--accent); } a:hover { color: var(--accent-hover); }
.muted { color: var(--muted); } .small { font-size: 13px; }
.word { font-weight: 650; letter-spacing: -.01em; } .word .w1 { color: var(--blue); } .word .w2 { color: var(--accent); }
header.top { display: flex; align-items: center; gap: 14px; padding: 14px 32px; background: var(--surface); flex-wrap: wrap; }
header.top .word { font-size: 19px; white-space: nowrap; }
header.top .sep { width: 1px; height: 24px; background: var(--border); margin: 0 6px; }
header.top nav { display: flex; gap: 20px; font-weight: 500; }
header.top nav a { color: var(--muted); text-decoration: none; padding-bottom: 4px; border-bottom: 2px solid transparent; }
header.top nav a[aria-current] { color: var(--text); border-bottom-color: var(--accent); }
header.top .grow { flex: 1; } header.top form { margin: 0; }
.facets { display: block; width: 100%; height: 6px; }
.facets .f1 { fill: var(--f1); } .facets .f2 { fill: var(--f2); } .facets .f3 { fill: var(--f3); } .facets .f4 { fill: var(--f4); }
.facets .f5 { fill: var(--f5); } .facets .f6 { fill: var(--f6); } .facets .f7 { fill: var(--f7); } .facets .f8 { fill: var(--f8); }
.button { display: inline-flex; align-items: center; justify-content: center; height: 40px; padding: 0 18px; border-radius: 8px;
  border: 0; background: var(--button); color: var(--button-text); text-decoration: none; font: inherit; font-weight: 600; font-size: 14px; cursor: pointer; }
.button:hover { color: var(--button-text); filter: brightness(1.08); }
.button.outline { background: transparent; color: var(--text); border: 1px solid var(--input); font-weight: 500; }
.wrap-main { max-width: 1180px; margin: 0 auto; padding: 28px 32px; display: flex; flex-direction: column; gap: 22px; }
.card { background: var(--surface); border: 1px solid var(--border); border-radius: 14px; box-shadow: var(--shadow); }
.dvr-card { padding: 20px 24px; display: flex; gap: 32px; flex-wrap: wrap; align-items: center; }
.dvr-card .state { display: flex; align-items: center; gap: 12px; min-width: 240px; }
.dot { width: 12px; height: 12px; border-radius: 50%; flex: none; }
.dot.on { background: var(--ok); box-shadow: 0 0 0 4px var(--ok-ring); } .dot.off { background: var(--bad); box-shadow: 0 0 0 4px var(--bad-ring); }
.dvr-card .big { font-size: 20px; font-weight: 600; }
.facts { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 16px 24px; flex: 1; min-width: 280px; font-size: 14px; }
.facts .k { font-size: 12px; color: var(--muted); } .facts .v { font-weight: 500; overflow-wrap: anywhere; }
.heading { display: flex; align-items: center; gap: 12px; } .heading h2 { margin: 0; font-size: 17px; font-weight: 600; }
.heading .rule { flex: 1; border-top: 1px solid var(--border); }
.channels { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 16px; }
.channel { padding: 16px 18px; display: flex; flex-direction: column; gap: 10px; }
.channel .title { display: flex; align-items: center; gap: 8px; } .channel .title b { flex: 1; overflow-wrap: anywhere; }
.badge { font-size: 12px; font-weight: 600; padding: 3px 10px; border-radius: 999px; white-space: nowrap; }
.badge.live { background: var(--live-bg); color: var(--live-fg); } .badge.offline { background: var(--off-bg); color: var(--off-fg); }
.kv { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 14px; font-size: 13px; }
.kv > span:nth-child(odd) { color: var(--muted); }
.notice { display: flex; align-items: center; gap: 12px; background: var(--notice-bg); border: 1px solid var(--notice-border);
  border-radius: 12px; padding: 14px 20px; font-size: 14px; color: var(--notice-text); }
.notice span { flex: 1; } .notice a { font-weight: 600; }
.box { border-radius: 12px; padding: 14px 20px; display: flex; flex-direction: column; gap: 8px; }
.box.success { background: var(--success-bg); border: 1px solid var(--success-border); color: var(--success-text); }
.box.error { background: var(--error-bg); border: 1px solid var(--error-border); color: var(--error-text); }
.box .line { display: flex; align-items: center; gap: 10px; font-weight: 600; }
.box table { color: var(--text); }
.scroll { overflow-x: auto; }
table.plain { border-collapse: collapse; font-size: 14px; }
table.plain th, table.plain td { text-align: left; padding: 4px 16px 4px 0; vertical-align: top; }
table.plain th { font-weight: 600; } table.events td { border-top: 1px solid var(--border); padding-top: 6px; padding-bottom: 6px; }
.events-card { padding: 16px 20px; }
/* login */
.center { min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 40px 16px; }
.login { width: 100%; max-width: 420px; padding: 32px 32px 28px; display: flex; flex-direction: column; gap: 18px; }
.login .brand { display: flex; flex-direction: column; align-items: center; gap: 8px; text-align: center; }
.login h1 { margin: 0; font-size: 24px; }
.login p { margin: 0; font-size: 14px; color: var(--muted); }
.login form { display: flex; flex-direction: column; gap: 14px; } .login .button { height: 46px; width: 100%; }
.hint { display: flex; gap: 10px; align-items: flex-start; background: var(--ground); border-radius: 8px; padding: 12px 14px; font-size: 13px; color: var(--muted); }
.hint svg { flex: none; margin-top: 1px; }
label.field { display: flex; flex-direction: column; gap: 6px; font-size: 14px; font-weight: 500; }
input, select { height: 44px; border: 1px solid var(--input); border-radius: 8px; padding: 0 12px; font: inherit; font-weight: 400;
  background: var(--surface); color: var(--text); width: 100%; }
select { padding: 0 10px; }
input:focus-visible, select:focus-visible, .button:focus-visible, a:focus-visible { outline: 2px solid var(--blue); outline-offset: 2px; }
/* settings */
.settings { max-width: 1220px; margin: 0 auto; padding: 24px 32px 48px; display: flex; flex-wrap: wrap; gap: 24px; align-items: flex-start; }
.side { flex: 1 1 220px; max-width: 260px; padding: 10px; display: flex; flex-direction: column; gap: 2px; font-size: 14px; border-radius: 12px; }
.side .group { display: flex; align-items: center; gap: 8px; font-size: 12px; text-transform: uppercase; letter-spacing: .04em; color: var(--muted); padding: 10px 10px 4px; }
.side .group svg { stroke: var(--muted); }
.side a { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 8px 10px; border-radius: 8px; text-decoration: none; color: var(--text); }
.side a[aria-current] { background: var(--current); font-weight: 600; }
.side a .tag { font-size: 12px; color: var(--muted); font-weight: 400; white-space: nowrap; }
.content { flex: 999 1 560px; min-width: 0; display: flex; flex-direction: column; gap: 20px; }
.form-card { position: relative; overflow: hidden; padding: 22px 24px; display: flex; flex-direction: column; gap: 18px; border-radius: 12px; }
.form-card .squirrel { position: absolute; right: 24px; bottom: 70px; pointer-events: none; stroke: var(--line-art); }
.form-card > *:not(.squirrel) { position: relative; }
.form-card .title { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
.form-card h1 { margin: 0; font-size: 22px; font-weight: 650; flex: 1; }
.pills { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; font-size: 14px; }
.pills a, .pills b { padding: 4px 12px; border-radius: 999px; border: 1px solid var(--border); text-decoration: none; }
.pills b { background: var(--current); border-color: var(--current); }
.grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px 24px; }
.grid .value { min-height: 44px; display: flex; align-items: center; color: var(--muted); font-weight: 400; }
.grid .note { font-size: 12px; color: var(--muted); font-weight: 400; }
.actions { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; border-top: 1px solid var(--border); padding-top: 16px; }
.actions .grow { flex: 1; }
.dashed { background: var(--surface); border: 1px dashed var(--input); border-radius: 12px; padding: 14px 20px; font-size: 13px; color: var(--muted); }
@media (max-width: 640px) {
  header.top, .wrap-main, .settings { padding-left: 16px; padding-right: 16px; }
  .grid { grid-template-columns: minmax(0, 1fr); } .side { max-width: none; }
}
</style></head><body>{{end}}

{{define "facets"}}<svg class="facets" aria-hidden="true" viewBox="0 0 1280 6" preserveAspectRatio="none"><polygon class="f1" points="0,0 160,0 120,6 0,6"/><polygon class="f2" points="160,0 320,0 280,6 120,6"/><polygon class="f3" points="320,0 480,0 440,6 280,6"/><polygon class="f4" points="480,0 640,0 600,6 440,6"/><polygon class="f5" points="640,0 800,0 760,6 600,6"/><polygon class="f6" points="800,0 960,0 920,6 760,6"/><polygon class="f7" points="960,0 1120,0 1080,6 920,6"/><polygon class="f8" points="1120,0 1280,0 1280,6 1080,6"/></svg>{{end}}

{{define "nav"}}<header class="top">
<img src="/logo.png" alt="" width="36" height="36">
<div class="word"><span class="w1">DVR</span> <span class="w2">Squirrel</span></div>
<span class="sep" aria-hidden="true"></span>
<nav aria-label="Main"><a href="/"{{if eq .Active "status"}} aria-current="page"{{end}}>Status</a><a href="/settings"{{if eq .Active "settings"}} aria-current="page"{{end}}>DVR settings</a></nav>
<span class="grow"></span>
{{if .LoggedIn}}<span class="muted small">{{.User}}</span><form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="button outline">Log out</button></form>{{else}}<a class="button" href="/login">Log in</a>{{end}}
</header>{{template "facets"}}{{end}}

{{define "login"}}{{template "head" "Log in"}}<div class="center"><main class="card login">
<div class="brand"><img src="/logo.png" alt="DVR Squirrel" width="96" height="96">
<h1 class="word"><span class="w1">DVR</span> <span class="w2">Squirrel</span></h1>
<p>Log in with an account of the DVR. The bridge checks it with the DVR and keeps it in memory for this session only.</p></div>
{{if .Error}}<div class="box error" role="alert"><div class="line">{{.Error}}</div></div>{{end}}
<form method="post" action="/login">
<label class="field">DVR user<input name="user" autocomplete="username" required></label>
<label class="field">Password<input name="password" type="password" autocomplete="current-password"></label>
<button class="button">Log in</button>
</form>
<div class="hint"><svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16h.01"/></svg>
<span>What you can open follows your DVR rights: a view-only account sees the status only, an admin account every settings page. After 5 failed attempts in a minute the login pauses for 60 s.</span></div>
<a href="/" class="small" style="text-align: center">Back to status</a>
</main></div></body></html>{{end}}

{{define "status"}}{{template "head" "Status"}}{{template "nav" .}}<main class="wrap-main">{{$now := .Now}}
{{with .Status}}
<section class="card dvr-card" aria-label="DVR">
<div class="state"><span class="dot {{if .DVR.Online}}on{{else}}off{{end}}" aria-hidden="true"></span>
<div><div class="big">DVR {{if .DVR.Online}}online{{else}}offline{{end}}</div>
<div class="small muted">since {{clock .DVR.Since}} &middot; {{elapsed $now .DVR.Since}}</div></div></div>
<div class="facts">
<div><div class="k">Device</div><div class="v">{{if .DVR.Type}}{{.DVR.Type}}{{else}}-{{end}}</div></div>
<div><div class="k">Channels</div><div class="v">{{.Live}} live, {{.OfflineCount}} offline</div></div>
<div><div class="k">Stream</div><div class="v">&asymp; {{int .TotalKbps}} kbit/s total</div></div>
{{if .DVR.Addr}}<div><div class="k">Address</div><div class="v">{{.DVR.Addr}}</div></div>{{end}}
{{if .DVR.LastError}}<div><div class="k">Last error</div><div class="v">{{.DVR.LastError}}</div></div>{{end}}
</div></section>
<div class="heading"><h2>Channels</h2><span class="rule" aria-hidden="true"></span></div>
<div class="channels">
{{range .Channels}}<article class="card channel">
<div class="title"><b>{{.Name}}</b>{{if .Offline}}<span class="badge offline">Offline since {{clock .OfflineSince}}</span>{{else}}<span class="badge live">Live</span>{{end}}</div>
<div class="kv"><span>Resolution</span><span>{{if .Width}}{{.Width}}x{{.Height}}{{else}}-{{end}}</span>
<span>Frame rate</span><span>{{num .FPS}} fps</span>
<span>Bitrate</span><span>{{int .Kbps}} kbit/s</span>
<span>Last keyframe</span><span>{{ago $now .LastIDR}}</span>
<span>Clients</span><span class="clients">{{.ClientCount}}</span></div>
{{if .Clients}}<div class="small muted">{{range .Clients}}{{.}}<br>{{end}}</div>{{end}}
</article>{{else}}<p class="muted">No channel yet: they start once the DVR has reported its channel count.</p>{{end}}
</div>
{{end}}
{{if .LoggedIn}}{{with .Status}}<div class="heading"><h2>Recent events</h2><span class="rule" aria-hidden="true"></span></div>
<section class="card events-card scroll"><table class="plain events">
{{range .Events}}<tr><td class="muted">{{clockSec .Time}}</td><td>{{.Level}}</td><td>{{.Message}}</td><td class="muted">{{.Attrs}}</td></tr>{{else}}<tr><td>none</td></tr>{{end}}
</table></section>{{end}}
{{else}}<section class="notice"><span>Recent events and the DVR settings are shown after logging in with a DVR account.</span><a href="/login">Log in</a></section>{{end}}
</main></body></html>{{end}}

{{define "sidebar"}}<nav class="card side" aria-label="Settings pages">
{{range .Nav}}<div class="group"><svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="{{.Icon}}"/></svg><span>{{.Title}}</span></div>
{{range .Pages}}<a href="/settings/{{.Name}}"{{if .Current}} aria-current="page"{{end}}><span>{{.Title}}</span>{{if .Tag}}<span class="tag">{{.Tag}}</span>{{end}}</a>{{end}}
{{end}}</nav>{{end}}

{{define "notoffered"}}<section class="dashed" aria-label="Not offered">Not offered on purpose: adding or deleting DVR users, NTP sync now, auto settings, firmware actions. The user list is read only, without passwords. The bridge never changes DVR settings by itself.</section>{{end}}

{{define "squirrel"}}<svg class="squirrel" aria-hidden="true" width="300" height="330" viewBox="0 0 200 220" fill="none" stroke-width="1" stroke-linejoin="round"><polygon points="69,214 43,177 95,185"/><polygon points="69,214 95,185 87,214"/><polygon points="43,177 38,131 90,154"/><polygon points="43,177 90,154 95,185"/><polygon points="38,131 51,94 85,120"/><polygon points="38,131 85,120 90,154"/><polygon points="51,94 74,66 90,94"/><polygon points="51,94 90,94 85,120"/><polygon points="74,66 105,50 103,89"/><polygon points="74,66 103,89 90,94"/><polygon points="105,50 134,55 121,99"/><polygon points="105,50 121,99 103,89"/><polygon points="134,55 150,76 139,97"/><polygon points="134,55 139,97 121,99"/><polygon points="140,38 150,10 160,40"/><polygon points="140,38 160,40 186,72"/><polygon points="140,38 186,72 160,88"/><polygon points="120,70 140,38 160,88"/><polygon points="100,100 120,70 160,88"/><polygon points="100,100 160,88 150,120"/><polygon points="100,100 150,120 88,150"/><polygon points="150,120 166,140 150,170"/><polygon points="88,150 150,120 150,170"/><polygon points="88,150 150,170 150,200"/><polygon points="88,150 150,200 90,214"/><polygon points="90,214 150,200 170,214"/><circle cx="163" cy="56" r="7"/><circle cx="163" cy="56" r="3.5"/></svg>{{end}}

{{define "settings"}}{{template "head" "DVR settings"}}{{template "nav" .}}<div class="settings">{{template "sidebar" .}}
<main class="content"><section class="card form-card">{{template "squirrel"}}
<div class="title"><h1>DVR settings</h1></div>
<p class="muted" style="margin: 0">Choose a page on the left. Pages are read and written with your DVR account; pages your account may not change are read only.</p>
</section>{{template "notoffered"}}</main></div></body></html>{{end}}

{{define "page"}}{{with .View}}{{template "head" .Page.Title}}{{end}}{{template "nav" .}}<div class="settings">{{template "sidebar" .}}
<main class="content">{{$csrf := .CSRF}}{{$now := .Now}}{{with .View}}
{{if .Written}}<section class="box success" role="status" aria-label="Saved">
<div class="line"><svg aria-hidden="true" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M8 12.5 11 15.5 16.5 9.5"/></svg>
<span>Saved to the DVR and read back: {{len .Changes}} field{{if ne (len .Changes) 1}}s{{end}} changed.</span></div>
<div class="scroll"><table class="plain"><thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead><tbody>
{{range .Changes}}<tr><td>{{.Label}} <span class="muted small">{{.Name}}</span></td><td>{{.Before}}</td><td>{{.After}}</td></tr>{{else}}<tr><td colspan="3">no field changed</td></tr>{{end}}
</tbody></table></div></section>{{end}}
{{if .Error}}<section class="box error" role="alert"><div class="line">{{.Error}}</div></section>{{end}}
<section class="card form-card" aria-label="{{.Page.Title}}">{{template "squirrel"}}
<div class="title"><h1>{{.Page.Title}}</h1><span class="small muted">read {{clock $now}} &middot; {{.Page.Name}}.xml</span></div>
{{if .Page.Note}}<p class="muted small" style="margin: 0">{{.Page.Note}}</p>{{end}}
{{if .Channels}}<div class="pills">Channel {{$ch := .Ch}}{{$name := .Page.Name}}{{range .Channels}}{{if eq . $ch}}<b>{{.}}</b>{{else}}<a href="/settings/{{$name}}?ch={{.}}">{{.}}</a>{{end}}{{end}}</div>{{end}}
{{if .Fields}}<form method="post" action="/settings/{{.Page.Name}}{{if .Channels}}?ch={{.Ch}}{{end}}"><input type="hidden" name="csrf" value="{{$csrf}}">
<div class="grid">{{$w := .Writable}}
{{range .Fields}}<label class="field">{{.F.Label}}
{{if not .Present}}<span class="value">not in the DVR's reply</span>
{{else if and $w (isSelect .F)}}<select name="{{.F.Name}}">{{$v := .Value}}{{range .F.Options}}<option value="{{.Value}}"{{if eq .Value $v}} selected{{end}}>{{.Text}}</option>{{end}}</select>
{{else if and $w (isText .F)}}<input name="{{.F.Name}}" value="{{.Value}}"{{if .F.Max}} maxlength="{{.F.Max}}"{{end}}>
{{else if and $w (isSecret .F)}}<input name="{{.F.Name}}" type="password" autocomplete="off" placeholder="unchanged">
{{else if isSecret .F}}<span class="value">(secret, not shown)</span>
{{else}}<span class="value">{{optText .F .Value}}</span>{{end}}
{{if .F.Note}}<span class="note">{{.F.Note}}</span>{{end}}</label>{{end}}
</div>
{{if $w}}<div class="actions"><button class="button">Save to DVR</button><a class="small" href="/settings/{{.Page.Name}}{{if .Channels}}?ch={{.Ch}}{{end}}">Discard changes</a><span class="grow"></span>
<span class="small muted">The whole page is written back as the DVR's form sends it; password fields are never shown.</span></div>{{end}}
</form>{{end}}
{{if .Extra}}<div class="scroll"><table class="plain">{{range .Extra}}<tr><th>{{.Name}}</th><td>{{.Value}}</td></tr>{{end}}</table></div>{{end}}
</section>{{end}}{{template "notoffered"}}</main></div></body></html>{{end}}
`
