package firewall

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/qdm12/golibs/logging"
)

type Firewall interface {
	AddRedirect(port, redirectPort int, externalIP string) error
	RemoveRedirect(port, redirectPort int, externalIP string) error
}

type firewall struct {
	logger logging.Logger
}

func NewFirewall(logger logging.Logger) Firewall {
	return &firewall{
		logger: logger,
	}
}

// AddRedirect adds iptables rules:
// 1. PREROUTING: Redirect incoming traffic port -> redirectPort
// 2. POSTROUTING: SNAT outgoing traffic from redirectPort -> port (masquerade as original port)
func (f *firewall) AddRedirect(port, redirectPort int, externalIP string) error {
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection requires root privileges")
		return fmt.Errorf("port redirection requires root privileges, run with 'sudo'")
	}

	// 1. INCOMING: Redirect 27055 -> 27056
	// iptables -t nat -A PREROUTING -p udp --dport 27055 -j REDIRECT --to-port 27056
	f.runRule("PREROUTING", "-p", "udp", "--dport", fmt.Sprintf("%d", port), "-j", "REDIRECT", "--to-port", fmt.Sprintf("%d", redirectPort))

	// 2. OUTGOING: SNAT 27056 -> 27055
	// This ensures packets sent by our proxy (on 27056) look like they come from 27055
	// iptables -t nat -A POSTROUTING -p udp --sport 27056 -j SNAT --to-source IP:27055
	snatTarget := fmt.Sprintf("%s:%d", externalIP, port)
	err := f.runRule("POSTROUTING", "-p", "udp", "--sport", fmt.Sprintf("%d", redirectPort), "-j", "SNAT", "--to-source", snatTarget)
	
	if err != nil {
		f.logger.Error("Failed to add SNAT rule: %v", err)
		// Try to cleanup PREROUTING if SNAT fails
		f.RemoveRedirect(port, redirectPort, externalIP)
		return err
	}

	f.logger.Info("Firewall rules configured: Incoming %d->%d, Outgoing Source %d->%d", port, redirectPort, redirectPort, port)
	return nil
}

func (f *firewall) RemoveRedirect(port, redirectPort int, externalIP string) error {
	if os.Geteuid() != 0 {
		return nil
	}

	f.logger.Info("Removing firewall rules...")

	// Remove PREROUTING
	f.removeRule("PREROUTING", "-p", "udp", "--dport", fmt.Sprintf("%d", port), "-j", "REDIRECT", "--to-port", fmt.Sprintf("%d", redirectPort))

	// Remove POSTROUTING
	snatTarget := fmt.Sprintf("%s:%d", externalIP, port)
	f.removeRule("POSTROUTING", "-p", "udp", "--sport", fmt.Sprintf("%d", redirectPort), "-j", "SNAT", "--to-source", snatTarget)

	return nil
}

// Helper to check and add rule
func (f *firewall) runRule(chain string, args ...string) error {
	// 1. Check if exists
	checkArgs := append([]string{"-t", "nat", "-C", chain}, args...)
	if err := exec.Command("iptables", checkArgs...).Run(); err == nil {
		return nil // Rule exists
	}

	// 2. Add rule
	addArgs := append([]string{"-t", "nat", "-A", chain}, args...)
	cmd := exec.Command("iptables", addArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables error: %v, output: %s", err, string(output))
	}
	return nil
}

// Helper to remove rule
func (f *firewall) removeRule(chain string, args ...string) error {
	delArgs := append([]string{"-t", "nat", "-D", chain}, args...)
	exec.Command("iptables", delArgs...).Run() // Ignore errors if rule doesn't exist
	return nil
}
