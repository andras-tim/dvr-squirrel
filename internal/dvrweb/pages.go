// SPDX-License-Identifier: GPL-3.0-or-later

package dvrweb

import (
	"fmt"
	"regexp"
	"strconv"
)

// Kind is how a field is shown and written.
type Kind int

// Field kinds.
const (
	Select  Kind = iota // one of Options
	Text                // free text, at most Max characters
	Secret              // a password: never shown, an empty input keeps the current value
	Hidden              // sent with its read value, not editable
	Display             // shown, never sent (the DVR's form disables it)
	Const               // sent with Value, not shown
)

// Option is one choice of a select.
type Option struct{ Value, Text string }

// Field is one field of a settings page, in the order the DVR's own form submits it.
type Field struct {
	Name    string // in the read reply
	Param   string // in the write request when it differs from Name
	Label   string
	Kind    Kind
	Options []Option
	Max     int
	Pattern *regexp.Regexp // for Text values
	Value   string         // for Const
	Note    string
	// When, if set, sends the field only when it returns true for the record being written (the DVR's form
	// disables the field otherwise).
	When func(get func(string) string) bool
}

// ParamName is the name used in the write request.
func (f *Field) ParamName() string {
	if f.Param != "" {
		return f.Param
	}
	return f.Name
}

// Page is a settings page of the DVR.
type Page struct {
	Name       string // the reader's name; the writer is Name + "A"
	Title      string
	Section    string
	PerChannel bool
	// Perm is the perm.xml right the DVR's UI requires (7 for the settings window, 0 for accounts).
	Perm int
	// ReadOnly pages are only shown: they have no writer, or writing would resend secrets.
	ReadOnly bool
	// SecretField marks fields of a read-only page that are never shown.
	SecretField *regexp.Regexp
	Note        string
	Fields      []Field
}

func opts(pairs ...string) []Option {
	out := make([]Option, 0, len(pairs))
	for _, p := range pairs {
		v, t, ok := cut(p)
		if !ok {
			v, t = p, p
		}
		out = append(out, Option{v, t})
	}
	return out
}

func cut(p string) (string, string, bool) {
	for i := 0; i < len(p); i++ {
		if p[i] == '=' {
			return p[:i], p[i+1:], true
		}
	}
	return "", "", false
}

// rng is the options from..to with value = text.
func rng(from, to int) []Option {
	var out []Option
	for i := from; i <= to; i++ {
		s := strconv.Itoa(i)
		out = append(out, Option{s, s})
	}
	return out
}

// rngf is the options from..to with the text given by f.
func rngf(from, to int, f func(int) string) []Option {
	var out []Option
	for i := from; i <= to; i++ {
		out = append(out, Option{strconv.Itoa(i), f(i)})
	}
	return out
}

var (
	offOn     = opts("0=OFF", "1=ON")
	enable    = opts("0=Disable", "1=Enable")
	noYes     = opts("0=NO", "1=YES")
	sens      = opts("0=Sensitivity OFF", "1=Sensitivity Low", "2=Sensitivity Normal", "3=Sensitivity High", "4=Sensitivity Highest")
	alarmMode = opts("0=Mute", "1=Buzzer-Short", "2=Buzzer-Long", "3=Alarm-Short", "4=Alarm-Long")
	recTime   = opts("0=5 s", "1=10 s", "2=15 s", "3=20 s", "4=25 s", "5=30 s", "6=45 s", "7=60 s", "8=90 s", "9=120 s", "10=150 s", "11=180 s")
	alarmTime = append(opts("0=Nonstop"), rngf(1, 12, func(i int) string { return fmt.Sprintf("%d s", i*5) })...)
	trigTime  = append(opts("0=OFF"), rngf(1, 30, func(i int) string { return fmt.Sprintf("%d s", i) })...)
	trigType  = opts("0=Initial Trigger", "1=Continuous Trigger")
	beep      = opts("0=Mute", "1=Sound A", "2=Sound B", "3=Sound C")
	repSpeed  = opts("0=OFF", "1=Slow", "2=Normal", "3=Fast")
	seqTime   = append(opts("0=OFF"), rngf(1, 29, func(i int) string { return fmt.Sprintf("%d s", i+1) })...)
	quality   = opts("0=Lowest", "1=Low", "2=Standard", "3=High", "4=Highest")
	month     = opts("0=No Definition", "1=January", "2=February", "3=March", "4=April", "5=May", "6=June", "7=July",
		"8=August", "9=September", "10=October", "11=November", "12=December")
	week    = opts("0=No Definition", "1=First Week", "2=Second Week", "3=Third Week", "4=Fourth Week", "5=Last Week")
	weekday = opts("0=No Definition", "1=Sunday", "2=Monday", "3=Tuesday", "4=Wednesday", "5=Thursday", "6=Friday", "7=Saturday")

	octet    = regexp.MustCompile(`^[0-9]{0,3}$`)
	digits   = regexp.MustCompile(`^[0-9]*$`)
	hostName = regexp.MustCompile(`^[.0-9A-Za-z]*$`)
	ascii    = regexp.MustCompile(`^[\x20-\x7e]*$`)
	schedule = regexp.MustCompile(`^[0-4]{24}$`)
)

