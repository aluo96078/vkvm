package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const VKVMMDNSService = "_vkvm._tcp.local"
const mdnsMulticastIPv4 = "224.0.0.251:5353"

type MDNSServiceInfo struct {
	DeviceID  string
	PublicKey string
	Role      string
	Port      int
	IP        net.IP
	// IPs allows an advertiser to publish all active addresses (for example
	// both an IPv4 and an IPv6 address). IP is retained for callers that only
	// have one address.
	IPs             []net.IP
	ProtocolVersion uint16
	Capabilities    uint32
}

type mdnsEndpoint struct {
	conn  *net.UDPConn
	group *net.UDPAddr
}

type MDNSAdvertiser struct {
	endpoints []mdnsEndpoint
	info      MDNSServiceInfo
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// StartMDNSAdvertiser publishes the VKVM service and responds to DNS-SD
// queries. It is best-effort because an OS mDNS daemon may already own port
// 5353; callers should keep the explicit endpoint as a fallback.
func StartMDNSAdvertiser(ctx context.Context, info MDNSServiceInfo) (*MDNSAdvertiser, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info.DeviceID == "" || info.Port <= 0 {
		return nil, fmt.Errorf("mDNS service requires device ID and port")
	}
	if info.IP == nil {
		info.IP = net.ParseIP("127.0.0.1")
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353})
	if err != nil {
		return nil, err
	}
	advertiserCtx, cancel := context.WithCancel(ctx)
	a := &MDNSAdvertiser{endpoints: []mdnsEndpoint{{conn: conn, group: &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}}}, info: info, cancel: cancel, done: make(chan struct{})}
	// IPv6 multicast may be unavailable on a VPN-only or sandboxed host; IPv4
	// remains a valid advertiser in that case.
	if ipv6, ipv6Err := net.ListenMulticastUDP("udp6", nil, &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353}); ipv6Err == nil {
		// IPv6 link-local multicast requires an interface zone. Publish on each
		// active interface instead of sending an unscoped ff02::fb datagram.
		published := false
		ifaces, _ := net.Interfaces()
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			hasIPv6 := false
			for _, addr := range addrs {
				ip, _, err := net.ParseCIDR(addr.String())
				if err == nil && ip.To4() == nil && ip.To16() != nil {
					hasIPv6 = true
					break
				}
			}
			if hasIPv6 {
				a.endpoints = append(a.endpoints, mdnsEndpoint{conn: ipv6, group: &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353, Zone: iface.Name}})
				published = true
			}
		}
		if !published {
			// Keep a usable endpoint for environments that expose IPv6 multicast
			// but do not report interface addresses (some test sandboxes).
			a.endpoints = append(a.endpoints, mdnsEndpoint{conn: ipv6, group: &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353}})
		}
	}
	go a.run(advertiserCtx)
	return a, nil
}

func (a *MDNSAdvertiser) run(ctx context.Context) {
	defer close(a.done)
	announcement := buildMDNSAnnouncement(a.info)
	a.announce(announcement)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.Close()
			return
		case <-ticker.C:
			a.announce(announcement)
		}
	}
}

func (a *MDNSAdvertiser) announce(announcement []byte) {
	if a == nil {
		return
	}
	// A single IPv6 socket may be represented by multiple endpoints (one per
	// interface zone). Send on every endpoint, but create only one reader per
	// underlying socket; concurrent ReadFromUDP calls on the same socket race
	// query ownership and can drop responses.
	readers := make(map[*net.UDPConn]struct{}, len(a.endpoints))
	for _, endpoint := range a.endpoints {
		conn, group := endpoint.conn, endpoint.group
		_, _ = conn.WriteToUDP(announcement, group)
		if _, ok := readers[conn]; ok {
			continue
		}
		readers[conn] = struct{}{}
		go func(conn *net.UDPConn) {
			buf := make([]byte, 2048)
			_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			if n, remote, err := conn.ReadFromUDP(buf); err == nil && isMDNSServiceQuery(buf[:n]) {
				_, _ = conn.WriteToUDP(announcement, remote)
			}
		}(conn)
	}
}

