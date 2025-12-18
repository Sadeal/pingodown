package firewall

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/qdm12/golibs/logging"
)

type Firewall interface {
	AddRedirect(fromPort, toPort int) error
	RemoveRedirect(fromPort, toPort int) error
}

type firewall struct {
	logger logging.Logger
}

func NewFirewall(logger logging.Logger) Firewall {
	return &firewall{
		logger: logger,
	}
}

// AddRedirect adds iptables rule to redirect fromPort to toPort
// Example: AddRedirect(27055, 27056) redirects all UDP traffic on 27055 -> 27056
func (f *firewall) AddRedirect(fromPort, toPort int) error {
	// Check if running as root
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection requires root privileges")
		return fmt.Errorf("port redirection requires root privileges, run with 'sudo'")
	}

	f.logger.Info("Setting up firewall redirect: UDP port %d -> %d", fromPort, toPort)

	// Try to add the rule - if it already exists, -A will just add a duplicate (which is fine)
	// But first check if rule exists to avoid spam
	checkCmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-C", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", fromPort),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", toPort),
	)

	// Suppress output since -C is just for checking
	checkCmd.Stdout = nil
	checkCmd.Stderr = nil

	if checkCmd.Run() == nil {
		f.logger.Info("Redirect rule already exists: UDP %d -> %d", fromPort, toPort)
		return nil
	}

	// Rule doesn't exist, add it
	f.logger.Info("Adding redirect rule: UDP %d -> %d", fromPort, toPort)
	addCmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-A", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", fromPort),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", toPort),
	)

	if output, err := addCmd.CombinedOutput(); err != nil {
		errMsg := string(output)
		f.logger.Error("Failed to add iptables redirect: %v, output: %s", err, errMsg)
		return fmt.Errorf("failed to add iptables redirect: %w", err)
	}

	f.logger.Info("Successfully added firewall redirect: UDP %d -> %d", fromPort, toPort)
	return nil
}

// RemoveRedirect removes iptables rule for port redirection
func (f *firewall) RemoveRedirect(fromPort, toPort int) error {
	// Check if running as root
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection removal requires root privileges")
		return fmt.Errorf("port redirection removal requires root privileges, run with 'sudo'")
	}

	f.logger.Info("Removing firewall redirect: UDP %d -> %d", fromPort, toPort)

	removeCmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-D", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", fromPort),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", toPort),
	)

	if output, err := removeCmd.CombinedOutput(); err != nil {
		errMsg := string(output)
		f.logger.Warn("Failed to remove iptables redirect: %v, output: %s (rule may not exist)", err, errMsg)
		// Not fatal - rule might not exist
		return nil
	}

	f.logger.Info("Successfully removed firewall redirect: UDP %d -> %d", fromPort, toPort)
	return nil
}
