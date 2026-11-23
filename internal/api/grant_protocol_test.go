package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/re8ch/among-clusters/internal/model"
	"github.com/re8ch/among-clusters/internal/protocol"
)

type scopedGrantStore struct {
	*MemorySovereignStore
	grants []model.GrantInstruction
}

func (s *scopedGrantStore) PendingGrants(context.Context, string, string, time.Time) ([]model.GrantInstruction, error) {
	return s.grants, nil
}

func TestSignedGrantPollingNeverWidensExactNamesOnLegacyAgents(t *testing.T) {
	for _, version := range []int{1, 2} {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		now := time.Now().UTC().Truncate(time.Second)
		identity := model.IdentityRegistration{Tenant: "tenant", ClusterID: "owner", TrustDomain: "owner.example",
			SPIFFEID: "spiffe://owner.example/cluster/owner", PublicKey: base64.RawStdEncoding.EncodeToString(pub)}
		memory := NewMemorySovereignStore()
		memory.Identities["tenant/owner"] = identity
		store := &scopedGrantStore{MemorySovereignStore: memory, grants: []model.GrantInstruction{
			{Name: "existing-admin", Scope: "Cluster", Rules: []model.AccessRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}},
			{Name: "release-canary", Scope: "Namespaced", Namespaces: []string{"default"},
				Rules: []model.AccessRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "patch"}, ResourceNames: []string{"release-canary"}}}},
		}}
		messageType := "grant.poll"
		if version == 2 {
			messageType = "grant.poll.v2"
		}
		message := model.ControlMessage{Issuer: identity.SPIFFEID, Audience: "hub", Generation: 1,
			IssuedAt: now, ExpiresAt: now.Add(time.Minute), Nonce: "poll", Type: messageType, Payload: json.RawMessage(`{}`)}
		if err := protocol.Sign(&message, key); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(message)
		response := httptest.NewRecorder()
		(&SovereignServer{Store: store, Now: func() time.Time { return now }}).Handler().ServeHTTP(response,
			httptest.NewRequest("POST", "/v1/peers/tenant/owner/messages", bytes.NewReader(body)))
		var result model.GrantSnapshot
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.ProtocolVersion != version {
			t.Fatalf("v%d: status=%d body=%s", version, response.Code, response.Body.String())
		}
		if len(result.Grants) != 2 || result.Grants[0].Revoked || len(result.Grants[0].Rules) != 1 {
			t.Fatal("unscoped existing grant was changed")
		}
		named := result.Grants[1]
		if version == 1 {
			// Simulate a legacy JSON client: even after discarding unknown fields,
			// it sees cleanup, never a broad active rule.
			var legacy struct {
				Revoked bool
				Rules   []struct{ Resources, Verbs []string }
			}
			encoded, _ := json.Marshal(named)
			_ = json.Unmarshal(encoded, &legacy)
			if !legacy.Revoked || len(legacy.Rules) != 0 {
				t.Fatal("legacy client could install a wider role")
			}
		} else if named.Revoked || len(named.Rules[0].ResourceNames) != 1 || named.Rules[0].ResourceNames[0] != "release-canary" {
			t.Fatal("v2 lost exact names")
		}
		if store.grants[1].Revoked || len(store.grants[1].Rules) != 1 {
			t.Fatal("wire projection mutated owner intent")
		}
	}
}
