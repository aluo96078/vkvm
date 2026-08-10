package network

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMDNSAnnouncementRoundTrip(t *testing.T) {
	packet := buildMDNSAnnouncement(MDNSServiceInfo{
		DeviceID: "agent-1", Role: "agent", Port: 18080,
		IP: net.ParseIP("192.0.2.10"), ProtocolVersion: 2, Capabilities: 9,
	})
	records := parseMDNSRecords(packet)
	peers := make(map[string]*mdnsPeer)
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
	hosts := mdnsPeersToHosts(peers)
	if len(hosts) != 1 || hosts[0].IP != "192.0.2.10" || hosts[0].Port != 18080 || hosts[0].DeviceID != "agent-1" || hosts[0].Role != "agent" || hosts[0].ProtocolVersion != 2 || hosts[0].Capabilities != 9 {
		t.Fatalf("mDNS hosts = %+v", hosts)
	}
}

func TestMDNSAnnouncementSupportsIPv6AndPublicKey(t *testing.T) {
	packet := buildMDNSAnnouncement(MDNSServiceInfo{
		DeviceID: "agent-v6", PublicKey: "ed25519-public", Role: "agent", Port: 18080,
		IP: net.ParseIP("fd00::10"), ProtocolVersion: 2,
	})
	records := parseMDNSRecords(packet)
	peers := make(map[string]*mdnsPeer)
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
	hosts := mdnsPeersToHosts(peers)
	if len(hosts) != 1 || hosts[0].IP != "fd00::10" || hosts[0].PublicKey != "ed25519-public" {
		t.Fatalf("IPv6 mDNS hosts = %+v", hosts)
	}
}

func TestMergeMDNSPeersCombinesInterfacesAndAddresses(t *testing.T) {
	dst := map[string]*mdnsPeer{}
	mergeMDNSPeers(dst, map[string]*mdnsPeer{
		"host._vkvm._tcp.local": {instance: "host._vkvm._tcp.local", target: "host.local", port: 18080, txt: map[string]string{"device_id": "host"}, ips: []net.IP{net.ParseIP("192.0.2.10")}},
	})
	mergeMDNSPeers(dst, map[string]*mdnsPeer{
		"host._vkvm._tcp.local": {instance: "host._vkvm._tcp.local", target: "host.local", port: 18080, txt: map[string]string{"role": "host"}, ips: []net.IP{net.ParseIP("fd00::10")}},
	})
	hosts := mdnsPeersToHosts(dst)
	if len(hosts) != 2 {
		t.Fatalf("merged hosts=%+v", hosts)
	}
	for _, host := range hosts {
		if host.DeviceID != "host" || host.Role != "host" || host.Port != 18080 {
			t.Fatalf("merged metadata=%+v", host)
		}
	}
}

func TestMDNSAnnouncementPublishesDualStackAddresses(t *testing.T) {
	packet := buildMDNSAnnouncement(MDNSServiceInfo{
		DeviceID: "dual", Port: 18080, IPs: []net.IP{net.ParseIP("192.0.2.20"), net.ParseIP("fd00::20")},
	})
	records := parseMDNSRecords(packet)
	peers := make(map[string]*mdnsPeer)
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
	hosts := mdnsPeersToHosts(peers)
	if len(hosts) != 2 {
		t.Fatalf("dual-stack mDNS hosts = %+v", hosts)
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		seen[host.IP] = true
	}
	if !seen["192.0.2.20"] || !seen["fd00::20"] {
		t.Fatalf("dual-stack addresses missing: %+v", hosts)
	}
}

func TestDNSNameCompressionParser(t *testing.T) {
	message := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	message = append(message, encodeDNSName("_vkvm._tcp.local")...)
	name, _, err := readDNSName(message, 12)
	if err != nil || name != "_vkvm._tcp.local" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestMDNSServiceQueryDetection(t *testing.T) {
	query := buildMDNSQuery(VKVMMDNSService)
	if !isMDNSServiceQuery(query) {
		t.Fatal("service query was not detected")
	}
	announcement := buildMDNSAnnouncement(MDNSServiceInfo{DeviceID: "host", Port: 18080})
	if isMDNSServiceQuery(announcement) {
		t.Fatal("announcement was incorrectly treated as a query")
	}
}

func TestMDNSAdvertiserCloseStopsRunLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	advertiser, err := StartMDNSAdvertiser(ctx, MDNSServiceInfo{DeviceID: "close-test", Port: 18080, IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") || strings.Contains(strings.ToLower(err.Error()), "address already in use") {
			t.Skipf("mDNS multicast unavailable in test environment: %v", err)
		}
		t.Fatal(err)
	}
	defer cancel()
	if err := advertiser.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-advertiser.done:
	case <-time.After(time.Second):
		t.Fatal("advertiser run loop did not stop after Close")
	}
}
