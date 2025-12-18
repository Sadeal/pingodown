package firewall

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

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

	// Check if rule already exists
	checkCmd := exec.Command("iptables", "-t", "nat", "-L", "PREROUTING", "-n", "-v")
	output, err := checkCmd.CombinedOutput()
	if err == nil {
		outputStr := string(output)
		if strings.Contains(outputStr, fmt.Sprintf("tcp dpt:%d", port)) {
			f.logger.Info("Redirect rule already exists for port %d -> %d", port, redirectPort)
			return nil
		}
	}

	// Add redirect rule: all traffic to port 27055 -> 27056
	cmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-A", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", port),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", redirectPort),
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables redirect rule: %w", err)
	}

	f.logger.Info("Added iptables redirect: %d -> %d", port, redirectPort)
	return nil
}

// RemoveRedirect removes iptables rule for port redirection
func (f *firewall) RemoveRedirect(port, redirectPort int) error {
	// Check if running as root
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection removal requires root privileges")
		return fmt.Errorf("port redirection removal requires root privileges, run with 'sudo'")
	}

	// Remove redirect rule
	cmd := exec.Command(
		"iptables",
		"-t", "nat",
		"-D", "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", port),
		"-j", "REDIRECT",
		"--to-port", fmt.Sprintf("%d", redirectPort),
	)

	if err := cmd.Run(); err != nil {
		f.logger.Warn("Failed to remove iptables redirect rule: %v (it may not exist)", err)
		// Not fatal - rule might not exist
		return nil
	}

	f.logger.Info("Removed iptables redirect: %d -> %d", port, redirectPort)
	return nil
}
