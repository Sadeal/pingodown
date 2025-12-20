package net_utils

import (
	"fmt"
	"net"
)

// GetServerExternalIP returns the external IP of the current server
func GetServerExternalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %w", err)
	}

	for _, iface := range interfaces {
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
			if ip.To4() == nil || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no valid external IP found on any interface")
}

// IsLocalIP checks if the given IP string belongs to the local machine
func IsLocalIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	
	// Check loopback
	if ip.IsLoopback() {
		return true
	}

	// Check all interface addresses
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}

	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var localIP net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				localIP = v.IP
			case *net.IPAddr:
				localIP = v.IP
			}
			if localIP.Equal(ip) {
				return true
			}
		}
	}
	return false
}
