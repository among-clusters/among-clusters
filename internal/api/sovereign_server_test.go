package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/re8ch/among-clusters/internal/model"
	"github.com/re8ch/among-clusters/internal/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInvitationIsOneTimeAndTenantBound(t *testing.T) {
	store := NewMemorySovereignStore()
	now := time.Now().UTC()
	server := &SovereignServer{Store: store, AdminToken: "admin", Now: func() time.Time { return now }}
	body := bytes.NewBufferString(`{"tenant":"tenant-a","ttlSeconds":300}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/invitations", body)
	request.Header.Set("Authorization", "Bearer admin")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var invitation map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &invitation)
	pub, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	accept := model.InvitationAcceptance{Token: invitation["token"].(string), Identity: model.IdentityRegistration{ClusterID: "c1", Tenant: "tenant-a", TrustDomain: "customer.example", SPIFFEID: "spiffe://customer.example/cluster/c1", BundleDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PublicKey: base64.RawStdEncoding.EncodeToString(pub)}}
	identityBody, _ := json.Marshal(accept.Identity)
	accept.Proof = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, identityBody))
	payload, _ := json.Marshal(accept)
	path := "/v1/invitations/" + invitation["invitationID"].(string) + "/accept"
	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload)))
	if first.Code != 201 {
		t.Fatalf("accept: %d %s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload)))
	if second.Code != 401 {
		t.Fatalf("reused invitation returned %d", second.Code)
	}
}
func TestInvitationRequiresAdmin(t *testing.T) {
	server := &SovereignServer{Store: NewMemorySovereignStore(), AdminToken: "admin"}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/invitations", bytes.NewBufferString(`{"tenant":"x"}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", response.Code)
	}
}

func TestCapabilityRejectionDoesNotConsumeInvitation(t *testing.T) {
	store := NewMemorySovereignStore()
	now := time.Now().UTC()
	invitation := model.Invitation{ID: "invite", Tenant: "tenant-a", TokenHash: "hash", Capabilities: []string{"quic-mtls"}, ExpiresAt: now.Add(time.Minute)}
	if err := store.CreateInvitation(context.Background(), invitation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeInvitation(context.Background(), "invite", "hash", "tenant-a", []string{"service"}, now); err == nil {
		t.Fatal("uninvited capability was accepted")
	}
	if _, err := store.ConsumeInvitation(context.Background(), "invite", "hash", "tenant-a", []string{"quic-mtls"}, now); err != nil {
		t.Fatalf("valid retry was consumed by rejected attempt: %v", err)
	}
}

func TestReleasedInvitationCanBeRetried(t *testing.T) {
	store := NewMemorySovereignStore()
	now := time.Now().UTC()
	invitation := model.Invitation{ID: "retry", Tenant: "tenant-a", TokenHash: "hash", ExpiresAt: now.Add(time.Minute)}
	if err := store.CreateInvitation(context.Background(), invitation); err != nil {
		t.Fatal(err)
	}
	consumed, err := store.ConsumeInvitation(context.Background(), "retry", "hash", "tenant-a", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReleaseInvitation(context.Background(), consumed); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConsumeInvitation(context.Background(), "retry", "hash", "tenant-a", nil, now); err != nil {
		t.Fatalf("released invitation could not be retried: %v", err)
	}
}

func TestServiceSnapshotReceiptUsesAcceptedPublicationTime(t *testing.T) {
	store := NewMemorySovereignStore()
	now := time.Now().UTC().Truncate(time.Second)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	identity := model.IdentityRegistration{ClusterID: "provider", Tenant: "tenant", TrustDomain: "provider.example", SPIFFEID: "spiffe://provider.example/cluster/provider", PublicKey: base64.RawStdEncoding.EncodeToString(pub)}
	store.Identities["tenant/provider"] = identity
	payload, _ := json.Marshal(model.ServiceSnapshot{})
	message := model.ControlMessage{Issuer: identity.SPIFFEID, Audience: "hub", Generation: 1, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Nonce: "publication-1", Type: "service.snapshot", Payload: payload}
	if err := protocol.Sign(&message, key); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(message)
	response := httptest.NewRecorder()
	(&SovereignServer{Store: store, Now: func() time.Time { return now }}).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/peers/tenant/provider/messages", bytes.NewReader(body)))
	var receipt model.ServiceSnapshotReceipt
	err := json.Unmarshal(response.Body.Bytes(), &receipt)
	if response.Code != 202 || err != nil || !receipt.PublishedAt.Equal(now) || receipt.Generation != message.Generation {
		t.Fatalf("invalid accepted receipt: %d %s", response.Code, response.Body.String())
	}
}
