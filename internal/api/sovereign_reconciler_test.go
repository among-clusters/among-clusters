package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
)

func TestOneSidedPublicGatewayIsConnectable(t *testing.T) {
	scheme := runtime.NewScheme()
	link := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "peering.re8ch.com/v1alpha1",
		"kind":       "AuthenticatedLink",
		"metadata":   map[string]any{"name": "provider-byoc", "namespace": "tenant"},
		"spec": map[string]any{
			"peerRef":      "provider-byoc",
			"transport":    "quic-mtls",
			"localGateway": "quic://203.0.113.10:8443",
		},
	}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{linksGVR: "AuthenticatedLinkList"}, link)
	reconciler := &SovereignReconciler{Client: client, Now: func() time.Time { return time.Unix(1_000, 0).UTC() }}
	reconciler.reconcileLinks(context.Background())
	result, err := client.Resource(linksGVR).Namespace("tenant").Get(context.Background(), "provider-byoc", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := unstructured.NestedString(result.Object, "status", "state")
	reason, _, _ := unstructured.NestedString(result.Object, "status", "reason")
	if state != "Connecting" || reason != "" {
		t.Fatalf("state=%q reason=%q, want one-sided provider endpoint to remain connectable", state, reason)
	}
}

func TestImportObservesEndpointRevisionAndWithdrawsOnExpiry(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	ad := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "ServiceAdvertisement", "metadata": map[string]any{"name": "canary", "namespace": "tenant", "uid": "ad-uid", "generation": int64(2), "annotations": map[string]any{"peering.re8ch.com/last-published-at": now.Format(time.RFC3339)}}, "spec": map[string]any{"publisherRef": "provider", "serviceIdentity": "spiffe://provider.example/ns/apps/service/canary", "protocol": "https", "ttlSeconds": int64(60), "targetPeers": []any{"provider-consumer"}, "publicIngress": map[string]any{"protocol": "https", "address": "192.0.2.10", "port": int64(443), "hostname": "canary.example.com", "path": "/health", "revision": "reviewed-1"}}}}
	imp := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "ImportedService", "metadata": map[string]any{"name": "canary-import", "namespace": "tenant", "uid": "import-uid", "generation": int64(1)}, "spec": map[string]any{"advertisementRef": "canary", "peerRef": "provider-consumer", "localServiceName": "canary-local"}}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{importsGVR: "ImportedServiceList"}, ad, imp)
	r := &SovereignReconciler{Client: client, Core: corefake.NewSimpleClientset(), Now: func() time.Time { return now }}
	r.reconcileImports(context.Background())
	got, _ := client.Resource(importsGVR).Namespace("tenant").Get(context.Background(), "canary-import", metav1.GetOptions{})
	state, _, _ := unstructured.NestedString(got.Object, "status", "state")
	address, _, _ := unstructured.NestedString(got.Object, "status", "publicIngress", "address")
	if state != "Ready" || address != "192.0.2.10" {
		t.Fatalf("state %s endpoint %s", state, address)
	}
	fixture, err := json.Marshal(map[string]any{"advertisement": ad.Object, "importedService": got.Object})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("PUBLIC_INGRESS_CONTRACT=" + string(fixture))
	_ = unstructured.SetNestedField(ad.Object, "192.0.2.11", "spec", "publicIngress", "address")
	_, _ = client.Resource(advertisementsGVR).Namespace("tenant").Update(context.Background(), ad, metav1.UpdateOptions{})
	r.reconcileImports(context.Background())
	got, _ = client.Resource(importsGVR).Namespace("tenant").Get(context.Background(), "canary-import", metav1.GetOptions{})
	address, _, _ = unstructured.NestedString(got.Object, "status", "publicIngress", "address")
	if address != "192.0.2.11" {
		t.Fatal("endpoint not refreshed")
	}
	now = now.Add(61 * time.Second)
	r.reconcileImports(context.Background())
	got, _ = client.Resource(importsGVR).Namespace("tenant").Get(context.Background(), "canary-import", metav1.GetOptions{})
	state, _, _ = unstructured.NestedString(got.Object, "status", "state")
	_, found, _ := unstructured.NestedMap(got.Object, "status", "publicIngress")
	if state != "Expired" || found {
		t.Fatalf("expired import still usable: %s ingress=%v", state, found)
	}
	now = now.Add(-61 * time.Second)
	_ = unstructured.SetNestedField(ad.Object, true, "spec", "revoked")
	_, _ = client.Resource(advertisementsGVR).Namespace("tenant").Update(context.Background(), ad, metav1.UpdateOptions{})
	r.reconcileImports(context.Background())
	got, _ = client.Resource(importsGVR).Namespace("tenant").Get(context.Background(), "canary-import", metav1.GetOptions{})
	reason, _, _ := unstructured.NestedString(got.Object, "status", "reason")
	if reason != "AdvertisementRevoked" {
		t.Fatalf("revocation not propagated: %s", reason)
	}
}
