package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func TestSessionRetryBackoffIsBounded(t *testing.T) {
	if got := nextBackoff(3 * time.Second); got != 6*time.Second {
		t.Fatalf("first retry backoff = %s", got)
	}
	if got := nextBackoff(time.Minute); got != time.Minute {
		t.Fatalf("maximum retry backoff = %s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForRetry(ctx, time.Hour) {
		t.Fatal("cancelled retry wait reported success")
	}
}

func TestPeerSessionCandidatesPreserveCompatibilityAndOrder(t *testing.T) {
	legacy := peerSession{Endpoint: "r640:8443"}
	if got := legacy.candidates(); len(got) != 1 || got[0] != "r640:8443" {
		t.Fatalf("legacy candidates = %#v", got)
	}
	regional := peerSession{
		Endpoint:  "ignored-legacy:8443",
		Endpoints: []string{"la1:8443", "la2:8443", "r640:8443"},
	}
	got := regional.candidates()
	if len(got) != 3 || got[0] != "la1:8443" || got[1] != "la2:8443" || got[2] != "r640:8443" {
		t.Fatalf("ordered candidates = %#v", got)
	}
	if got := (peerSession{}).candidates(); got != nil {
		t.Fatalf("empty candidates = %#v", got)
	}
}

func TestSessionQUICConfigSupportsPublicEdgeFanout(t *testing.T) {
	for _, dial := range []bool{false, true} {
		config := sessionQUICConfig(dial)
		if config.MaxIncomingStreams != sessionMaxConcurrentStreams {
			t.Fatalf("dial=%t max incoming streams = %d", dial, config.MaxIncomingStreams)
		}
		if config.KeepAlivePeriod != 15*time.Second {
			t.Fatalf("dial=%t keepalive = %s", dial, config.KeepAlivePeriod)
		}
	}
	if sessionQUICConfig(true).HandshakeIdleTimeout != 5*time.Second {
		t.Fatal("dialer handshake timeout is not bounded")
	}
	if sessionQUICConfig(false).HandshakeIdleTimeout != 0 {
		t.Fatal("listener unexpectedly overrides the handshake timeout")
	}
}

func TestSessionRegistryWaitsForReconnect(t *testing.T) {
	registry := newSessionRegistry()
	connection := new(quic.Conn)
	go func() {
		time.Sleep(20 * time.Millisecond)
		registry.put("spiffe://remote.test/cluster/remote", connection)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := registry.wait(ctx, "spiffe://remote.test/cluster/remote/", time.Second); got != connection {
		t.Fatal("reconnected peer session was not returned")
	}
}

func TestSessionRegistryWaitIsBounded(t *testing.T) {
	registry := newSessionRegistry()
	started := time.Now()
	if got := registry.wait(context.Background(), "spiffe://missing.test/cluster/remote", 20*time.Millisecond); got != nil {
		t.Fatal("missing peer session was returned")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("missing peer wait took %s", elapsed)
	}
}

func TestExportTunnelRoutesByServiceIdentity(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		connection, _ := upstream.Accept()
		if connection != nil {
			defer connection.Close()
			io.Copy(connection, connection)
		}
	}()
	client, server := net.Pipe()
	defer client.Close()
	routes := map[string]exportRoute{"spiffe://remote/ns/default/service/api": {ServiceIdentity: "spiffe://remote/ns/default/service/api", Target: upstream.Addr().String(), TargetPeers: []string{"remote-local"}}}
	identities := map[string][]string{"remote-local": {"spiffe://caller.test/cluster/caller"}}
	go handleExport(server, routes, identities, "spiffe://caller.test/cluster/caller")
	identity := []byte("spiffe://remote/ns/default/service/api")
	if err = binary.Write(client, binary.BigEndian, uint16(len(identity))); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Write(identity); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 4)
	if _, err = io.ReadFull(client, result); err != nil {
		t.Fatal(err)
	}
	if string(result) != "ping" {
		t.Fatalf("got %q", result)
	}
}