func (a *MDNSAdvertiser) Close() error {
	if a == nil {
		return nil
	}
	var firstErr error
	a.closeOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		closed := make(map[*net.UDPConn]struct{})
		for _, endpoint := range a.endpoints {
			if _, ok := closed[endpoint.conn]; ok {
				continue
			}
			closed[endpoint.conn] = struct{}{}
			if err := endpoint.conn.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	})
	return firstErr
}

func isMDNSServiceQuery(data []byte) bool {
	if len(data) < 12 || binary.BigEndian.Uint16(data[2:4])&0x8000 != 0 {
		return false
	}
	questions := int(binary.BigEndian.Uint16(data[4:6]))
	off := 12
	for i := 0; i < questions; i++ {
		name, next, err := readDNSName(data, off)
		if err != nil || next+4 > len(data) {
			return false
		}
		if strings.EqualFold(strings.TrimSuffix(name, "."), VKVMMDNSService) &&
			(binary.BigEndian.Uint16(data[next:next+2]) == 12 || binary.BigEndian.Uint16(data[next:next+2]) == 255) {
			return true
		}
		off = next + 4
	}
	return false
}

func buildMDNSAnnouncement(info MDNSServiceInfo) []byte {
	instance := strings.ToLower(info.DeviceID) + "." + VKVMMDNSService
	target := strings.ToLower(info.DeviceID) + ".local"
	ips := serviceIPs(info)
	header := make([]byte, 12)
	binary.BigEndian.PutUint16(header[2:4], 0x8400)               // response + authoritative
	binary.BigEndian.PutUint16(header[6:8], 1)                    // PTR answer
	binary.BigEndian.PutUint16(header[10:12], uint16(2+len(ips))) // SRV, TXT, A/AAAA
	packet := append(header, encodeDNSName(VKVMMDNSService)...)
	packet = append(packet, dnsRRHeader(12, 120, len(encodeDNSName(instance)))...)
	packet = append(packet, encodeDNSName(instance)...)
	packet = append(packet, encodeDNSName(instance)...)
	srv := make([]byte, 6)
	binary.BigEndian.PutUint16(srv[4:6], uint16(info.Port))
	srv = append(srv, encodeDNSName(target)...)
	packet = append(packet, dnsRRHeader(33, 120, len(srv))...)
	packet = append(packet, srv...)
	txtFields := []string{"device_id=" + info.DeviceID, "public_key=" + info.PublicKey, "role=" + info.Role, fmt.Sprintf("protocol_version=%d", info.ProtocolVersion), fmt.Sprintf("capabilities=%d", info.Capabilities)}
	var txt []byte
	for _, field := range txtFields {
		if len(field) > 255 {
			continue
		}
		txt = append(txt, byte(len(field)))
		txt = append(txt, field...)
	}
	packet = append(packet, encodeDNSName(instance)...)
	packet = append(packet, dnsRRHeader(16, 120, len(txt))...)
	packet = append(packet, txt...)
	for _, address := range ips {
		packet = append(packet, encodeDNSName(target)...)
		if ip := address.To4(); ip != nil {
			packet = append(packet, dnsRRHeader(1, 120, net.IPv4len)...)
			packet = append(packet, ip...)
		} else if ip := address.To16(); ip != nil {
			packet = append(packet, dnsRRHeader(28, 120, net.IPv6len)...)
			packet = append(packet, ip...)
		}
	}
	return packet
}

func serviceIPs(info MDNSServiceInfo) []net.IP {
	seen := make(map[string]struct{})
	out := make([]net.IP, 0, len(info.IPs)+1)
	add := func(ip net.IP) {
		if ip == nil {
			return
		}
		ip = append(net.IP(nil), ip...)
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		} else if ip16 := ip.To16(); ip16 != nil {
			ip = ip16
		} else {
			return
		}
		key := ip.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, ip)
	}
	add(info.IP)
	for _, ip := range info.IPs {
		add(ip)
	}
	if len(out) == 0 {
		out = append(out, net.IPv4(127, 0, 0, 1))
	}
	return out
}

