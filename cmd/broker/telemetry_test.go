package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"github.com/re8ch/among-clusters/internal/credential"
	"github.com/re8ch/among-clusters/internal/model"
	"github.com/re8ch/among-clusters/internal/telemetry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstrumentedBrokerRetainsCredentialBoundaryAndProxySemantics(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	calls := atomic.Int32{}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-short-lived" {
			t.Error("broker credential not injected")
		}
		if r.Header.Get("sentry-trace") != "" || r.Header.Get("baggage") != "" || r.Header.Get(telemetry.OperationHeader) != "" {
			t.Error("diagnostic headers leaked into remote API")
		}
		if r.URL.Path != "/version" || r.URL.RawQuery != "timeout=120s" {
			t.Error("proxy path/query changed")
		}
		w.Write([]byte(`{"gitVersion":"fixture"}`))
	}))
	defer target.Close()
	private, _ := ecdh.X25519().GenerateKey(rand.Reader)
	payload := model.CredentialPayload{Token: "fixture-short-lived", Server: target.URL, CertificateAuthority: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw})), ExpiresAt: time.Now().Add(time.Hour)}
	encrypted, _ := credential.Encrypt(private.PublicKey().Bytes(), payload)
	raw, _ := json.Marshal(encrypted)
	b := &broker{client: fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "broker", Name: "fixture-admin"}, Data: map[string][]byte{"envelope.json": raw}}), namespace: "broker", private: private}
	request := httptest.NewRequest("GET", "/k8s/fixture/admin/version?timeout=120s", nil)
	request.Header.Set("Authorization", "Bearer credential://among-clusters/fixture/admin")
	request.Header.Set("sentry-trace", "12345678901234567890123456789012-1234567890123456-1")
	request.Header.Set("baggage", "sentry-user_id=private")
	op := "op_12345678-1234-1234-1234-123456789012"
	request.Header.Set(telemetry.OperationHeader, op)
	out := httptest.NewRecorder()
	telemetry.NewFromEnv("among-broker").Handler(http.HandlerFunc(b.proxy)).ServeHTTP(out, request)
	if out.Code != 200 || calls.Load() != 1 || out.Header().Get(telemetry.OperationHeader) != op {
		t.Fatalf("status=%d calls=%d operation=%s", out.Code, calls.Load(), out.Header().Get(telemetry.OperationHeader))
	}
	payload.ExpiresAt = time.Now().Add(-time.Minute)
	encrypted, _ = credential.Encrypt(private.PublicKey().Bytes(), payload)
	raw, _ = json.Marshal(encrypted)
	secret, _ := b.client.CoreV1().Secrets("broker").Get(context.Background(), "fixture-admin", metav1.GetOptions{})
	secret.Data["envelope.json"] = raw
	b.client.CoreV1().Secrets("broker").Update(context.Background(), secret, metav1.UpdateOptions{})
	out = httptest.NewRecorder()
	telemetry.NewFromEnv("among-broker").Handler(http.HandlerFunc(b.proxy)).ServeHTTP(out, request)
	if out.Code != 403 || calls.Load() != 1 {
		t.Fatal("expired credential acquired new access or business was replayed")
	}
}
func TestIdentityLookupCarriesCancellationWithoutReplay(t *testing.T) {
	calls := atomic.Int32{}
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); close(entered); <-r.Context().Done() }))
	defer server.Close()
	b := &broker{hub: server.URL, http: &http.Client{Timeout: 5 * time.Second, Transport: telemetry.Transport(http.DefaultTransport, "hub_request", server.URL)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.identity(ctx, "fixture", "cluster"); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("lookup lost caller cancellation")
	}
	if calls.Load() != 1 {
		t.Fatal("lookup replayed")
	}
}
