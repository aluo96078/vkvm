// Package network provides network discovery and utilities.
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

// DiscoveredHost represents a VKVM instance found on the network
type DiscoveredHost struct {
	IP              string   `json:"ip"`
	Port            int      `json:"port"`
	DeviceID        string   `json:"device_id,omitempty"`
	PublicKey       string   `json:"public_key,omitempty"`
	Role            string   `json:"role,omitempty"`
	ProtocolVersion uint16   `json:"protocol_version,omitempty"`
	Capabilities    uint32   `json:"capabilities,omitempty"`
	CurrentProfile  string   `json:"current_profile"`
	Profiles        []string `json:"profiles"`
}

// GetLocalIP returns the primary local IP address
func GetLocalIP() (string, error) {
	ips, err := GetLocalIPs()
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no active non-loopback IPv4 address")
	}
	return ips[0], nil
}

// ScanLAN scans the local network for VKVM instances. It is retained as a
// compatibility fallback for networks without multicast DNS; new callers
// should prefer DiscoverVKVM.
func ScanLAN(port int) ([]DiscoveredHost, error) {
	return ScanLANContext(context.Background(), port)
}

// ScanLANContext is the bounded, cancellable legacy IPv4 discovery path.
// Callers should provide a deadline because a large private subnet can still
// contain thousands of addresses even though probes are concurrency limited.
func ScanLANContext(ctx context.Context, port int) ([]DiscoveredHost, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	networks, localIPs, err := localIPv4Networks()
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate local networks: %w", err)
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("no scannable local IPv4 network")
	}

	var hosts []DiscoveredHost
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	seen := make(map[string]struct{})

	for _, network := range networks {
		for ip := firstHost(network); ip != nil; ip = nextHost(ip, network) {
			select {
			case <-ctx.Done():
				wg.Wait()
				return hosts, ctx.Err()
			default:
			}
			candidate := ip.String()
			if _, local := localIPs[candidate]; local {
				continue
			}
			if _, already := seen[candidate]; already {
				continue
			}
			seen[candidate] = struct{}{}
			candidateCopy := candidate
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				if host, ok := probeHostContext(ctx, candidateCopy, port); ok {
					mu.Lock()
					hosts = append(hosts, host)
					mu.Unlock()
				}
			}()
		}
	}

	wg.Wait()
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].IP < hosts[j].IP })
	return hosts, nil
}

// DiscoverVKVM uses DNS-SD/mDNS first. If multicast DNS is unavailable on the
// active network, it performs one cancellable legacy scan with a short bound so
// the caller never gets an unbounded subnet walk. An empty mDNS result is a
// valid result and deliberately does not trigger a scan.
func DiscoverVKVM(ctx context.Context, port int) ([]DiscoveredHost, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	hosts, err := DiscoverMDNS(ctx, 750*time.Millisecond)
	if err == nil {
		return hosts, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	fallbackCtx, cancel := context.WithTimeout(ctx, 1250*time.Millisecond)
	defer cancel()
	hosts, fallbackErr := ScanLANContext(fallbackCtx, port)
	if fallbackErr != nil {
		return nil, fmt.Errorf("mDNS discovery failed: %v; legacy discovery failed: %w", err, fallbackErr)
	}
	return hosts, nil
}

func localIPv4Networks() ([]*net.IPNet, map[string]struct{}, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	networks := make([]*net.IPNet, 0)
	localIPs := make(map[string]struct{})
	seenNetworks := make(map[string]struct{})
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			var network *net.IPNet
			switch value := address.(type) {
			case *net.IPNet:
				ip, network = value.IP, value
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil {
				continue
			}
			ipv4 := ip.To4()
			if ipv4 == nil || network == nil {
				continue
			}
			localIPs[ipv4.String()] = struct{}{}
			mask, bits := network.Mask.Size()
			if bits != 32 || mask < 20 {
				// Avoid unbounded broadcast scans. Larger/VPN networks should use
				// mDNS or an explicit peer endpoint instead.
				continue
			}
			key := network.String()
			if _, exists := seenNetworks[key]; exists {
				continue
			}
			seenNetworks[key] = struct{}{}
			networks = append(networks, &net.IPNet{IP: ipv4.Mask(network.Mask), Mask: network.Mask})
		}
	}
	return networks, localIPs, nil
}

func firstHost(network *net.IPNet) net.IP {
	one, bits := network.Mask.Size()
	if bits != 32 || one >= 32 {
		return nil
	}
	start := binaryIPv4(network.IP)
	if start == 0xffffffff {
		return nil
	}
	start++
	return ipv4FromUint32(start)
}

func nextHost(ip net.IP, network *net.IPNet) net.IP {
	current := binaryIPv4(ip)
	end := binaryIPv4(network.IP) | ^binaryIPv4(net.IP(network.Mask))
	if current >= end-1 {
		return nil
	}
	return ipv4FromUint32(current + 1)
}

func binaryIPv4(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func ipv4FromUint32(value uint32) net.IP {
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}

// probeHost checks if a host is running VKVM API
func probeHost(ip string, port int) (DiscoveredHost, bool) {
	return probeHostContext(context.Background(), ip, port)
}

func probeHostContext(parent context.Context, ip string, port int) (DiscoveredHost, bool) {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
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
