// SPDX-License-Identifier: GPL-3.0-or-later

package discovery

import (
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func probeXML(typesXML string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"` +
		` xmlns:wsd="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:wsdp="http://schemas.xmlsoap.org/ws/2006/02/devprof"` +
		` xmlns:dn="http://www.onvif.org/ver10/network/wsdl" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
		`<soap:Header><wsa:MessageID>urn:uuid:11111111-2222-3333-4444-555555555555</wsa:MessageID>` +
		`<wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</wsa:Action></soap:Header>` +
		`<soap:Body><wsd:Probe>` + typesXML + `</wsd:Probe></soap:Body></soap:Envelope>`
}

func TestParseProbe(t *testing.T) {
	tests := map[string]bool{
		`<wsd:Types>dn:NetworkVideoTransmitter</wsd:Types>`:                                                  true,
		`<wsd:Types>tds:Device</wsd:Types>`:                                                                  true,
		`<wsd:Types xmlns:x="http://www.onvif.org/ver10/network/wsdl">x:NetworkVideoTransmitter</wsd:Types>`: true,
		`<wsd:Types>NetworkVideoTransmitter</wsd:Types>`:                                                     true,
		`<wsd:Types></wsd:Types>`:                                                                            true,
		``:                                                                                                   true,
		`<wsd:Types>wsdp:Device</wsd:Types>`:                                                                 false,
		`<wsd:Types>pub:Computer</wsd:Types>`:                                                                false,
	}
	for types, want := range tests {
		p, err := parseProbe([]byte(probeXML(types)))
		if err != nil {
			t.Fatalf("%s: %v", types, err)
		}
		if p.matches != want {
			t.Errorf("%q: matches = %t, want %t", types, p.matches, want)
		}
		if p.messageID != "urn:uuid:11111111-2222-3333-4444-555555555555" {
			t.Errorf("message id %q", p.messageID)
		}
	}
	if _, err := parseProbe([]byte(strings.Replace(probeXML(""), "Probe>", "Hello>", 2))); err == nil {
		t.Error("a Hello parsed as a probe")
	}
}

func TestServeAnswersProbe(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &Responder{Devices: []Device{
		{EndpointUUID: "urn:uuid:aaaaaaaa-0000-0000-0000-000000000001", Scopes: func() []string { return []string{"onvif://www.onvif.org/name/Yard"} }, Port: 8081},
		{EndpointUUID: "urn:uuid:aaaaaaaa-0000-0000-0000-000000000002", IP: netip.MustParseAddr("192.0.2.12"), Port: 80},
	}}
	done := make(chan error, 1)
	go func() { done <- r.Serve(server) }()
	defer func() {
		server.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	read := func() string {
		buf := make([]byte, 8192)
		_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, _, err := client.ReadFrom(buf)
		if err != nil {
			return ""
		}
		return string(buf[:n])
	}

	if _, err := client.WriteTo([]byte(probeXML(`<wsd:Types>wsdp:Device</wsd:Types>`)), server.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "" {
		t.Fatalf("answered a wsdp:Device probe: %s", got)
	}

	if _, err := client.WriteTo([]byte(probeXML(`<wsd:Types>dn:NetworkVideoTransmitter</wsd:Types>`)), server.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	replies := []string{read(), read()}
	wantAddrs := []string{"http://127.0.0.1:8081/onvif/device_service", "http://192.0.2.12:80/onvif/device_service"}
	for i, rep := range replies {
		for _, want := range []string{
			"<a:RelatesTo>urn:uuid:11111111-2222-3333-4444-555555555555</a:RelatesTo>",
			"<d:ProbeMatch>", r.Devices[i].EndpointUUID, "<d:Types>dn:NetworkVideoTransmitter tds:Device</d:Types>",
			"<d:XAddrs>" + wantAddrs[i] + "</d:XAddrs>",
		} {
			if !strings.Contains(rep, want) {
				t.Errorf("reply %d lacks %q:\n%s", i, want, rep)
			}
		}
	}
	if extra := read(); extra != "" {
		t.Fatalf("unexpected third reply: %s", extra)
	}
}

func TestAnnouncements(t *testing.T) {
	r := &Responder{instance: 7}
	d := Device{EndpointUUID: "urn:uuid:x", Port: 80}
	hello := string(r.announcement(d, netip.MustParseAddr("192.0.2.1"), "Hello"))
	bye := string(r.announcement(d, netip.MustParseAddr("192.0.2.1"), "Bye"))
	if !strings.Contains(hello, "<d:Hello>") || !strings.Contains(hello, "<d:XAddrs>http://192.0.2.1:80/onvif/device_service</d:XAddrs>") {
		t.Fatalf("hello: %s", hello)
	}
	if !strings.Contains(bye, "<d:Bye>") || strings.Contains(bye, "XAddrs") || !strings.Contains(bye, `MessageNumber="2"`) {
		t.Fatalf("bye: %s", bye)
	}
}
