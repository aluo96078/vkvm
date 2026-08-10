//go:build windows

package network

import (
	"net"
	"os/exec"
	"regexp"
)

var windowsGatewayRE = regexp.MustCompile(`(?m)^\s*0\.0\.0\.0\s+0\.0\.0\.0\s+([0-9.]+)\s+`)

func currentDefaultGateways() []string {
	out, err := exec.Command("route", "print", "-4", "0.0.0.0").Output()
	if err != nil {
		return nil
	}
	match := windowsGatewayRE.FindSubmatch(out)
	if len(match) == 2 {
		if ip := net.ParseIP(string(match[1])); ip != nil {
			return []string{ip.String()}
		}
	}
	return nil
}
