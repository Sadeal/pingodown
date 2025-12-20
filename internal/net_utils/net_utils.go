package net_utils

import (
	"fmt"
	"net"
)

// GetServerExternalIP returns the external IP of the current server
// Filters out loopback (127.0.0.1), link-local, and multicast addresses
func GetServerExternalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %w", err)
	}

	for _, iface := range interfaces {
		// Skip down or loopback interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipNet.IP
			// Skip IPv6, link-local, and multicast
			if ip.To4() == nil || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
				continue
			}

			// Found a valid IPv4 address
			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no valid external IP found on any interface")
}
