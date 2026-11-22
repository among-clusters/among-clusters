package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func putExportRoutes(t *testing.T, path string, routes []exportRoute) {
	t.Helper()
	data, _ := json.Marshal(routes)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNewStreamLeaseExpiresWithoutResurrectingSameStaticIdentity(t *testing.T) {
	now := time.Unix(1000, 0)
	path := filepath.Join(t.TempDir(), "exports.json")
	static := map[string]exportRoute{"owned": {ServiceIdentity: "owned", Target: "old-static:80"}, "unrelated": {ServiceIdentity: "unrelated", Target: "reviewed-static:80"}}
	fresh := exportRoute{ServiceIdentity: "owned", Target: "current:8080", LeaseRequired: true, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}
	putExportRoutes(t, path, []exportRoute{fresh})
	cache := &exportRouteCache{}
	if route, ok := cache.lookup(path, static, "owned", now); !ok || route.Target != "current:8080" {
		t.Fatal("fresh lease not selected")
	}
	if _, ok := cache.lookup(path, static, "owned", now.Add(time.Minute)); ok {
		t.Fatal("expired lease resurrected static alias")
	}
	if _, ok := cache.lookup(path, static, "unrelated", now.Add(time.Hour)); !ok {
		t.Fatal("unrelated explicit static route disabled")
	}
	putExportRoutes(t, path, []exportRoute{})
	if _, ok := cache.lookup(path, static, "owned", now); ok {
		t.Fatal("removed dynamic identity resurrected static alias")
	}
	putExportRoutes(t, path, []exportRoute{{ServiceIdentity: "owned", LeaseRequired: true, Revoked: true}})
	if _, ok := (&exportRouteCache{}).lookup(path, static, "owned", now); ok {
		t.Fatal("persisted tombstone lost across restart")
	}
}

func TestFileFailureCannotExtendLeaseOrDowngradeKnownOwnership(t *testing.T) {
	now := time.Unix(1000, 0)
	path := filepath.Join(t.TempDir(), "exports.json")
	cache := &exportRouteCache{}
	fresh := exportRoute{ServiceIdentity: "owned", Target: "current:8080", LeaseRequired: true, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}
	putExportRoutes(t, path, []exportRoute{fresh})
	cache.lookup(path, nil, "owned", now)
	_ = os.Remove(path)
	if _, ok := cache.lookup(path, nil, "owned", now.Add(time.Second)); !ok {
		t.Fatal("fresh accepted lease unnecessarily lost on file outage")
	}
	if _, ok := cache.lookup(path, nil, "owned", now.Add(time.Minute)); ok {
		t.Fatal("file outage extended expired lease")
	}
	putExportRoutes(t, path, []exportRoute{{ServiceIdentity: "owned", Target: "unlimited:80"}})
	if _, ok := cache.lookup(path, nil, "owned", now); ok {
		t.Fatal("leased ownership downgraded to unlimited legacy")
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy.json")
	putExportRoutes(t, legacyPath, []exportRoute{{ServiceIdentity: "legacy", Target: "legacy:80"}})
	legacy := &exportRouteCache{}
	legacy.lookup(legacyPath, nil, "legacy", now)
	_ = os.Remove(legacyPath)
	if _, ok := legacy.lookup(legacyPath, map[string]exportRoute{"legacy": {Target: "static:80"}}, "legacy", now); ok {
		t.Fatal("missing legacy dynamic file resurrected static alias")
	}
}

func TestHandleExportRefusesExpiredLeaseBeforeOpeningUpstream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := filepath.Join(t.TempDir(), "exports.json")
	t.Setenv("EXPORTS_FILE", path)
	identity := "spiffe://provider.example/ns/apps/service/canary"
	putExportRoutes(t, path, []exportRoute{{ServiceIdentity: identity, Target: listener.Addr().String(), TargetPeers: []string{"peer"}, LeaseRequired: true, ExpiresAt: time.Now().Add(-time.Second).Format(time.RFC3339Nano)}})
	client, server := net.Pipe()
	defer client.Close()
	go handleExport(server, map[string]exportRoute{identity: {ServiceIdentity: identity, Target: listener.Addr().String(), TargetPeers: []string{"peer"}}}, map[string][]string{"peer": {"spiffe://consumer.example/cluster/consumer"}}, "spiffe://consumer.example/cluster/consumer")
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_ = binary.Write(client, binary.BigEndian, uint16(len(identity)))
	_, _ = client.Write([]byte(identity))
	if _, err = io.ReadAll(client); err != nil {
		t.Fatalf("expired export did not close stream: %v", err)
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Millisecond))
	if upstream, err := listener.Accept(); err == nil {
		upstream.Close()
		t.Fatal("expired lease opened upstream")
	}
}

func TestOtherTrustedPeerCannotUseNarrowedDynamicLeaseOrStaticWildcard(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := filepath.Join(t.TempDir(), "exports.json")
	t.Setenv("EXPORTS_FILE", path)
	identity := "spiffe://provider.example/ns/apps/service/canary"
	putExportRoutes(t, path, []exportRoute{{ServiceIdentity: identity, Target: listener.Addr().String(), TargetPeers: []string{"ready"}, LeaseRequired: true, ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}})
	client, server := net.Pipe()
	defer client.Close()
	trusted := map[string][]string{"ready": {"spiffe://ready.example/cluster/ready"}, "other": {"spiffe://other.example/cluster/other"}}
	go handleExport(server, map[string]exportRoute{identity: {ServiceIdentity: identity, Target: listener.Addr().String(), TargetPeers: []string{"*"}}}, trusted, "spiffe://other.example/cluster/other")
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_ = binary.Write(client, binary.BigEndian, uint16(len(identity)))
	_, _ = client.Write([]byte(identity))
	if _, err = io.ReadAll(client); err != nil {
		t.Fatalf("denied peer stream was not closed: %v", err)
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Millisecond))
	if upstream, err := listener.Accept(); err == nil {
		upstream.Close()
		t.Fatal("unapproved trusted peer reached upstream")
	}
}