func ch() Field { return Field{Name: "ch", Kind: Hidden, Label: "Channel"} }

func sel(name, label string, o []Option) Field {
	return Field{Name: name, Label: label, Kind: Select, Options: o}
}

func text(name, label string, max int, re *regexp.Regexp) Field {
	return Field{Name: name, Label: label, Kind: Text, Max: max, Pattern: re}
}

func display(name, label string) Field { return Field{Name: name, Label: label, Kind: Display} }

// octets is a group of IPv4 octet fields name0..name3.
func octets(name, label string, kind Kind) []Field {
	var out []Field
	for i := range 4 {
		f := Field{Name: fmt.Sprintf("%s%d", name, i), Label: fmt.Sprintf("%s (octet %d)", label, i+1), Kind: kind, Max: 3, Pattern: octet}
		out = append(out, f)
	}
	return out
}

func displayMAC(name, label string) []Field {
	var out []Field
	for i := range 6 {
		out = append(out, display(fmt.Sprintf("%s%d", name, i), fmt.Sprintf("%s (byte %d)", label, i+1)))
	}
	return out
}

func is(field, value string) func(func(string) string) bool {
	return func(get func(string) string) bool { return get(field) == value }
}

func isNot(field, value string) func(func(string) string) bool {
	return func(get func(string) string) bool { return get(field) != value }
}

