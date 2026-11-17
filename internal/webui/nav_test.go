// SPDX-License-Identifier: GPL-3.0-or-later

package webui

import (
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
)

func TestSidebarCoversEveryPage(t *testing.T) {
	var all [dvrweb.MaxPerm + 1]bool
	for i := range all {
		all[i] = true
	}
	n := 0
	for _, g := range sidebar(all, "Network") {
		for _, p := range g.Pages {
			n++
			if p.Current != (p.Name == "Network") {
				t.Fatalf("current %+v", p)
			}
		}
	}
	if n != len(dvrweb.Pages) {
		t.Fatalf("sidebar shows %d of %d pages", n, len(dvrweb.Pages))
	}
}
