// Package network provides network discovery and utilities.
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DiscoveredHost represents a VKVM instance found on the network
type DiscoveredHost struct {
	IP             string   `json:"ip"`
	Port           int      `json:"port"`
	CurrentProfile string   `json:"current_profile"`
	Profiles       []string `json:"profiles"`
}

// GetLocalIP returns the primary local IP address
func GetLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

// ScanLAN scans the local network for VKVM instances
// Returns discovered hosts on the same subnet
func ScanLAN(port int) ([]DiscoveredHost, error) {
	localIP, err := GetLocalIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get local IP: %w", err)
	}

	// Parse the local IP to get the subnet
	parts := strings.Split(localIP, ".")
	if len(parts) != 4 {
		return nil, fmt.Errorf("invalid IP address format: %s", localIP)
	}

	subnet := fmt.Sprintf("%s.%s.%s", parts[0], parts[1], parts[2])

	var hosts []DiscoveredHost
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Scan IPs 1-254 in the subnet
	for i := 1; i <= 254; i++ {
		wg.Add(1)
		go func(hostNum int) {
			defer wg.Done()

			ip := fmt.Sprintf("%s.%d", subnet, hostNum)

			// Skip our own IP
			if ip == localIP {
				return
			}

			// Try to connect to the VKVM API
			if host, ok := probeHost(ip, port); ok {
				mu.Lock()
				hosts = append(hosts, host)
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	return hosts, nil
}

// probeHost checks if a host is running VKVM API
func probeHost(ip string, port int) (DiscoveredHost, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// First check health endpoint
	healthURL := fmt.Sprintf("http://%s:%d/health", ip, port)
	req, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
	if err != nil {
		return DiscoveredHost{}, false
	}

	client := &http.Client{
		Timeout: 500 * time.Millisecond,
	}

	resp, err := client.Do(req)
	if err != nil {
		return DiscoveredHost{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DiscoveredHost{}, false
	}

	// Try to get status
	statusURL := fmt.Sprintf("http://%s:%d/api/status", ip, port)
	req, err = http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		return DiscoveredHost{IP: ip, Port: port}, true
	}

	resp, err = client.Do(req)
	if err != nil {
		return DiscoveredHost{IP: ip, Port: port}, true
	}
	defer resp.Body.Close()

	var status struct {
		CurrentProfile string   `json:"current_profile"`
		Profiles       []string `json:"profiles"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&status); err == nil {
		return DiscoveredHost{
			IP:             ip,
			Port:           port,
			CurrentProfile: status.CurrentProfile,
			Profiles:       status.Profiles,
		}, true
	}

	return DiscoveredHost{IP: ip, Port: port}, true
}

// GetLocalIPs returns all available local IPv4 addresses
func GetLocalIPs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}
			ips = append(ips, ip.String())
		}
	}
	return ips, nil
}
