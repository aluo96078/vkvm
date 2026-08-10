package network

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InterfaceSnapshot is the network identity relevant to peer discovery.
// Address prefixes are used instead of volatile DHCP lease timestamps.
type InterfaceSnapshot struct {
	Name      string
	Index     int
	Flags     string
	Addresses []string
}

// NetworkSnapshot is a stable, comparable view of active network interfaces.
type NetworkSnapshot struct {
	Interfaces []InterfaceSnapshot
	// Gateways are the default-route next hops, when the platform exposes them.
	// They are part of the fingerprint because DHCP can preserve an interface
	// prefix while moving the machine between isolated networks.
	Gateways []string
}

func (s NetworkSnapshot) Key() string {
	parts := make([]string, 0, len(s.Interfaces))
	for _, iface := range s.Interfaces {
		addresses := append([]string(nil), iface.Addresses...)
		sort.Strings(addresses)
		parts = append(parts, iface.Name+"/"+strconv.Itoa(iface.Index)+"/"+iface.Flags+"/"+strings.Join(addresses, ","))
	}
	sort.Strings(parts)
	gateways := append([]string(nil), s.Gateways...)
	sort.Strings(gateways)
	return strings.Join(parts, "|") + "#gw=" + strings.Join(gateways, ",")
}

// CurrentNetworkSnapshot returns active non-loopback interface addresses. It
// does not probe hosts or scan the LAN, so it remains safe on offline/VPN
// networks and is cheap enough to use as a portable fallback when native OS
// network notifications are unavailable.
func CurrentNetworkSnapshot() (NetworkSnapshot, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return NetworkSnapshot{}, err
	}
	snapshot := NetworkSnapshot{Interfaces: make([]InterfaceSnapshot, 0, len(interfaces)), Gateways: currentDefaultGateways()}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		prefixes := make([]string, 0, len(addresses))
		for _, address := range addresses {
			ip, network, err := net.ParseCIDR(address.String())
			if err != nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			prefixes = append(prefixes, network.String())
		}
		if len(prefixes) == 0 {
			continue
		}
		sort.Strings(prefixes)
		snapshot.Interfaces = append(snapshot.Interfaces, InterfaceSnapshot{
			Name:      iface.Name,
			Index:     iface.Index,
			Flags:     iface.Flags.String(),
			Addresses: prefixes,
		})
	}
	sort.Slice(snapshot.Interfaces, func(i, j int) bool {
		return snapshot.Interfaces[i].Name < snapshot.Interfaces[j].Name
	})
	return snapshot, nil
}

// GetLocalAddresses returns active, non-loopback IPv4 and IPv6 addresses.
// Unlike GetLocalIPs (the legacy IPv4-only helper), this is used for service
// advertisement so a dual-stack peer can receive both A and AAAA records.
func GetLocalAddresses() ([]net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	addresses := make([]net.IP, 0)
	seen := make(map[string]struct{})
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addrs {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ip = ip4
			} else if ip16 := ip.To16(); ip16 != nil {
				ip = ip16
			} else {
				continue
			}
			key := ip.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			addresses = append(addresses, append(net.IP(nil), ip...))
		}
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].String() < addresses[j].String() })
	return addresses, nil
}

// NetworkMonitor emits the initial snapshot and only subsequent fingerprint
// changes. It deliberately does not perform peer discovery itself.
type NetworkMonitor struct {
	interval time.Duration
	debounce time.Duration
}

func NewNetworkMonitor(interval time.Duration) *NetworkMonitor {
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	return &NetworkMonitor{interval: interval, debounce: 2 * time.Second}
}

func (m *NetworkMonitor) Changes(ctx context.Context) <-chan NetworkSnapshot {
	out := make(chan NetworkSnapshot, 1)
	go func() {
		defer close(out)
		last, err := CurrentNetworkSnapshot()
		if err == nil {
			out <- last
		}
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		var pending NetworkSnapshot
		var pendingKey string
		var timer *time.Timer
		var timerC <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := CurrentNetworkSnapshot()
				if err != nil {
					continue
				}
				if current.Key() == last.Key() {
					if timer != nil {
						if !timer.Stop() {
							select {
							case <-timer.C:
							default:
							}
						}
						timerC = nil
					}
					pendingKey = ""
					continue
				}
				key := current.Key()
				pending, pendingKey = current, key
				if timer == nil {
					timer = time.NewTimer(m.debounce)
					timerC = timer.C
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(m.debounce)
				}
			case <-timerC:
				if pendingKey == "" || pendingKey == last.Key() {
					pendingKey = ""
					timerC = nil
					continue
				}
				last = pending
				pendingKey = ""
				timerC = nil
				select {
				case out <- pending:
				default:
					select {
					case <-out:
					default:
					}
					out <- pending
				}
			}
		}
	}()
	return out
}
