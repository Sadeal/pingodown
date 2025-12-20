package firewall

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/qdm12/golibs/logging"
)

type Firewall interface {
	AddRedirect(port, redirectPort int) error
	RemoveRedirect(port, redirectPort int) error
}

type firewall struct {
	logger logging.Logger
}

func NewFirewall(logger logging.Logger) Firewall {
	return &firewall{
		logger: logger,
	}
}

// AddRedirect adds iptables rule to redirect port to redirectPort
// Example: AddRedirect(27055, 27056) redirects 27055 -> 27056
func (f *firewall) AddRedirect(port, redirectPort int) error {
	// Check if running as root
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection requires root privileges")
		return fmt.Errorf("port redirection requires root privileges, run with 'sudo'")
	}

	// Check if rule already exists using -C (check) flag
	f.logger.Info("Checking if redirect rule already exists for port %d -> %d", port, redirectPort)
	checkCmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-C", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", port),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", redirectPort),
	)

	// Suppress stderr since -C returns error if rule doesn't exist (that's expected)
	checkCmd.Stderr = nil
	if err := checkCmd.Run(); err == nil {
		f.logger.Info("Redirect rule already exists for port %d -> %d", port, redirectPort)
		return nil
	}

	// Add redirect rule: all traffic to port -> redirectPort (UDP)
	f.logger.Info("Adding iptables redirect rule: %d -> %d", port, redirectPort)
	cmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-A", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", port),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", redirectPort),
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		errMsg := string(output)
		f.logger.Error("Failed to add iptables redirect rule: %v, output: %s", err, errMsg)
		return fmt.Errorf("failed to add iptables redirect rule: %w", err)
	}

	f.logger.Info("Successfully added iptables redirect: %d -> %d", port, redirectPort)
	return nil
}

// RemoveRedirect removes iptables rule for port redirection
func (f *firewall) RemoveRedirect(port, redirectPort int) error {
	// Check if running as root
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection removal requires root privileges")
		return fmt.Errorf("port redirection removal requires root privileges, run with 'sudo'")
	}

	// Remove redirect rule - MUST match exactly what was added
	f.logger.Info("Removing iptables redirect rule: %d -> %d", port, redirectPort)
	cmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-D", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", port),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", redirectPort),
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		errMsg := string(output)
		f.logger.Warn("Failed to remove iptables redirect rule: %v, output: %s (it may not exist)", err, errMsg)
		// Not fatal - rule might not exist
		return nil
	}

	f.logger.Info("Successfully removed iptables redirect: %d -> %d", port, redirectPort)
	return nil
}
