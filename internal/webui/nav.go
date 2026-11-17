// SPDX-License-Identifier: GPL-3.0-or-later

package webui

import (
	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
)

// navGroup is one group of the settings sidebar.
type navGroup struct {
	Title string
	Icon  string // SVG path data, 24x24, stroked
	Pages []navPage
}

type navPage struct {
	Name, Title, Tag string
	Current          bool
}

// sidebarOrder lists the groups in sidebar order with their icons.
var sidebarOrder = []struct{ title, icon string }{
	{"Channels", "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z"},
	{"Recording", "M3 7h12v10H3zM15 10l6-3v10l-6-3"},
	{"Detection", "M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9M10 21h4"},
	{"Network", "M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18"},
	{"System", "M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6zM19 12l2-1-2-4-2 1-2-2V4h-4v2L9 8 7 7 5 11l2 1v2l-2 1 2 4 2-1 2 2v2h4v-2l2-2 2 1 2-4-2-1z"},
}

// sidebar groups the settings pages; a page is tagged per channel, or read only when the DVR's form has no
// writer or the account lacks the right.
func sidebar(perms [dvrweb.MaxPerm + 1]bool, current string) []navGroup {
	var out []navGroup
	for _, g := range sidebarOrder {
		ng := navGroup{Title: g.title, Icon: g.icon}
		for i := range dvrweb.Pages {
			p := &dvrweb.Pages[i]
			if p.Section != g.title {
				continue
			}
			tag := ""
			switch {
			case p.ReadOnly || !perms[p.Perm]:
				tag = "read only"
			case p.PerChannel:
				tag = "per channel"
			}
			ng.Pages = append(ng.Pages, navPage{Name: p.Name, Title: p.Title, Tag: tag, Current: p.Name == current})
		}
		if len(ng.Pages) > 0 {
			out = append(out, ng)
		}
	}
	return out
}

// Live is the number of channels with live video.
func (s Status) Live() int {
	n := 0
	for _, c := range s.Channels {
		if !c.Offline {
			n++
		}
	}
	return n
}

// OfflineCount is the number of channels showing filler.
func (s Status) OfflineCount() int { return len(s.Channels) - s.Live() }

// TotalKbps is the summed live video bitrate.
func (s Status) TotalKbps() float64 {
	t := 0.0
	for _, c := range s.Channels {
		t += c.Kbps
	}
	return t
}
