package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/re8ch/among-clusters/internal/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func leaseAgent(t *testing.T, endpoint string) *agent {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "apps", Labels: map[string]string{"peering.re8ch.com/advertise": "true"}, Annotations: map[string]string{"peering.re8ch.com/protocol": "http", "peering.re8ch.com/service-class": "canary", "peering.re8ch.com/policy-ref": "canary-export", "peering.re8ch.com/target-peers": "provider-consumer", "peering.re8ch.com/ttl-seconds": "60"}}, Spec: corev1.ServiceSpec{ClusterIP: "192.0.2.10", Ports: []corev1.ServicePort{{Port: 8080}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "identity", Namespace: "agent", Annotations: map[string]string{}}}
	return &agent{client: fake.NewSimpleClientset(svc, secret), clusterID: "provider", tenant: "tenant", trustDomain: "provider.example", namespace: "agent", secretName: "identity", key: key, endpoint: endpoint, http: &http.Client{Timeout: time.Second}}
}

func TestPublicationRequiresHubAcceptanceAndReceiptBeforeRouteWrite(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		accepted bool
	}{{"denied", 403, "policy denied", false}, {"old-hub-no-receipt", 202, "", false}, {"wrong-generation", 202, `{"publishedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","generation":2,"exports":[{"serviceIdentity":"spiffe://provider.example/ns/apps/service/canary","approvedPeerRefs":["provider-consumer"],"ttlSeconds":60}]}`, false}, {"missing-publication-time", 202, `{"generation":1,"exports":[{"serviceIdentity":"spiffe://provider.example/ns/apps/service/canary","approvedPeerRefs":["provider-consumer"],"ttlSeconds":60}]}`, false}, {"accepted", 202, `{"publishedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","generation":1,"exports":[{"serviceIdentity":"spiffe://provider.example/ns/apps/service/canary","approvedPeerRefs":["provider-consumer"],"ttlSeconds":60}]}`, true}} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			a := leaseAgent(t, server.URL)
			err := a.publishServices(context.Background())
			if !called {
				t.Fatal("hub never consulted")
			}
			cm, getErr := a.client.CoreV1().ConfigMaps("agent").Get(context.Background(), "among-clusters-service-routes", metav1.GetOptions{})
			if !test.accepted {
				if err == nil || !apierrors.IsNotFound(getErr) {
					t.Fatalf("unaccepted publication installed route: %v %v", err, getErr)
				}
				return
			}
			if err != nil || getErr != nil {
				t.Fatalf("accepted snapshot failed: %v %v", err, getErr)
			}
			var routes []model.ExportRoute
			_ = json.Unmarshal([]byte(cm.Data["exports.json"]), &routes)
			if len(routes) != 1 || !routes[0].LeaseRequired || routes[0].AcceptedGeneration != 1 {
				t.Fatalf("missing accepted lease %#v", routes)
			}
			var receipt struct {
				PublishedAt time.Time `json:"publishedAt"`
			}
			_ = json.Unmarshal([]byte(test.body), &receipt)
			expires, _ := time.Parse(time.RFC3339Nano, routes[0].ExpiresAt)
			if !expires.Equal(receipt.PublishedAt.Add(time.Minute)) {
				t.Fatal("local renewal extended beyond hub publication lease")
			}
		})
	}
}

func TestRemovedDynamicRoutePersistsScopedTombstone(t *testing.T) {
	a := leaseAgent(t, "")
	now := time.Now().UTC()
	service := model.AdvertisedService{Name: "canary", Namespace: "apps", Port: 8080, TTLSeconds: 60, TargetPeers: []string{"provider-consumer"}}
	if err := a.writeGatewayRoutes(context.Background(), []model.AdvertisedService{service}, now, 1, map[string][]string{"spiffe://provider.example/ns/apps/service/canary": {"provider-consumer"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := a.writeGatewayRoutes(context.Background(), nil, now, 1, nil); err != nil {
			t.Fatal(err)
		}
		cm, _ := a.client.CoreV1().ConfigMaps("agent").Get(context.Background(), "among-clusters-service-routes", metav1.GetOptions{})
		var routes []model.ExportRoute
		_ = json.Unmarshal([]byte(cm.Data["exports.json"]), &routes)
		if len(routes) != 1 || !routes[0].Revoked || !routes[0].LeaseRequired || routes[0].Target != "" {
			t.Fatalf("withdrawal tombstone lost %#v", routes)
		}
	}
}

func TestDynamicWildcardUsesHubSubsetAndRejectsMissingOrExpandedApprovals(t *testing.T) {
	service := model.AdvertisedService{Name: "canary", Namespace: "apps", TTLSeconds: 60, TargetPeers: []string{"*"}}
	identity := "spiffe://provider.example/ns/apps/service/canary"
	approved := model.ApprovedServiceExport{ServiceIdentity: identity, ApprovedPeerRefs: []string{"ready"}, TTLSeconds: 60}
	receipt := model.ServiceSnapshotReceipt{Exports: []model.ApprovedServiceExport{approved}}
	peers, err := approvedRoutePeers("provider.example", []model.AdvertisedService{service}, receipt)
	if err != nil || len(peers[identity]) != 1 || peers[identity][0] != "ready" {
		t.Fatalf("wildcard was not narrowed %#v %v", peers, err)
	}
	for _, bad := range []model.ServiceSnapshotReceipt{{}, {Exports: []model.ApprovedServiceExport{{ServiceIdentity: identity, TTLSeconds: 60}}}, {Exports: []model.ApprovedServiceExport{{ServiceIdentity: identity, TTLSeconds: 60, ApprovedPeerRefs: []string{"*"}}}}, {Exports: []model.ApprovedServiceExport{{ServiceIdentity: "spiffe://other/ns/apps/service/canary", TTLSeconds: 60, ApprovedPeerRefs: []string{"ready"}}}}} {
		if _, err = approvedRoutePeers("provider.example", []model.AdvertisedService{service}, bad); err == nil {
			t.Fatal("unbound/wildcard approval accepted")
		}
	}
	service.TargetPeers = []string{"requested"}
	if _, err = approvedRoutePeers("provider.example", []model.AdvertisedService{service}, receipt); err == nil {
		t.Fatal("unrequested peer accepted")
	}
}
