//go:build darwin

package network

import (
	"bytes"
	"net"
	"os/exec"
	"regexp"
)

var gatewayRE = regexp.MustCompile(`gateway:\s+([^\s]+)`)

func currentDefaultGateways() []string {
	// route(8) is a read-only system utility and accepts no user input. This is
	// the portable fallback when Network.framework notifications are not
	// available to the embedded process.
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return nil
	}
	match := gatewayRE.FindSubmatch(bytes.TrimSpace(out))
	if len(match) != 2 {
		return nil
	}
	if ip := net.ParseIP(string(match[1])); ip != nil {
		return []string{ip.String()}
	}
	return nil
}
