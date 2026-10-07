package scanner

import (
	"bufio"
	"os"
	"strings"
)

// ARPEntry maps an IP to a MAC address from the local neighbour cache.
type ARPEntry struct {
	IP  string
	MAC string
}

// ReadARPTable reads the kernel ARP/neighbour cache from /proc/net/arp (Linux).
// This supplies MAC addresses for hosts on the same L2 segment, enriching the
// inventory with vendor/hardware data. Returns an empty slice off Linux.
func ReadARPTable() []ARPEntry {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []ARPEntry
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first { // header: IP address HW type Flags HW address Mask Device
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		ip := fields[0]
		mac := fields[3]
		if mac == "00:00:00:00:00:00" || mac == "" {
			continue
		}
		entries = append(entries, ARPEntry{IP: ip, MAC: strings.ToUpper(mac)})
	}
	return entries
}

// FillMAC fills in MAC addresses on live hosts from the ARP cache when nmap did
// not resolve them (e.g. unprivileged scans). It never adds hosts: only hosts
// that answered the ping are part of the inventory.
func FillMAC(hosts []Host, arp []ARPEntry) []Host {
	byIP := make(map[string]string, len(arp))
	for _, e := range arp {
		byIP[e.IP] = e.MAC
	}
	for i := range hosts {
		if hosts[i].MAC == "" {
			if mac, ok := byIP[hosts[i].IP]; ok {
				hosts[i].MAC = mac
			}
		}
	}
	return hosts
}
