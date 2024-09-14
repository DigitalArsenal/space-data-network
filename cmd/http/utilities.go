package httpserver

import (
	"net"
)

// Helper function to extract the node's IP address
func getNodeIPAddress(node *Node) string {
	// Attempt to retrieve IP addresses from the node's multiaddresses
	for _, addr := range node.Host.Addrs() {
		ip, err := addr.ValueForProtocol(net.IPv4len)
		if err == nil {
			return ip // Return the first valid IP address found
		}
	}
	return ""
}
