package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/re8ch/among-clusters/internal/model"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

type exportRoute = model.ExportRoute
type importRoute struct {
	Listen           string `json:"listen"`
	Endpoint         string `json:"endpoint"`
	ServiceIdentity  string `json:"serviceIdentity"`
	ExpectedSPIFFEID string `json:"expectedSPIFFEID"`
	SessionOnly      bool   `json:"sessionOnly"`
}
type peerSession struct {
	Endpoint         string `json:"endpoint"`
	ExpectedSPIFFEID string `json:"expectedSPIFFEID"`
}
type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string][]*quic.Conn
}

const sessionHeartbeatIdentity = "among-clusters.internal/session-heartbeat"
const sessionAcceptorMaintainsHeartbeat = false
const sessionMaxConcurrentStreams int64 = 4096

func sessionQUICConfig(dial bool) *quic.Config {
	config := &quic.Config{
		KeepAlivePeriod:    15 * time.Second,
		MaxIncomingStreams: sessionMaxConcurrentStreams,
	}
	if dial {
		config.HandshakeIdleTimeout = 5 * time.Second
	}
	return config
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: map[string][]*quic.Conn{}}
}
func canonicalIdentity(identity string) string { return strings.TrimSuffix(identity, "/") }
func (r *sessionRegistry) put(identity string, connection *quic.Conn) {
	r.mu.Lock()
	key := canonicalIdentity(identity)
	// Prefer the freshest authenticated session. A half-open QUIC connection can
	// remain blocked in AcceptStream after the peer has already reconnected; if
	// it stays first, every import is sent to that stale connection forever.
	r.sessions[key] = append([]*quic.Conn{connection}, r.sessions[key]...)
	r.mu.Unlock()
}
func (r *sessionRegistry) get(identity string) *quic.Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	connections := r.sessions[canonicalIdentity(identity)]
	if len(connections) > 0 {
		return connections[0]
	}
	return nil
}
func (r *sessionRegistry) wait(ctx context.Context, identity string, timeout time.Duration) *quic.Conn {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if connection := r.get(identity); connection != nil {
			return connection
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-ticker.C:
		}
	}
}
func (r *sessionRegistry) remove(identity string, connection *quic.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := canonicalIdentity(identity)
	connections := r.sessions[key]
	for i, candidate := range connections {
		if candidate == connection {
			connections = append(connections[:i], connections[i+1:]...)
			break
		}
	}
	if len(connections) == 0 {
		delete(r.sessions, key)
	} else {
		r.sessions[key] = connections
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	cert, roots := credentials()
	var exports []exportRoute
	var imports []importRoute
	var sessions []peerSession
	peerIdentities := map[string][]string{}
	if json.Unmarshal([]byte(env("EXPORTS_JSON", "[]")), &exports) != nil || json.Unmarshal([]byte(env("IMPORTS_JSON", "[]")), &imports) != nil || json.Unmarshal([]byte(env("PEER_SESSIONS_JSON", "[]")), &sessions) != nil || json.Unmarshal([]byte(env("PEER_IDENTITIES_JSON", "{}")), &peerIdentities) != nil {
		log.Fatal("invalid service routing configuration")
	}
	routes := map[string]exportRoute{}
	for _, route := range exports {
		if route.ServiceIdentity == "" || route.Target == "" {
			log.Fatal("invalid export route")
		}
		routes[route.ServiceIdentity] = route
	}
	registry := newSessionRegistry()
	if os.Getenv("LISTENER_ENABLED") == "true" {
		go serveQUIC(ctx, cert, roots, routes, peerIdentities, registry)
	}
	for _, session := range sessions {
		if session.Endpoint == "" || session.ExpectedSPIFFEID == "" {
			log.Fatal("peer session requires endpoint and expectedSPIFFEID")
		}
		session := session
		go maintainSession(ctx, cert, roots, routes, peerIdentities, registry, session)
	}
	for _, route := range imports {
		route := route
		go serveImport(ctx, cert, roots, registry, route)
	}
	<-ctx.Done()
}

func credentials() (tls.Certificate, *x509.CertPool) {
	var cert tls.Certificate
	var err error
	for i := 0; i < 60; i++ {
		cert, err = tls.LoadX509KeyPair(env("TLS_CERT_FILE", "/identity/tls.crt"), env("TLS_KEY_FILE", "/identity/tls.key"))
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		log.Fatal(err)
	}
	bundle, err := os.ReadFile(env("PEER_BUNDLE_FILE", "/peer-bundles/ca.crt"))
	if err != nil {
		log.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		log.Fatal("invalid peer bundle")
	}
	return cert, roots
}
func tlsConfig(cert tls.Certificate, roots *x509.CertPool, expected string, server bool) *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"among-clusters/1"}}
	if server {
		c.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return currentCertificate(cert) }
		c.ClientAuth = tls.RequireAndVerifyClientCert
		c.ClientCAs = roots
		return c
	}
	c.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return currentCertificate(cert) }
	c.InsecureSkipVerify = true
	c.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("peer certificate missing")
		}
		inter := x509.NewCertPool()
		for _, certificate := range state.PeerCertificates[1:] {
			inter.AddCert(certificate)
		}
		if _, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return err
		}
		if len(state.PeerCertificates[0].URIs) != 1 || strings.TrimSuffix(state.PeerCertificates[0].URIs[0].String(), "/") != strings.TrimSuffix(expected, "/") {
			return errors.New("peer SPIFFE identity mismatch")
		}
		return nil
	}
	return c
}
func currentCertificate(fallback tls.Certificate) (*tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(env("TLS_CERT_FILE", "/identity/tls.crt"), env("TLS_KEY_FILE", "/identity/tls.key"))
	if err != nil {
		return &fallback, nil
	}
	return &certificate, nil
}
func serveQUIC(ctx context.Context, cert tls.Certificate, roots *x509.CertPool, routes map[string]exportRoute, peerIdentities map[string][]string, registry *sessionRegistry) {
	listener, err := quic.ListenAddr(env("LISTEN_ADDRESS", ":8443"), tlsConfig(cert, roots, "", true), sessionQUICConfig(false))
	if err != nil {
		log.Fatal(err)
	}
	for {
		connection, err := listener.Accept(ctx)
		if err != nil {
			return
		}
		go func() {
			defer connection.CloseWithError(0, "closed")
			certificates := connection.ConnectionState().TLS.PeerCertificates
			if len(certificates) == 0 || len(certificates[0].URIs) != 1 {
				return
			}
			peerIdentity := canonicalIdentity(certificates[0].URIs[0].String())
			// The outbound dialer owns application heartbeats. The accepting side
			// only acknowledges them in handleExport; two heartbeat loops can race
			// business streams and close an otherwise healthy shared session.
			handleConnection(ctx, connection, peerIdentity, routes, peerIdentities, registry, sessionAcceptorMaintainsHeartbeat)
		}()
	}
}

