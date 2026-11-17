// SPDX-License-Identifier: GPL-3.0-or-later

// Package discovery answers WS-Discovery probes for the ONVIF devices, so NVRs can find them.
package discovery

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/ipv4"
)

const (
	nsNetwork = "http://www.onvif.org/ver10/network/wsdl"
	nsDevice  = "http://www.onvif.org/ver10/device/wsdl"
	types     = "dn:NetworkVideoTransmitter tds:Device"
)

// Group is the WS-Discovery IPv4 multicast address.
var Group = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}

// Device is one advertised ONVIF device.
type Device struct {
	EndpointUUID string // urn:uuid:...
	// Scopes returns the current scopes; they may change, e.g. once the DVR's type is known.
	Scopes func() []string
	// IP is advertised in XAddrs; when invalid, the local address that routes to the peer is used.
	IP   netip.Addr
	Port int // ONVIF HTTP port
}

func (d Device) scopes() []string {
	if d.Scopes == nil {
		return nil
	}
	return d.Scopes()
}

func (d Device) xaddr(ip netip.Addr) string {
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(d.Port)) + "/onvif/device_service"
}

// Responder answers probes and announces the devices with Hello and Bye.
type Responder struct {
	Devices []Device
	// Interface names the network interface for the multicast group; empty means the system default.
	Interface string
	Log       *slog.Logger

	instance uint32 // AppSequence InstanceId: changes with every start
	msgNum   atomic.Uint32
}

