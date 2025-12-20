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

// AddRedirect adds iptables rules with high priority (-I) to override Docker
func (f *firewall) AddRedirect(port, redirectPort int, externalIP string) error {
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection requires root privileges")
		return fmt.Errorf("port redirection requires root privileges, run with 'sudo'")
	}

	p := fmt.Sprintf("%d", port)
	rp := fmt.Sprintf("%d", redirectPort)
	snatTarget := fmt.Sprintf("%s:%d", externalIP, port)

	// 1. INCOMING: Redirect port -> redirectPort
	// USE -I (INSERT) to ensure this runs BEFORE Docker's PREROUTING rules
	err := f.runRule("PREROUTING", "-I", 
		"-p", "udp", 
		"--dport", p, 
		"-j", "REDIRECT", 
		"--to-port", rp)
	if err != nil {
		return err
	}

	// 2. OUTGOING: SNAT redirectPort -> port
	// Ensures replies look like they come from the original port (27055)
	// Also using -I to be safe, though -A often works for POSTROUTING
	err = f.runRule("POSTROUTING", "-I", 
		"-p", "udp", 
		"--sport", rp, 
		"-j", "SNAT", 
		"--to-source", snatTarget)
	
	if err != nil {
		f.logger.Error("Failed to add SNAT rule: %v", err)
		f.RemoveRedirect(port, redirectPort, externalIP) // Cleanup
		return err
	}

	f.logger.Info("Firewall rules active: %s -> %s (SNAT as %s)", p, rp, snatTarget)
	return nil
}

func (f *firewall) RemoveRedirect(port, redirectPort int, externalIP string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	f.logger.Info("Cleaning up firewall rules...")

	p := fmt.Sprintf("%d", port)
	rp := fmt.Sprintf("%d", redirectPort)
	snatTarget := fmt.Sprintf("%s:%d", externalIP, port)

	// Remove rules (Order doesn't strictly matter for deletion)
	f.removeRule("PREROUTING", "-p", "udp", "--dport", p, "-j", "REDIRECT", "--to-port", rp)
	f.removeRule("POSTROUTING", "-p", "udp", "--sport", rp, "-j", "SNAT", "--to-source", snatTarget)

	return nil
}

// runRule executes iptables command. 
// op is usually "-I" (Insert) or "-A" (Append). 
// Checks existence first to avoid duplicates.
func (f *firewall) runRule(chain string, op string, args ...string) error {
	// 1. Check if rule exists using -C
	checkArgs := append([]string{"-t", "nat", "-C", chain}, args...)
	if err := exec.Command("iptables", checkArgs...).Run(); err == nil {
		f.logger.Info("Rule already exists in %s", chain)
		return nil 
	}

	// 2. Add rule
	addArgs := append([]string{"-t", "nat", op, chain}, args...)
	cmd := exec.Command("iptables", addArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables %s failed: %v, output: %s", op, err, string(output))
	}
	return nil
}

func (f *firewall) removeRule(chain string, args ...string) {
	// Use -D to delete
	delArgs := append([]string{"-t", "nat", "-D", chain}, args...)
	exec.Command("iptables", delArgs...).Run()
}