func handleConnection(ctx context.Context, connection *quic.Conn, peerIdentity string, routes map[string]exportRoute, peerIdentities map[string][]string, registry *sessionRegistry, maintainHeartbeat bool) {
	registry.put(peerIdentity, connection)
	defer registry.remove(peerIdentity, connection)
	if maintainHeartbeat {
		heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
		defer cancelHeartbeat()
		go maintainConnectionHeartbeat(heartbeatCtx, connection)
	}
	for {
		stream, err := connection.AcceptStream(ctx)
		if err != nil {
			log.Printf("peer session %s closed: %v", peerIdentity, err)
			return
		}
		go handleExport(stream, routes, peerIdentities, peerIdentity)
	}
}

func maintainConnectionHeartbeat(ctx context.Context, connection *quic.Conn) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stream, err := connection.OpenStreamSync(ctx)
			if err != nil {
				connection.CloseWithError(1, "session heartbeat failed")
				return
			}
			identity := []byte(sessionHeartbeatIdentity)
			deadline := time.Now().Add(5 * time.Second)
			_ = stream.SetDeadline(deadline)
			if binary.Write(stream, binary.BigEndian, uint16(len(identity))) != nil {
				stream.CancelWrite(1)
				connection.CloseWithError(1, "session heartbeat failed")
				return
			}
			if _, err = stream.Write(identity); err != nil {
				stream.CancelWrite(1)
				connection.CloseWithError(1, "session heartbeat failed")
				return
			}
			ack := []byte{0}
			if _, err = io.ReadFull(stream, ack); err != nil || ack[0] != 1 {
				stream.CancelRead(1)
				connection.CloseWithError(1, "session heartbeat unacknowledged")
				return
			}
			stream.Close()
		}
	}
}