func dnsRRHeader(typeCode uint16, ttl uint32, length int) []byte {
	h := make([]byte, 10)
	binary.BigEndian.PutUint16(h[0:2], typeCode)
	binary.BigEndian.PutUint16(h[2:4], 1)
	binary.BigEndian.PutUint32(h[4:8], ttl)
	binary.BigEndian.PutUint16(h[8:10], uint16(length))
	return h
}

type mdnsRecord struct {
	name        string
	typeCode    uint16
	data        []byte
	message     []byte
	rdataOffset int
}

type mdnsPeer struct {
	instance string
	target   string
	port     int
	txt      map[string]string
	ips      []net.IP
}

// DiscoverMDNS queries the local multicast DNS service and returns only
// complete endpoint records. It is bounded by timeout and never scans a LAN.
func DiscoverMDNS(ctx context.Context, timeout time.Duration) ([]DiscoveredHost, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 750 * time.Millisecond
	}
	peers := make(map[string]*mdnsPeer)
	var queryErrors []error
	ifaces, _ := net.Interfaces()
	active := make([]*net.Interface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		copy := iface
		active = append(active, &copy)
	}

	// Query each active interface independently so an mDNS responder on a VPN,
	// Wi-Fi and wired LAN can all be discovered. Each query has the same
	// caller-owned deadline and contributes records to a single merge map.
	type result struct {
		peers map[string]*mdnsPeer
		err   error
	}
	queries := make([]struct {
		network string
		group   *net.UDPAddr
		iface   *net.Interface
	}, 0, len(active)*2)
	for _, iface := range active {
		queries = append(queries,
			struct {
				network string
				group   *net.UDPAddr
				iface   *net.Interface
			}{network: "udp4", group: &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}, iface: iface},
			struct {
				network string
				group   *net.UDPAddr
				iface   *net.Interface
			}{network: "udp6", group: &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353, Zone: iface.Name}, iface: iface},
		)
	}
	if len(queries) == 0 {
		queries = append(queries,
			struct {
				network string
				group   *net.UDPAddr
				iface   *net.Interface
			}{network: "udp4", group: &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}},
		)
	}
	results := make(chan result, len(queries))
	for _, query := range queries {
		query := query
		go func() {
			local := make(map[string]*mdnsPeer)
			err := queryMDNSFamily(ctx, timeout, query.network, query.group, query.iface, local)
			results <- result{peers: local, err: err}
		}()
	}
	for range queries {
		res := <-results
		if res.err != nil {
			queryErrors = append(queryErrors, res.err)
		}
		mergeMDNSPeers(peers, res.peers)
	}
	if len(peers) == 0 && len(queryErrors) > 0 {
		return nil, queryErrors[0]
	}
	return mdnsPeersToHosts(peers), nil
}

func queryMDNSFamily(ctx context.Context, timeout time.Duration, networkName string, group *net.UDPAddr, iface *net.Interface, peers map[string]*mdnsPeer) error {
	var conn *net.UDPConn
	var err error
	if iface != nil {
		// ListenMulticastUDP joins the group on the requested interface. A
		// separate socket is intentional: it keeps interface-specific IPv6
		// zones and VPN routing isolated while the caller merges records.
		conn, err = net.ListenMulticastUDP(networkName, iface, group)
	} else {
		bindIP := net.IPv4zero
		if networkName == "udp6" {
			bindIP = net.IPv6zero
		}
		conn, err = net.ListenUDP(networkName, &net.UDPAddr{IP: bindIP, Port: 0})
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.WriteToUDP(buildMDNSQuery(VKVMMDNSService), group); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if timer, ok := ctx.Deadline(); ok && timer.Before(deadline) {
		deadline = timer
	}
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 65535)
	for {
		n, _, readErr := conn.ReadFromUDP(buf)
		if readErr != nil {
			if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readErr
		}
		records := parseMDNSRecords(buf[:n])
		for _, record := range records {
			if record.typeCode != 1 && record.typeCode != 28 {
				applyMDNSRecord(peers, record)
			}
		}
		for _, record := range records {
			if record.typeCode == 1 || record.typeCode == 28 {
				applyMDNSRecord(peers, record)
			}
		}
	}
}