func TestExportTunnelPreservesResponseAfterClientHalfClose(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		connection, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		request, _ := io.ReadAll(connection)
		_, _ = connection.Write(append([]byte("reply:"), request...))
	}()

	tunnel, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	routes := map[string]exportRoute{"service": {ServiceIdentity: "service", Target: upstream.Addr().String(), TargetPeers: []string{"peer"}}}
	identities := map[string][]string{"peer": {"spiffe://caller.test/cluster/caller"}}
	go func() {
		connection, acceptErr := tunnel.Accept()
		if acceptErr == nil {
			handleExport(connection, routes, identities, "spiffe://caller.test/cluster/caller")
		}
	}()

	clientConnection, err := net.Dial("tcp", tunnel.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := clientConnection.(*net.TCPConn)
	defer client.Close()
	if err = binary.Write(client, binary.BigEndian, uint16(len("service"))); err != nil {
		t.Fatal(err)
	}
	_, _ = client.Write([]byte("service"))
	_, _ = client.Write([]byte("payload"))
	if err = client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "reply:payload" {
		t.Fatalf("response after half-close = %q", response)
	}
}

func TestExportAcknowledgesSessionHeartbeat(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go handleExport(server, nil, nil, "spiffe://caller.test/cluster/caller")
	identity := []byte(sessionHeartbeatIdentity)
	if err := binary.Write(client, binary.BigEndian, uint16(len(identity))); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(identity); err != nil {
		t.Fatal(err)
	}
	ack := []byte{0}
	if _, err := io.ReadFull(client, ack); err != nil {
		t.Fatal(err)
	}
	if ack[0] != 1 {
		t.Fatalf("unexpected heartbeat acknowledgement %d", ack[0])
	}
}

func TestOnlySessionDialerOwnsHeartbeat(t *testing.T) {
	// An accepted connection shares its stream budget with ordinary imports and
	// only acknowledges the dialer's heartbeat. Running a second heartbeat loop
	// on the acceptor can tear down the entire peer session under fan-in load.
	if sessionAcceptorMaintainsHeartbeat {
		t.Fatal("session acceptor must not maintain a competing heartbeat")
	}
}

func TestSessionRegistryKeepsLongLivedSessionWhenProbeCloses(t *testing.T) {
	registry := newSessionRegistry()
	first := new(quic.Conn)
	second := new(quic.Conn)
	registry.put("spiffe://remote.test/cluster/remote/", first)
	if got := registry.get("spiffe://remote.test/cluster/remote"); got != first {
		t.Fatal("canonical SPIFFE identity did not resolve the registered session")
	}
	registry.put("spiffe://remote.test/cluster/remote", second)
	if got := registry.get("spiffe://remote.test/cluster/remote"); got != second {
		t.Fatalf("expected newest session, got %p want %p", got, second)
	}
	registry.remove("spiffe://remote.test/cluster/remote", second)
	if got := registry.get("spiffe://remote.test/cluster/remote"); got != first {
		t.Fatal("a closing probe removed the long-lived peer session")
	}
	registry.remove("spiffe://remote.test/cluster/remote", first)
	if got := registry.get("spiffe://remote.test/cluster/remote"); got != nil {
		t.Fatal("closed current session remained registered")
	}
}

func TestExportRouteDeniesIdentityOutsideTargetPeer(t *testing.T) {
	route := exportRoute{ServiceIdentity: "spiffe://remote/ns/default/service/api", TargetPeers: []string{"remote-local"}}
	identities := map[string][]string{"remote-local": {"spiffe://allowed.test/cluster/allowed"}}
	if routeAllowsIdentity(route, identities, "spiffe://other.test/cluster/other") {
		t.Fatal("service route allowed a SPIFFE identity outside its target peer")
	}
	if !routeAllowsIdentity(route, identities, "spiffe://allowed.test/cluster/allowed") {
		t.Fatal("service route denied its explicitly configured target peer")
	}
}

func TestWildcardExportRouteAllowsOnlyAfterMTLSAuthentication(t *testing.T) {
	route := exportRoute{ServiceIdentity: "spiffe://remote/ns/default/service/api", TargetPeers: []string{"*"}}
	if !routeAllowsIdentity(route, nil, "spiffe://approved.test/cluster/approved") {
		t.Fatal("wildcard route denied an identity already authenticated by the peer trust bundle")
	}
}