func maintainSession(ctx context.Context, cert tls.Certificate, roots *x509.CertPool, routes map[string]exportRoute, peerIdentities map[string][]string, registry *sessionRegistry, session peerSession) {
	backoff := 3 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		connection, err := quic.DialAddr(ctx, strings.TrimPrefix(session.Endpoint, "quic://"), tlsConfig(cert, roots, session.ExpectedSPIFFEID, false), sessionQUICConfig(true))
		if err != nil {
			log.Printf("peer session %s: %v", session.ExpectedSPIFFEID, err)
			if !waitForRetry(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		handleConnection(ctx, connection, session.ExpectedSPIFFEID, routes, peerIdentities, registry, true)
		connection.CloseWithError(0, "reconnecting")
		if time.Since(started) >= 2*time.Minute {
			backoff = 3 * time.Second
		}
		if !waitForRetry(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > time.Minute {
		return time.Minute
	}
	return current
}

type readWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}

func handleExport(stream readWriteCloser, routes map[string]exportRoute, peerIdentities map[string][]string, peerIdentity string) {
	defer stream.Close()
	var size uint16
	if err := binary.Read(stream, binary.BigEndian, &size); err != nil || size == 0 || size > 2048 {
		log.Printf("export request from %s rejected: invalid identity frame size=%d err=%v", peerIdentity, size, err)
		return
	}
	identity := make([]byte, size)
	if _, err := io.ReadFull(stream, identity); err != nil {
		return
	}
	if string(identity) == sessionHeartbeatIdentity {
		_, _ = stream.Write([]byte{1})
		return
	}
	route, ok := dynamicExports.lookup(env("EXPORTS_FILE", "/routes/exports.json"), routes, string(identity), time.Now())
	if !ok {
		log.Printf("export request from %s rejected: service identity %s is not advertised or its dynamic lease is invalid", peerIdentity, string(identity))
		return
	}
	if !routeAllowsIdentity(route, peerIdentities, peerIdentity) {
		log.Printf("export request from %s rejected: service identity %s is outside target peers", peerIdentity, route.ServiceIdentity)
		return
	}
	upstream, err := net.DialTimeout("tcp", route.Target, 5*time.Second)
	if err != nil {
		log.Printf("export request from %s for %s failed to connect upstream %s: %v", peerIdentity, route.ServiceIdentity, route.Target, err)
		return
	}
	log.Printf("export request from %s for %s connected upstream %s", peerIdentity, route.ServiceIdentity, route.Target)
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(upstream, stream)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() { io.Copy(stream, upstream); stream.Close(); done <- struct{}{} }()
	<-done
	<-done
}
func routeAllowsIdentity(route exportRoute, peerIdentities map[string][]string, identity string) bool {
	identity = strings.TrimSuffix(identity, "/")
	for _, peer := range route.TargetPeers {
		// A wildcard route still sits behind RequireAndVerifyClientCert. It admits
		// any identity rooted in the explicitly installed peer trust bundle; it
		// never turns the listener into an anonymous public proxy.
		if peer == "*" {
			return true
		}
		for _, allowed := range peerIdentities[peer] {
			if strings.TrimSuffix(allowed, "/") == identity {
				return true
			}
		}
	}
	return false
}
func serveImport(ctx context.Context, cert tls.Certificate, roots *x509.CertPool, registry *sessionRegistry, route importRoute) {
	listener, err := net.Listen("tcp", route.Listen)
	if err != nil {
		log.Printf("import %s: %v", route.ServiceIdentity, err)
		return
	}
	go func() { <-ctx.Done(); listener.Close() }()
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go proxyImport(ctx, client, cert, roots, registry, route)
	}
}
func proxyImport(ctx context.Context, client net.Conn, cert tls.Certificate, roots *x509.CertPool, registry *sessionRegistry, route importRoute) {
	defer client.Close()
	connection := registry.wait(ctx, route.ExpectedSPIFFEID, 15*time.Second)
	owned := false
	if connection == nil {
		if route.SessionOnly {
			log.Printf("import %s: authenticated peer session unavailable", route.ServiceIdentity)
			return
		}
		if route.Endpoint == "" {
			log.Printf("import %s: no authenticated session or fallback endpoint", route.ServiceIdentity)
			return
		}
		var err error
		connection, err = quic.DialAddr(ctx, strings.TrimPrefix(route.Endpoint, "quic://"), tlsConfig(cert, roots, route.ExpectedSPIFFEID, false), sessionQUICConfig(true))
		if err != nil {
			log.Printf("import %s dial: %v", route.ServiceIdentity, err)
			return
		}
		owned = true
	}
	if owned {
		defer connection.CloseWithError(0, "closed")
	}
	openCtx, cancelOpen := context.WithTimeout(ctx, 5*time.Second)
	stream, err := connection.OpenStreamSync(openCtx)
	cancelOpen()
	if err != nil {
		// Removing a half-open session from the local registry is not enough:
		// the dialing peer can still receive heartbeat acknowledgements and will
		// therefore never reconnect. Close it so maintainSession redials.
		connection.CloseWithError(1, "import stream unavailable")
		registry.remove(route.ExpectedSPIFFEID, connection)
		log.Printf("import %s stream: %v", route.ServiceIdentity, err)
		return
	}
	identity := []byte(route.ServiceIdentity)
	if err = binary.Write(stream, binary.BigEndian, uint16(len(identity))); err != nil {
		log.Printf("import %s identity frame: %v", route.ServiceIdentity, err)
		return
	}
	if _, err = stream.Write(identity); err != nil {
		log.Printf("import %s identity write: %v", route.ServiceIdentity, err)
		return
	}
	log.Printf("import %s opened authenticated stream to %s", route.ServiceIdentity, route.ExpectedSPIFFEID)
	done := make(chan struct{}, 2)
	go func() { io.Copy(stream, client); stream.Close(); done <- struct{}{} }()
	go func() {
		io.Copy(client, stream)
		if tcp, ok := client.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
