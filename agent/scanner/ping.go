package scanner

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"time"
)

// PingSweep is phase 1: it sends one ICMP echo request to every address of the
// configured ranges (no nmap involved) and streams the hosts that reply to
// onBatch while the sweep runs, so each live host can be stored right away.
//
// Packets are paced at rate per second. After the last request it waits
// timeout for late replies. It needs a raw ICMP socket (root / CAP_NET_RAW).
func PingSweep(ctx context.Context, ranges []string, rate int, timeout time.Duration, onBatch func([]Host)) error {
	if rate <= 0 {
		rate = 1000
	}
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("open ICMP socket (needs root/CAP_NET_RAW): %w", err)
	}
	defer conn.Close()

	id := uint16(os.Getpid() & 0xffff)
	targets := make([]*net.IPNet, 0, len(ranges))
	for _, r := range ranges {
		_, n, err := net.ParseCIDR(r)
		if err != nil {
			if ip := net.ParseIP(r).To4(); ip != nil { // single address
				n = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
			} else {
				return fmt.Errorf("invalid range %q", r)
			}
		}
		if n.IP.To4() == nil {
			return fmt.Errorf("only IPv4 ranges are supported: %q", r)
		}
		targets = append(targets, n)
	}

	// Receiver: collects echo replies carrying our identifier.
	found := make(chan Host, 1024)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		defer close(found)
		seen := map[string]bool{}
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return // socket closed (sweep finished or cancelled)
			}
			if n < 8 || buf[0] != 0 /* echo reply */ || binary.BigEndian.Uint16(buf[4:6]) != id {
				continue
			}
			ip := addr.(*net.IPAddr).IP.String()
			if seen[ip] {
				continue
			}
			seen[ip] = true
			found <- Host{IP: ip, Status: "up"}
		}
	}()

	// Streamer: hands live hosts to onBatch from a single goroutine, flushing
	// every 25 hosts or every 2 seconds.
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		const maxBatch = 25
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		var batch []Host
		flush := func() {
			if len(batch) > 0 {
				onBatch(batch)
				batch = nil
			}
		}
		for {
			select {
			case h, ok := <-found:
				if !ok {
					flush()
					return
				}
				if batch = append(batch, h); len(batch) >= maxBatch {
					flush()
				}
			case <-tick.C:
				flush()
			}
		}
	}()

	sendErr := sendEchoes(ctx, conn, targets, id, rate)

	// Wait for late replies (unless stopped), then close the socket to end the
	// receiver and let the streamer flush what is left.
	if ctx.Err() == nil && sendErr == nil {
		select {
		case <-ctx.Done():
		case <-time.After(timeout):
		}
	}
	conn.Close()
	<-recvDone
	<-streamDone

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return sendErr
}

// sendEchoes walks every host address of the targets and sends one echo
// request each, paced to rate packets per second.
func sendEchoes(ctx context.Context, conn net.PacketConn, targets []*net.IPNet, id uint16, rate int) error {
	const slot = 10 * time.Millisecond
	perSlot := rate / int(time.Second/slot)
	if perSlot < 1 {
		perSlot = 1
	}
	pace := time.NewTicker(slot)
	defer pace.Stop()

	var seq uint16
	sent, ok := 0, 0
	var lastErr error
	for _, n := range targets {
		first, last := hostBounds(n)
		for a := first; ; a++ {
			if sent == perSlot {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-pace.C:
				}
				sent = 0
			}
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, a)
			seq++
			// Per-destination send errors (e.g. unreachable) are normal during a
			// sweep; only a sweep where nothing could be sent is an error.
			if _, err := conn.WriteTo(echoRequest(id, seq), &net.IPAddr{IP: ip}); err != nil {
				lastErr = err
			} else {
				ok++
			}
			sent++
			if a == last {
				break
			}
		}
	}
	if ok == 0 && lastErr != nil {
		return fmt.Errorf("no ICMP echo could be sent: %w", lastErr)
	}
	return nil
}

// hostBounds returns the first and last usable host address of a network,
// skipping the network and broadcast addresses for prefixes shorter than /31.
func hostBounds(n *net.IPNet) (uint32, uint32) {
	base := binary.BigEndian.Uint32(n.IP.To4())
	ones, bits := n.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	first, last := base, base+size-1
	if bits-ones >= 2 {
		first++
		last--
	}
	return first, last
}

// echoRequest builds an ICMP echo request (type 8) with a checksum.
func echoRequest(id, seq uint16) []byte {
	b := make([]byte, 16)
	b[0] = 8 // echo request
	binary.BigEndian.PutUint16(b[4:6], id)
	binary.BigEndian.PutUint16(b[6:8], seq)
	copy(b[8:], "ntm-ping")
	binary.BigEndian.PutUint16(b[2:4], checksum(b))
	return b
}

func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