func mergeMDNSPeers(dst, src map[string]*mdnsPeer) {
	for key, incoming := range src {
		current := dst[key]
		if current == nil {
			current = &mdnsPeer{instance: incoming.instance, target: incoming.target, port: incoming.port, txt: make(map[string]string)}
			dst[key] = current
		}
		if current.target == "" {
			current.target = incoming.target
		}
		if current.port == 0 {
			current.port = incoming.port
		}
		for k, v := range incoming.txt {
			current.txt[k] = v
		}
		seen := make(map[string]struct{}, len(current.ips))
		for _, ip := range current.ips {
			seen[ip.String()] = struct{}{}
		}
		for _, ip := range incoming.ips {
			if _, ok := seen[ip.String()]; ok {
				continue
			}
			current.ips = append(current.ips, append(net.IP(nil), ip...))
			seen[ip.String()] = struct{}{}
		}
	}
}

func buildMDNSQuery(name string) []byte {
	buf := make([]byte, 12)
	binary.BigEndian.PutUint16(buf[4:6], 1)
	buf = append(buf, encodeDNSName(name)...)
	question := make([]byte, 4)
	binary.BigEndian.PutUint16(question[0:2], 12) // PTR
	binary.BigEndian.PutUint16(question[2:4], 1)  // IN
	return append(buf, question...)
}

func encodeDNSName(name string) []byte {
	name = strings.TrimSuffix(name, ".")
	var out []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 {
			return nil
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0)
}

func parseMDNSRecords(msg []byte) []mdnsRecord {
	if len(msg) < 12 {
		return nil
	}
	count := int(binary.BigEndian.Uint16(msg[4:6])) + int(binary.BigEndian.Uint16(msg[6:8])) + int(binary.BigEndian.Uint16(msg[8:10])) + int(binary.BigEndian.Uint16(msg[10:12]))
	off := 12
	for i := 0; i < int(binary.BigEndian.Uint16(msg[4:6])); i++ {
		var err error
		_, off, err = readDNSName(msg, off)
		if err != nil || off+4 > len(msg) {
			return nil
		}
		off += 4
	}
	records := make([]mdnsRecord, 0, count)
	for i := 0; i < count-int(binary.BigEndian.Uint16(msg[4:6])); i++ {
		name, next, err := readDNSName(msg, off)
		if err != nil || next+10 > len(msg) {
			break
		}
		typeCode := binary.BigEndian.Uint16(msg[next : next+2])
		rdLength := int(binary.BigEndian.Uint16(msg[next+8 : next+10]))
		rdStart := next + 10
		if rdStart+rdLength > len(msg) {
			break
		}
		records = append(records, mdnsRecord{name: strings.ToLower(strings.TrimSuffix(name, ".")), typeCode: typeCode, data: append([]byte(nil), msg[rdStart:rdStart+rdLength]...), message: msg, rdataOffset: rdStart})
		off = rdStart + rdLength
	}
	return records
}

