package db

import (
	"fmt"
	"net"

	"github.com/valid/ntm/platform/internal/models"
)

// BuildTopology turns a flat host list into a graph for the Network Map:
// Internet edge -> subnet (gateway) -> host. Hosts are grouped by their /24 so
// the map shows interconnections of systems up to the Internet border.
func BuildTopology(hosts []models.Host) map[string]any {
	type node struct {
		Data map[string]any `json:"data"`
	}
	type edge struct {
		Data map[string]any `json:"data"`
	}

	nodes := []node{}
	edges := []edge{}
	seenSubnet := map[string]bool{}

	// Internet edge node (the border).
	nodes = append(nodes, node{Data: map[string]any{
		"id": "internet", "label": "Internet", "kind": "internet",
	}})

	for _, h := range hosts {
		ip := net.ParseIP(h.IP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		v4 := ip.To4()
		subnetID := fmt.Sprintf("net-%d.%d.%d.0", v4[0], v4[1], v4[2])
		subnetLabel := fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])

		if !seenSubnet[subnetID] {
			seenSubnet[subnetID] = true
			nodes = append(nodes, node{Data: map[string]any{
				"id": subnetID, "label": subnetLabel, "kind": "subnet",
			}})
			edges = append(edges, edge{Data: map[string]any{
				"id": "e-" + subnetID + "-internet", "source": subnetID, "target": "internet",
			}})
		}

		label := h.Hostname
		if label == "" {
			label = h.IP
		}
		hostNodeID := fmt.Sprintf("host-%d", h.ID)
		nodes = append(nodes, node{Data: map[string]any{
			"id":       hostNodeID,
			"label":    label,
			"kind":     "host",
			"assety":   h.AssetType,
			"status":   h.Status,
			"ip":       h.IP,
			"os":       h.OSGuess,
			"numports": len(h.Ports),
		}})
		edges = append(edges, edge{Data: map[string]any{
			"id": "e-" + hostNodeID, "source": hostNodeID, "target": subnetID,
		}})
	}

	return map[string]any{"nodes": nodes, "edges": edges}
}