func join(groups ...[]Field) []Field {
	var out []Field
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// Pages are the settings pages, from the field table in docs/protocol.md ("Web settings API").
var Pages = []Page{
	{Name: "ChannSet", Title: "Channel Setup", Section: "Channels", PerChannel: true, Perm: 7, Fields: []Field{
		ch(),
		sel("act", "Active Channel", offOn),
		sel("rec", "Record Channel", offOn),
		text("name", "Channel Name", 24, ascii),
		sel("hidd", "Hidden Channel", noYes),
		{Name: "rota", Label: "Rotation Time (s)", Kind: Select, Options: rng(2, 30)},
		sel("bri", "Brightness", rng(1, 20)),
		sel("contra", "Contrast", rng(1, 20)),
		sel("hue", "Hue", rng(1, 32)),
		sel("satur", "Saturation", rng(1, 32)),
		sel("sharp", "Sharpness", rng(1, 16)),
		sel("stc", "Activate Special Color", enable),
		{Name: "sStart", Label: "Special Color Start Time", Kind: Select, Options: tenMinutes(), Note: "hour*6 + ten-minute index"},
		{Name: "sEnd", Label: "Special Color End Time", Kind: Select, Options: tenMinutes()},
		sel("sBri", "Special Brightness", rng(1, 20)),
		sel("sContra", "Special Contrast", rng(1, 20)),
		sel("sHue", "Special Hue", rng(1, 32)),
		sel("sSatur", "Special Saturation", rng(1, 32)),
		sel("sSharp", "Special Sharpness", rng(1, 16)),
	}},
	{Name: "RecSet", Title: "Record Setup", Section: "Recording", PerChannel: true, Perm: 7, Fields: []Field{
		{Name: "ar", Label: "Auto Record", Kind: Select, Options: append(opts("0=OFF"), rngf(1, 6, func(i int) string { return fmt.Sprintf("%d s", i*10) })...), Note: "applies to all channels (inferred)"},
		{Name: "pgu", Label: "Activate Performance Gains", Kind: Select, Options: enable, Note: "applies to all channels (inferred)"},
		sel("resol", "Record Resolution", opts("1=Low Res (CIF)", "2=Middle Res (Half D1)", "3=High Res (D1)")),
		sel("video", "Video Quality", quality),
		{Name: "frame", Label: "Record Framerate (fps)", Kind: Select, Options: rng(1, 30), Note: "1..25 on PAL systems"},
		{Name: "perf", Label: "Record Performance Gains", Kind: Select, Options: opts("1=YES", "0=NO"), When: isNot("pgu", "0")},
		ch(),
		display("fvk", "Video system (0 = NTSC)"),
	}},
	{Name: "Schedule", Title: "Record Schedule", Section: "Recording", PerChannel: true, Perm: 7,
		Note: "one character per hour 0..23: 0 none, 1 time, 2 motion, 3 sensor, 4 motion and sensor",
		Fields: []Field{
			ch(),
			text("day0", "Sunday", 24, schedule), text("day1", "Monday", 24, schedule), text("day2", "Tuesday", 24, schedule),
			text("day3", "Wednesday", 24, schedule), text("day4", "Thursday", 24, schedule), text("day5", "Friday", 24, schedule),
			text("day6", "Saturday", 24, schedule),
		}},
	{Name: "Alert", Title: "Alarm Setup", Section: "Detection", PerChannel: true, Perm: 7, Fields: []Field{
		ch(),
		sel("ndm", "Light Detect Mode", sens),
		sel("nda", "Light Detect Alarm Mode", alarmMode),
		sel("bdm", "Blind Detect Mode", sens),
		sel("bda", "Blind Detect Alarm Mode", alarmMode),
		sel("vlm", "Video Loss Mode", offOn),
		sel("vla", "Video Loss Alarm Mode", alarmMode),
	}},
	{Name: "Mot", Title: "Motion Setup", Section: "Detection", PerChannel: true, Perm: 7, Fields: []Field{
		ch(),
		sel("rda", "Record Detection Activate", enable),
		sel("motm", "Motion Mode", sens),
		sel("motrt", "Record Time", recTime),
		sel("motam", "Alarm Mode", alarmMode),
		sel("motat", "Alarm Time", alarmTime),
		sel("motdt", "Trigger Full Screen", trigTime),
		sel("mottt", "Trigger Type", trigType),
	}},
	{Name: "Sensor", Title: "Sensor Setup", Section: "Detection", PerChannel: true, Perm: 7, Fields: []Field{
		ch(),
		sel("rda", "Record Detection Activate", enable),
		sel("sem", "Sensor Mode", enable),
		sel("sm1", "Sensor Mapping, input 1", opts("0=Not installed", "1=Normal-Open", "2=Normal-Close")),
		sel("sm2", "Sensor Mapping, input 2", opts("0=Not installed", "1=Normal-Open", "2=Normal-Close")),
		sel("sm3", "Sensor Mapping, input 3", opts("0=Not installed", "1=Normal-Open", "2=Normal-Close")),
		sel("sm4", "Sensor Mapping, input 4", opts("0=Not installed", "1=Normal-Open", "2=Normal-Close")),
		sel("sert", "Record Time", recTime),
		sel("seam", "Alarm Mode", alarmMode),
		sel("seat", "Alarm Time", alarmTime),
		sel("setd", "Trigger Full Screen", trigTime),
		sel("sett", "Trigger Type", trigType),
	}},
	{Name: "MotArea", Title: "Motion Area", Section: "Detection", PerChannel: true, Perm: 7, ReadOnly: true,
		Note: "one rectangle; its writer exists only in disabled code of the DVR's UI"},
	{Name: "PTZ", Title: "Pan/Tilt/Zoom Setup", Section: "Channels", PerChannel: true, Perm: 7, Fields: []Field{
		ch(),
		{Name: "proto", Label: "PTZ Protocol", Kind: Select, Options: rngf(0, 12, func(i int) string {
			if i == 0 {
				return "None"
			}
			return fmt.Sprintf("protocol %d", i)
		}), Note: "see docs/protocol.md for the protocol list"},
		sel("cid", "Camera ID", rng(1, 8)),
		sel("pans", "Pan Speed", rng(1, 8)),
		sel("tilts", "Tilt Speed", rng(1, 8)),
		sel("lss", "Cruise Speed", rng(0, 3)),
		sel("autos", "Auto Pan Speed", rng(0, 5)),
		sel("zooms", "Zoom Speed", rng(0, 3)),
		sel("focus", "Focus Speed", rng(0, 3)),
		sel("iris", "Iris Speed", rng(0, 3)),
		sel("afs", "Auto Focus", enable),
		sel("ais", "Auto Iris", enable),
	}},
	{Name: "SysComm", Title: "System Setup", Section: "System", Perm: 7,
		Note: "the DVR's form sends the full screen sequence as a first beepm parameter; this page does the same",
		Fields: []Field{
			sel("lang", "Language Selection", opts("0=English", "1=Chinese", "2=Spanish", "3=Russian", "4=Persian", "5=German",
				"6=Greek", "7=Thai", "8=Hungarian", "9=Italian", "10=Japanese", "11=French")),
			{Name: "ssf", Param: "beepm", Label: "Full Screen Sequence", Kind: Select, Options: offOn},
			sel("ss4", "4 Split Sequence", seqTime),
			sel("ss8", "1+7 Split Sequence", seqTime),
			sel("ss9", "9 Split Sequence", seqTime),
			sel("beepm", "Beep of Mouse Buttons", beep),
			sel("beepk", "Beep of Keypad", beep),
			sel("beepi", "Beep of IR Remote", beep),
			sel("mms", "Mouse Moving Speed", opts("1=Slow", "2=Normal", "3=Fast")),
			sel("mrs", "Mouse Repeat Speed", repSpeed),
			sel("krs", "Keypad Repeat Speed", repSpeed),
			sel("irs", "IR Repeat Speed", repSpeed),
			{Name: "auto", Label: "Auto Exit Menu", Kind: Select, Options: append(opts("0=OFF"), rngf(1, 6, func(i int) string { return fmt.Sprintf("%d s", i*10) })...)},
		}},
	{Name: "View", Title: "View Setup", Section: "System", Perm: 7,
		Note: "these settings affect only the DVR's own monitor output, not the network stream",
		Fields: []Field{
			sel("mgm", "Main Menu Graphics Mode", offOn),
			sel("dynef", "Channel Dynamic Effects", offOn),
			sel("icoh", "Icons Help", offOn),
			sel("erri", "Error Messages", offOn),
			sel("all", "All State Bar Information", offOn),
			{Name: "recl", Param: "srecl", Label: "Record Light (state bar)", Kind: Select, Options: offOn},
			sel("net", "Network Information", offOn),
			sel("hard", "Hard Drive Information", offOn),
			sel("date", "Date/Time Information", offOn),
			sel("rota", "Rotate Information", offOn),
			sel("chall", "All Channels Information", offOn),
			sel("chnum", "Channel Number", offOn),
			sel("chn", "Channel Name", offOn),
			sel("crecl", "Record Light (channel)", offOn),
			sel("cvl", "Video Loss", offOn),
			sel("crm", "Record Mode", offOn),
			sel("cdm", "Detector Mode", offOn),
			sel("cav", "Alarm Display", offOn),
		}},
	{Name: "DateTime", Title: "Date/Time Setup", Section: "System", Perm: 7,
		Note: "Synchronize Now (NTPClient.xml) is an action and not offered",
		Fields: []Field{
			sel("mode", "Date Display Mode", opts("0=YYYY/MM/DD", "1=DD/MM/YYYY", "2=MM/DD/YYYY")),
			{Name: "zone", Label: "Time Zone (index)", Kind: Text, Max: 3, Pattern: digits, Note: "index into the DVR's time zone list, not documented"},
			sel("dlact", "Activate Daylight Saving", enable),
			sel("smon", "DST starts: month", month),
			sel("sweek", "DST starts: week", week),
			sel("swday", "DST starts: day", weekday),
			sel("shour", "DST starts: hour", rng(0, 23)),
			sel("emon", "DST ends: month", month),
			sel("eweek", "DST ends: week", week),
			sel("ewday", "DST ends: day", weekday),
			sel("ehour", "DST ends: hour", rng(0, 23)),
			sel("ntp", "NTP: Activate Service", enable),
			text("server", "NTP: Server", 64, hostName),
			{Name: "inter", Label: "NTP: Interval (days)", Kind: Select, Options: rng(1, 7)},
			{Name: "DvrUtcTime", Kind: Const, Value: "0"},
			display("utc", "DVR clock (Unix seconds)"),
		}},
	{Name: "Network", Title: "Network Setup", Section: "Network", Perm: 7,
		Note:        "changing the IP setup can make the DVR unreachable",
		SecretField: regexp.MustCompile(`^(ppp|ddnpw|spwd|cpwd)$`),
		Fields: join(
			[]Field{
				sel("act", "Network Enable", enable),
				sel("ipt", "IP Type", opts("0=Static", "1=DHCP", "2=PPPoE")),
			},
			octets("sip", "Static IP Address", Text),
			octets("sgw", "Static Gateway", Text),
			[]Field{
				display("ssub0", "Static Subnet Mask (octet 1)"), display("ssub1", "Static Subnet Mask (octet 2)"),
				{Name: "ssub2", Label: "Static Subnet Mask (octet 3)", Kind: Text, Max: 3, Pattern: octet},
				{Name: "ssub3", Label: "Static Subnet Mask (octet 4)", Kind: Text, Max: 3, Pattern: octet},
			},
			displayMAC("smac", "MAC Address"),
			octets("dip", "DHCP IP Address", Display), octets("dgw", "DHCP Gateway", Display),
			octets("dsub", "DHCP Subnet Mask", Display),
			octets("pip", "PPPoE IP Address", Display), octets("pgw", "PPPoE Gateway", Display),
			octets("psub", "PPPoE Subnet Mask", Display),
			[]Field{
				{Name: "pppa", Label: "PPPoE Account ID", Kind: Text, Max: 64, Pattern: ascii, When: is("ipt", "2")},
				{Name: "ppp", Label: "PPPoE Password", Kind: Secret, Max: 64, When: is("ipt", "2")},
				sel("mma", "Management Services", enable),
				sel("moa", "Monitoring Services", enable),
				text("mop", "Monitoring Port", 5, digits),
				sel("vq", "Video Transmission Quality", quality),
				sel("aut", "Audio Transmission", offOn),
			},
			octets("dns1", "Default DNS Address", Display),
			octets("dns2", "Custom DNS Address", Text),
			[]Field{
				sel("ddns", "DDNS: Activate Service", enable),
				sel("ddnp", "DDNS: Provider", rng(0, 2)),
				text("ddnh", "DDNS: Server Name", 35, hostName),
				text("ddna", "DDNS: Account", 27, ascii),
				{Name: "ddnpw", Label: "DDNS: Password", Kind: Secret, Max: 27},
			},
		)},
	{Name: "Screen", Title: "Screen Setup", Section: "System", Perm: 7, Fields: []Field{
		sel("auto", "NTSC/PAL Auto Detection", offOn),
		{Name: "type", Label: "Video Output Format", Kind: Select, Options: opts("0=NTSC", "1=PAL"), When: isNot("auto", "1")},
		sel("bord", "Screen Border", offOn),
		sel("md", "Main Display Screen", opts("0=BNC", "1=VGA")),
		sel("vga", "VGA Resolution", opts("0=640x480", "1=800x600", "2=1024x768", "3=1280x1024")),
	}},
	{Name: "Key", Title: "Keyboard Setup", Section: "System", Perm: 7, Fields: []Field{
		sel("baud", "RS-485 Baudrate", opts("0=1200", "1=1800", "2=2400", "3=4800", "4=9600", "5=14400", "6=19200", "7=38400", "8=57600", "9=115200")),
		sel("data", "RS-485 Databit", opts("0=7 Bit", "1=8 Bit")),
		sel("pari", "RS-485 Parity", opts("0=No Parity", "1=Odd", "2=Even")),
		sel("stop", "RS-485 Stopbit", opts("0=1 Stopbit", "1=2 Stopbit")),
		sel("mod", "Keyboard Model", opts("0=OFF", "1=model 1", "2=model 2", "3=model 3")),
		sel("keyid", "Keyboard ID", rngf(0, 15, func(i int) string { return strconv.Itoa(i + 1) })),
		sel("keyp", "Keyboard Protocol (model 1)", rng(0, 7)),
		sel("keyp1", "Keyboard Protocol (model 3)", opts("0=D variant", "1=P variant")),
	}},
	{Name: "Audio", Title: "Audio Setup", Section: "Recording", Perm: 7, Note: "the DVR's form always uses ch=1",
		Fields: []Field{
			{Name: "ch", Kind: Const, Value: "1"},
			sel("rec", "Audio Record", offOn),
			sel("mut", "Audio Mute", offOn),
			sel("inv", "Input Volume", rngf(0, 15, func(i int) string { return strconv.Itoa(i + 1) })),
			sel("outv", "Output Volume", rngf(0, 15, func(i int) string { return strconv.Itoa(i + 1) })),
		}},
	{Name: "CgiAuth", Title: "Accounts", Section: "System", Perm: 0, ReadOnly: true,
		SecretField: regexp.MustCompile(`^p[0-9]+$`),
		Note:        "read only here: writing accounts would resend every password; the passwords are never shown"},
	{Name: "Port", Title: "Ports", Section: "Network", Perm: 8, ReadOnly: true},
}

// tenMinutes is the special color time: hour*6 + ten-minute index, 0..143.
func tenMinutes() []Option {
	return rngf(0, 143, func(i int) string { return fmt.Sprintf("%02d:%02d", i/6, i%6*10) })
}

// FindPage returns the page with the given name.
func FindPage(name string) *Page {
	for i := range Pages {
		if Pages[i].Name == name {
			return &Pages[i]
		}
	}
	return nil
}

// ReadChannel is the ch parameter of the page's read for the selected channel; 0 means none.
func (p *Page) ReadChannel(ch int) int {
	if p.Name == "Audio" {
		return 1 // the DVR's own form always uses ch=1
	}
	if p.PerChannel {
		return ch
	}
	return 0
}
