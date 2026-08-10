//go:build !darwin && !windows

package network

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

func currentDefaultGateways() []string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return nil
	}
	defer file.Close()
	var result []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] == "Iface" || fields[1] != "00000000" {
			continue
		}
		var value uint32
		if _, err := fmt.Sscanf(fields[2], "%x", &value); err != nil {
			continue
		}
		result = append(result, net.IPv4(byte(value), byte(value>>8), byte(value>>16), byte(value>>24)).String())
	}
	return result
}
