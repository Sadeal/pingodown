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

func (f *firewall) AddRedirect(port, redirectPort int, externalIP string) error {
	if os.Geteuid() != 0 {
		f.logger.Error("Port redirection requires root privileges")
		return fmt.Errorf("port redirection requires root privileges, run with 'sudo'")
	}

	p := fmt.Sprintf("%d", port)
	rp := fmt.Sprintf("%d", redirectPort)
	snatTarget := fmt.Sprintf("%s:%d", externalIP, port)

	// CRITICAL: Insert BEFORE Docker's PREROUTING rule
	// Check which interface Docker uses (pterodactyl0 in your case)
	f.logger.Info("Adding PREROUTING redirect: %s -> %s", p, rp)
	
	// Remove old rules first (cleanup)
	f.removeAllRedirectRules(port, redirectPort, externalIP)

	// Add PREROUTING rule with position 1 (BEFORE Docker)
	err := f.insertRule(1, "PREROUTING",
		"-p", "udp",
		"-d", externalIP, // Only for packets to external IP
		"--dport", p,
		"-j", "REDIRECT",
		"--to-port", rp)
	if err != nil {
		return fmt.Errorf("failed to add PREROUTING rule: %w", err)
	}

	// Add POSTROUTING SNAT to rewrite source port
	f.logger.Info("Adding POSTROUTING SNAT: sport %s -> %s", rp, snatTarget)
	err = f.insertRule(1, "POSTROUTING",
		"-p", "udp",
		"-s", externalIP, // Only rewrite packets from external IP
		"--sport", rp,
		"-j", "SNAT",
		"--to-source", snatTarget)
	
	if err != nil {
		f.logger.Error("Failed to add SNAT rule: %v", err)
		f.RemoveRedirect(port, redirectPort, externalIP)
		return err
	}

	f.logger.Info("Firewall configured successfully")
	return nil
}

func (f *firewall) RemoveRedirect(port, redirectPort int, externalIP string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	f.logger.Info("Removing firewall rules...")
	f.removeAllRedirectRules(port, redirectPort, externalIP)
	return nil
}

// insertRule inserts rule at specific position
func (f *firewall) insertRule(position int, chain string, args ...string) error {
	// Build command: iptables -t nat -I CHAIN POSITION args...
	cmdArgs := []string{"-t", "nat", "-I", chain, fmt.Sprintf("%d", position)}
	cmdArgs = append(cmdArgs, args...)
	
	cmd := exec.Command("iptables", cmdArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables insert failed: %v, output: %s", err, string(output))
	}
	return nil
}

// removeAllRedirectRules removes all matching rules (in case of duplicates)
func (f *firewall) removeAllRedirectRules(port, redirectPort int, externalIP string) {
	p := fmt.Sprintf("%d", port)
	rp := fmt.Sprintf("%d", redirectPort)
	snatTarget := fmt.Sprintf("%s:%d", externalIP, port)

	// Remove PREROUTING redirect
	for i := 0; i < 10; i++ { // Try up to 10 times in case of duplicates
		delArgs := []string{"-t", "nat", "-D", "PREROUTING",
			"-p", "udp",
			"-d", externalIP,
			"--dport", p,
			"-j", "REDIRECT",
			"--to-port", rp}
		if exec.Command("iptables", delArgs...).Run() != nil {
			break // Rule doesn't exist, stop trying
		}
	}

	// Remove POSTROUTING SNAT
	for i := 0; i < 10; i++ {
		delArgs := []string{"-t", "nat", "-D", "POSTROUTING",
			"-p", "udp",
			"-s", externalIP,
			"--sport", rp,
			"-j", "SNAT",
			"--to-source", snatTarget}
		if exec.Command("iptables", delArgs...).Run() != nil {
			break
		}
	}
}
