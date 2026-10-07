package scanner

import (
	"context"
	"net"
	"sync"
	"time"
)

// DNSRecord is a name/value discovered during enumeration.
type DNSRecord struct {
	HostIP string
	Name   string
	Type   string
	Value  string
}

// ResolveHostnames is phase 2: it reverse-resolves (PTR) each live IP to its
// hostname and forward-resolves that name back to A records. Lookups run in
// parallel so large batches of live hosts do not serialize on DNS timeouts.
// Returns only the hosts that resolved, plus the DNS records found.
func ResolveHostnames(ctx context.Context, resolver string, ips []string) ([]Host, []DNSRecord) {
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

	type result struct {
		host    Host
		records []DNSRecord
	}
	results := make([]result, len(ips))
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			names, err := r.LookupAddr(lookupCtx, ip)
			cancel()
			if err != nil || len(names) == 0 {
				return
			}
			res := result{host: Host{IP: ip, Hostname: trimDot(names[0])}}
			for _, name := range names {
				name = trimDot(name)
				res.records = append(res.records, DNSRecord{HostIP: ip, Name: name, Type: "PTR", Value: ip})
				fctx, fcancel := context.WithTimeout(ctx, 3*time.Second)
				addrs, ferr := r.LookupHost(fctx, name)
				fcancel()
				if ferr == nil {
					for _, a := range addrs {
						res.records = append(res.records, DNSRecord{HostIP: ip, Name: name, Type: "A", Value: a})
					}
				}
			}
			results[i] = res
		}(i, ip)
	}
	wg.Wait()

	var hosts []Host
	var records []DNSRecord
	seen := map[string]bool{}
	for _, res := range results {
		if res.host.IP == "" {
			continue
		}
		hosts = append(hosts, res.host)
		for _, rec := range res.records {
			key := rec.Type + "|" + rec.Name + "|" + rec.Value
			if !seen[key] {
				seen[key] = true
				records = append(records, rec)
			}
		}
	}
	return hosts, records
}

func trimDot(s string) string {
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
