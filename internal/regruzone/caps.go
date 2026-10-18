package regruzone

import "github.com/lavr/regru-cli/internal/zone"

func Capabilities() map[zone.Type]zone.Cap {
	full := zone.Cap{Read: true, Write: true, ExactDelete: true}
	return map[zone.Type]zone.Cap{
		zone.A:     full,
		zone.AAAA:  full,
		zone.CNAME: full,
		zone.MX:    full,
		zone.TXT:   full,
		zone.NS:    full,
		zone.SRV:   full,
		zone.CAA:   full,
		zone.HTTPS: {Read: true},
	}
}

func IsProtected(k zone.Key) bool {
	return k.Type == zone.NS && k.Name == "@"
}

func writable(t zone.Type) bool {
	c, ok := Capabilities()[t]
	return ok && c.Write
}