func readDNSName(msg []byte, off int) (string, int, error) {
	if off < 0 || off >= len(msg) {
		return "", off, fmt.Errorf("dns name offset out of range")
	}
	labels := make([]string, 0, 4)
	pos := off
	consumed := -1
	for jumps := 0; jumps < 16; jumps++ {
		if pos >= len(msg) {
			return "", off, fmt.Errorf("dns name truncated")
		}
		length := int(msg[pos])
		if length == 0 {
			if consumed < 0 {
				consumed = pos + 1
			}
			return strings.Join(labels, "."), consumed, nil
		}
		if length&0xc0 == 0xc0 {
			if pos+1 >= len(msg) {
				return "", off, fmt.Errorf("dns pointer truncated")
			}
			pointer := ((length & 0x3f) << 8) | int(msg[pos+1])
			if consumed < 0 {
				consumed = pos + 2
			}
			name, _, err := readDNSName(msg, pointer)
			if err != nil {
				return "", off, err
			}
			if name != "" {
				labels = append(labels, strings.Split(name, ".")...)
			}
			return strings.Join(labels, "."), consumed, nil
		}
		if length > 63 || pos+1+length > len(msg) {
			return "", off, fmt.Errorf("dns label truncated")
		}
		labels = append(labels, string(msg[pos+1:pos+1+length]))
		pos += 1 + length
	}
	return "", off, fmt.Errorf("dns compression loop")
}

func applyMDNSRecord(peers map[string]*mdnsPeer, record mdnsRecord) {
	switch record.typeCode {
	case 12: // PTR service -> instance
		instance, _, err := readDNSName(record.message, record.rdataOffset)
		if err != nil {
			return
		}
		instance = strings.ToLower(strings.TrimSuffix(instance, "."))
		peer := peers[instance]
		if peer == nil {
			peer = &mdnsPeer{instance: instance, txt: make(map[string]string)}
			peers[instance] = peer
		}
	case 33: // SRV
		if len(record.data) < 6 {
			return
		}
		target, _, err := readDNSName(record.message, record.rdataOffset+6)
		if err != nil {
			return
		}
		peer := peers[record.name]
		if peer == nil {
			peer = &mdnsPeer{instance: record.name, txt: make(map[string]string)}
			peers[record.name] = peer
		}
		peer.target = strings.ToLower(strings.TrimSuffix(target, "."))
		peer.port = int(binary.BigEndian.Uint16(record.data[4:6]))
	case 16: // TXT
		peer := peers[record.name]
		if peer == nil {
			peer = &mdnsPeer{instance: record.name, txt: make(map[string]string)}
			peers[record.name] = peer
		}
		for pos := 0; pos < len(record.data); {
			length := int(record.data[pos])
			pos++
			if pos+length > len(record.data) {
				break
			}
			field := string(record.data[pos : pos+length])
			if key, value, ok := strings.Cut(field, "="); ok {
				peer.txt[strings.ToLower(key)] = value
			}
			pos += length
		}
	case 1, 28: // A / AAAA
		for _, peer := range peers {
			if peer.target != record.name {
				continue
			}
			if record.typeCode == 1 && len(record.data) == net.IPv4len {
				peer.ips = append(peer.ips, net.IP(append([]byte(nil), record.data...)))
			} else if record.typeCode == 28 && len(record.data) == net.IPv6len {
				peer.ips = append(peer.ips, net.IP(append([]byte(nil), record.data...)))
			}
		}
	}
}

func mdnsPeersToHosts(peers map[string]*mdnsPeer) []DiscoveredHost {
	result := make([]DiscoveredHost, 0, len(peers))
	for _, peer := range peers {
		if peer.port == 0 || len(peer.ips) == 0 {
			continue
		}
		for _, ip := range peer.ips {
			host := DiscoveredHost{IP: ip.String(), Port: peer.port, DeviceID: peer.txt["device_id"], PublicKey: peer.txt["public_key"], Role: peer.txt["role"]}
			if value := peer.txt["protocol_version"]; value != "" {
				_, _ = fmt.Sscanf(value, "%d", &host.ProtocolVersion)
			}
			if value := peer.txt["capabilities"]; value != "" {
				_, _ = fmt.Sscanf(value, "%d", &host.Capabilities)
			}
			result = append(result, host)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DeviceID != result[j].DeviceID {
			return result[i].DeviceID < result[j].DeviceID
		}
		return result[i].IP < result[j].IP
	})
	return result
}
