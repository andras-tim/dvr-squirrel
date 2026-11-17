// SPDX-License-Identifier: GPL-3.0-or-later

package devtype

import "testing"

func TestNames(t *testing.T) {
	tests := []struct {
		code      string
		n         int
		model     string
		describes string
	}{
		{"504", 4, "DVR 500 series, 4 channels", "DVR 500 series, 4 channels (type 504)"},
		{"516", 16, "DVR 500 series, 16 channels", "DVR 500 series, 16 channels (type 516)"},
		{"999", 4, "DVR, 4 channels (type 999)", "DVR, 4 channels (type 999, not a known one)"},
		{"", 8, "DVR, 8 channels", "DVR, 8 channels (type unknown)"},
	}
	for _, tt := range tests {
		if got := Model(tt.code, tt.n); got != tt.model {
			t.Errorf("Model(%q, %d) = %q", tt.code, tt.n, got)
		}
		if got := Describe(tt.code, tt.n); got != tt.describes {
			t.Errorf("Describe(%q, %d) = %q", tt.code, tt.n, got)
		}
	}
	if n, ok := Known("508"); !ok || n != 8 {
		t.Fatal("508")
	}
}
