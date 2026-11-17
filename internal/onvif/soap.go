// SPDX-License-Identifier: GPL-3.0-or-later

package onvif

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
)

// Namespaces of the services and of the SOAP envelope.
const (
	nsSOAP   = "http://www.w3.org/2003/05/soap-envelope"
	nsSchema = "http://www.onvif.org/ver10/schema"
	nsDevice = "http://www.onvif.org/ver10/device/wsdl"
	nsMedia  = "http://www.onvif.org/ver10/media/wsdl"
	nsError  = "http://www.onvif.org/ver10/error"
)

// request is the part of a SOAP request the services read. encoding/xml matches the untagged
// namespaces by local name, so any prefix works.
type request struct {
	Header struct {
		Security struct {
			UsernameToken *usernameToken `xml:"UsernameToken"`
		} `xml:"Security"`
	} `xml:"Header"`
	Body struct {
		Action struct {
			XMLName            xml.Name
			ProfileToken       string `xml:"ProfileToken"`
			ConfigurationToken string `xml:"ConfigurationToken"`
		} `xml:",any"`
	} `xml:"Body"`
}

type usernameToken struct {
	Username string `xml:"Username"`
	Password struct {
		Type  string `xml:"Type,attr"`
		Value string `xml:",chardata"`
	} `xml:"Password"`
	Nonce   string `xml:"Nonce"`
	Created string `xml:"Created"`
}

func parseRequest(b []byte) (*request, error) {
	var r request
	if err := xml.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Body.Action.XMLName.Local == "" {
		return nil, fmt.Errorf("empty SOAP body")
	}
	return &r, nil
}

type envelope struct{ buf bytes.Buffer }

func newEnvelope() *envelope {
	e := &envelope{}
	e.buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="` + nsSOAP + `" xmlns:tt="` + nsSchema + `" xmlns:tds="` + nsDevice +
		`" xmlns:trt="` + nsMedia + `" xmlns:ter="` + nsError + `"><s:Body>`)
	return e
}

// printf appends formatted XML; string arguments must be wrapped in esc.
func (e *envelope) printf(format string, args ...any) *envelope {
	fmt.Fprintf(&e.buf, format, args...)
	return e
}

func (e *envelope) bytes() []byte {
	e.buf.WriteString(`</s:Body></s:Envelope>`)
	return e.buf.Bytes()
}

// esc escapes text for XML content and attributes.
func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// fault is a SOAP 1.2 fault with an ONVIF subcode.
type fault struct {
	status  int
	code    string // s:Sender or s:Receiver
	subcode string // ter:...
	reason  string
}

var (
	// SOAP 1.2 over HTTP maps every fault to 400 (Sender) or 500 (Receiver).
	faultNotAuthorized = fault{http.StatusBadRequest, "s:Sender", "ter:NotAuthorized", "Sender not authorized"}
	faultNotSupported  = fault{http.StatusInternalServerError, "s:Receiver", "ter:ActionNotSupported", "Optional action not implemented"}
	faultNoProfile     = fault{http.StatusBadRequest, "s:Sender", "ter:InvalidArgVal", "No such profile"}
	faultNoConfig      = fault{http.StatusBadRequest, "s:Sender", "ter:InvalidArgVal", "No such configuration"}
	faultMalformed     = fault{http.StatusBadRequest, "s:Sender", "ter:WellFormed", "Malformed SOAP request"}
)

func (f fault) body() []byte {
	return newEnvelope().printf(`<s:Fault><s:Code><s:Value>%s</s:Value><s:Subcode><s:Value>%s</s:Value></s:Subcode></s:Code>`+
		`<s:Reason><s:Text xml:lang="en">%s</s:Text></s:Reason></s:Fault>`, f.code, f.subcode, esc(f.reason)).bytes()
}