func (r *Responder) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// Run joins the multicast group, sends Hello, answers probes until ctx is done, then sends Bye.
func (r *Responder) Run(ctx context.Context) error {
	var ifi *net.Interface
	if r.Interface != "" {
		var err error
		if ifi, err = net.InterfaceByName(r.Interface); err != nil {
			return err
		}
	}
	lc := net.ListenConfig{Control: reuseAddrPort}
	pc, err := lc.ListenPacket(ctx, "udp4", ":"+strconv.Itoa(Group.Port))
	if err != nil {
		return fmt.Errorf("ws-discovery listen: %w", err)
	}
	defer pc.Close()
	p := ipv4.NewPacketConn(pc)
	if err := p.JoinGroup(ifi, Group); err != nil {
		return fmt.Errorf("ws-discovery join %s: %w", Group.IP, err)
	}
	if ifi != nil {
		if err := p.SetMulticastInterface(ifi); err != nil {
			return fmt.Errorf("ws-discovery multicast interface: %w", err)
		}
	}
	_ = p.SetMulticastTTL(1)

	r.announce(pc, "Hello")
	stop := context.AfterFunc(ctx, func() {
		r.announce(pc, "Bye")
		_ = pc.Close()
	})
	defer stop()
	err = r.Serve(pc)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// Serve answers probes arriving on pc until it is closed.
func (r *Responder) Serve(pc net.PacketConn) error {
	if r.instance == 0 {
		r.instance = uint32(time.Now().Unix())
	}
	buf := make([]byte, 64<<10)
	for {
		n, src, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		probe, err := parseProbe(buf[:n])
		if err != nil || !probe.matches {
			continue
		}
		udp, ok := src.(*net.UDPAddr)
		if !ok {
			continue
		}
		local := localIPFor(udp)
		for _, d := range r.Devices {
			ip := d.IP
			if !ip.IsValid() {
				ip = local
			}
			if _, err := pc.WriteTo(r.probeMatch(d, ip, probe.messageID), src); err != nil {
				r.log().Debug("ws-discovery reply failed", "to", src.String(), "err", err)
			}
		}
		r.log().Debug("ws-discovery probe answered", "from", src.String(), "devices", len(r.Devices))
	}
}

func (r *Responder) announce(pc net.PacketConn, kind string) {
	if r.instance == 0 {
		r.instance = uint32(time.Now().Unix())
	}
	local := localIPFor(Group)
	for _, d := range r.Devices {
		ip := d.IP
		if !ip.IsValid() {
			ip = local
		}
		if _, err := pc.WriteTo(r.announcement(d, ip, kind), Group); err != nil {
			r.log().Warn("ws-discovery announcement failed", "kind", kind, "err", err)
		}
	}
}

// localIPFor returns the local address the kernel would use toward dst; connecting UDP sends nothing.
func localIPFor(dst *net.UDPAddr) netip.Addr {
	c, err := net.DialUDP("udp4", nil, dst)
	if err != nil {
		return netip.Addr{}
	}
	defer c.Close()
	a, _ := netip.AddrFromSlice(c.LocalAddr().(*net.UDPAddr).IP)
	return a.Unmap()
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

const envelopeStart = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"` +
	` xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"` +
	` xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"` +
	` xmlns:dn="` + nsNetwork + `" xmlns:tds="` + nsDevice + `">`

func (r *Responder) header(action, to, relatesTo string) string {
	h := `<s:Header><a:MessageID>urn:uuid:` + uuid.NewString() + `</a:MessageID>`
	if relatesTo != "" {
		h += `<a:RelatesTo>` + esc(relatesTo) + `</a:RelatesTo>`
	}
	h += `<a:To>` + to + `</a:To><a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/` + action + `</a:Action>`
	h += fmt.Sprintf(`<d:AppSequence InstanceId="%d" MessageNumber="%d"/></s:Header>`, r.instance, r.msgNum.Add(1))
	return h
}

func (d Device) body(ip netip.Addr, withAddrs bool) string {
	b := `<a:EndpointReference><a:Address>` + esc(d.EndpointUUID) + `</a:Address></a:EndpointReference>`
	if withAddrs {
		b += `<d:Types>` + types + `</d:Types><d:Scopes>` + esc(strings.Join(d.scopes(), " ")) + `</d:Scopes>`
		if ip.IsValid() {
			b += `<d:XAddrs>` + esc(d.xaddr(ip)) + `</d:XAddrs>`
		}
	}
	return b + `<d:MetadataVersion>1</d:MetadataVersion>`
}

func (r *Responder) probeMatch(d Device, ip netip.Addr, relatesTo string) []byte {
	return []byte(envelopeStart +
		r.header("ProbeMatches", "http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous", relatesTo) +
		`<s:Body><d:ProbeMatches><d:ProbeMatch>` + d.body(ip, true) + `</d:ProbeMatch></d:ProbeMatches></s:Body></s:Envelope>`)
}

// announcement builds a Hello or a Bye.
func (r *Responder) announcement(d Device, ip netip.Addr, kind string) []byte {
	return []byte(envelopeStart +
		r.header(kind, "urn:schemas-xmlsoap-org:ws:2005:04:discovery", "") +
		`<s:Body><d:` + kind + `>` + d.body(ip, kind == "Hello") + `</d:` + kind + `></s:Body></s:Envelope>`)
}

type probe struct {
	messageID string
	matches   bool
}

// parseProbe reads a Probe and decides whether it asks for ONVIF devices. Types are QNames whose
// prefixes may be declared on any ancestor, so the namespace declarations are tracked while
// tokenizing. Other WS-Discovery users on the network (e.g. wsdd probing for wsdp:Device) do not match.
func parseProbe(b []byte) (probe, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	prefixes := map[string]string{}
	var path []string
	var p probe
	isProbe, sawTypes := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return p, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" {
					prefixes[a.Name.Local] = a.Value
				} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
					prefixes[""] = a.Value
				}
			}
			path = append(path, t.Name.Local)
			if len(path) == 3 && path[1] == "Body" && t.Name.Local == "Probe" {
				isProbe = true
			}
		case xml.EndElement:
			path = path[:len(path)-1]
		case xml.CharData:
			if len(path) == 0 {
				continue
			}
			switch {
			case len(path) == 3 && path[1] == "Header" && path[2] == "MessageID":
				p.messageID = strings.TrimSpace(string(t))
			case len(path) == 4 && path[2] == "Probe" && path[3] == "Types":
				sawTypes = true
				p.matches = p.matches || typesMatch(string(t), prefixes)
			}
		}
	}
	if !isProbe {
		return p, fmt.Errorf("not a probe")
	}
	if !sawTypes {
		p.matches = true
	}
	return p, nil
}

func typesMatch(list string, prefixes map[string]string) bool {
	fields := strings.Fields(list)
	if len(fields) == 0 {
		return true
	}
	for _, qn := range fields {
		prefix, local, ok := strings.Cut(qn, ":")
		if !ok {
			prefix, local = "", qn
		}
		ns, known := prefixes[prefix]
		switch {
		case local == "NetworkVideoTransmitter" && (!known || ns == nsNetwork):
			return true
		case local == "Device" && known && ns == nsDevice:
			return true
		}
	}
	return false
}
