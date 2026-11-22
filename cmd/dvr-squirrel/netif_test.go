// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/config"
)

func TestChannelMAC(t *testing.T) {
	ifs := []iface{
		{name: "lo", addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{name: "eth0", mac: net.HardwareAddr{0x02, 0, 0x5e, 0, 0x53, 0x10}, addrs: []netip.Addr{netip.MustParseAddr("192.0.2.10")}},
		{name: "cam2", mac: net.HardwareAddr{0x02, 0, 0x5e, 0, 0x53, 0x11}, addrs: []netip.Addr{
			netip.MustParseAddr("fe80::1"), netip.MustParseAddr("192.0.2.11")}},
	}
	orig := systemInterfaces
	t.Cleanup(func() { systemInterfaces = orig })
	systemInterfaces = func() ([]iface, error) { return ifs, nil }

	configured := net.HardwareAddr{0x02, 0, 0x5e, 0, 0x53, 0x99}
	tests := map[string]struct {
		ch      config.Channel
		mac     string
		source  string
		wantErr string
	}{
		"configured": {config.Channel{MAC: configured, ListenIP: netip.MustParseAddr("192.0.2.11")}, "02:00:5e:00:53:99", "config", ""},
		"interface":  {config.Channel{ListenIP: netip.MustParseAddr("192.0.2.11")}, "02:00:5e:00:53:11", "interface cam2", ""},
		"no address": {config.Channel{}, "", "derived from the serial", ""},
		"not local":  {config.Channel{ListenIP: netip.MustParseAddr("192.0.2.99")}, "", "derived from the serial", "no interface holds"},
		"no mac":     {config.Channel{ListenIP: netip.MustParseAddr("127.0.0.1")}, "", "derived from the serial", "has no MAC"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mac, source, err := channelMAC(tc.ch)
			got := ""
			if mac != nil {
				got = mac.String()
			}
			if got != tc.mac || source != tc.source {
				t.Fatalf("got %q (%s), want %q (%s)", got, source, tc.mac, tc.source)
			}
			if (err == nil) != (tc.wantErr == "") || err != nil && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err %v, want %q", err, tc.wantErr)
			}
		})
	}

	systemInterfaces = func() ([]iface, error) { return nil, errors.New("netlink denied") }
	if mac, _, err := channelMAC(config.Channel{ListenIP: netip.MustParseAddr("192.0.2.10")}); mac != nil || err == nil {
		t.Fatalf("listing failure: %v %v", mac, err)
	}
}
