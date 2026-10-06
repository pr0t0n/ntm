package scanner

import (
	"context"
	"net"
	"time"
)

// DNSRecord is a name/value discovered during enumeration.
type DNSRecord struct {
	HostIP string
	Name   string
	Type   string
	Value  string
}

// EnumerateDNS performs reverse (PTR) lookups for discovered hosts and resolves
// each discovered name back to A records. This enriches inventory records with
// hostnames for assets on the operator's own network.
func EnumerateDNS(ctx context.Context, resolver string, hosts []Host) []DNSRecord {
	r := &net.Resolver{}
	if resolver != "" {
		r = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(ctx, network, net.JoinHostPort(resolver, "53"))
			},
		}
	}

	var records []DNSRecord
	seen := map[string]bool{}

	for _, h := range hosts {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		names, err := r.LookupAddr(lookupCtx, h.IP)
		cancel()
		if err == nil {
			for _, name := range names {
				name = trimDot(name)
				key := "PTR|" + h.IP + "|" + name
				if seen[key] {
					continue
				}
				seen[key] = true
				records = append(records, DNSRecord{HostIP: h.IP, Name: name, Type: "PTR", Value: h.IP})

				// Forward-resolve the name to capture A records.
				fctx, fcancel := context.WithTimeout(ctx, 3*time.Second)
				addrs, ferr := r.LookupHost(fctx, name)
				fcancel()
				if ferr == nil {
					for _, a := range addrs {
						akey := "A|" + name + "|" + a
						if seen[akey] {
							continue
						}
						seen[akey] = true
						records = append(records, DNSRecord{HostIP: h.IP, Name: name, Type: "A", Value: a})
					}
				}
			}
		}
	}
	return records
}

func trimDot(s string) string {
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
