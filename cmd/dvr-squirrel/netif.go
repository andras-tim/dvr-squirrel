// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/andras-tim/dvr-squirrel/internal/config"
)

// iface is the part of a network interface the MAC lookup needs.
type iface struct {
	name  string
	mac   net.HardwareAddr
	addrs []netip.Addr
}

// systemInterfaces lists the host's interfaces; a variable for the tests.
var systemInterfaces = func() ([]iface, error) {
	nis, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []iface
	for _, ni := range nis {
		addrs, err := ni.Addrs()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ni.Name, err)
		}
		i := iface{name: ni.Name, mac: ni.HardwareAddr}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					i.addrs = append(i.addrs, ip.Unmap())
				}
			}
		}
		out = append(out, i)
	}
	return out, nil
}

// channelMAC returns the MAC a channel reports to NVRs and where it comes from: the configured mac, else the MAC of
// the interface holding the listen_ip, so NVRs see the same MAC in ARP and in GetNetworkInterfaces. Nil means the
// MAC derived from the serial (no listen_ip, or the lookup failed: err says why).
func channelMAC(cc config.Channel) (net.HardwareAddr, string, error) {
	if cc.MAC != nil {
		return cc.MAC, "config", nil
	}
	if !cc.ListenIP.IsValid() {
		return nil, "derived from the serial", nil
	}
	ifs, err := systemInterfaces()
	if err != nil {
		return nil, "derived from the serial", fmt.Errorf("list interfaces: %w", err)
	}
	ip := cc.ListenIP.Unmap()
	for _, i := range ifs {
		for _, a := range i.addrs {
			if a != ip {
				continue
			}
			if len(i.mac) != 6 {
				return nil, "derived from the serial", fmt.Errorf("interface %s of %s has no MAC address", i.name, ip)
			}
			return i.mac, "interface " + i.name, nil
		}
	}
	return nil, "derived from the serial", fmt.Errorf("no interface holds %s", ip)
}
