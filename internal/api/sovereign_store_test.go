package api

import (
	"context"
	"testing"
	"time"

	"github.com/re8ch/among-clusters/internal/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestRecordGenerationMarksIdentityReady(t *testing.T) {
	identity := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "peering.re8ch.com/v1alpha1",
		"kind":       "ClusterIdentity",
		"metadata":   map[string]any{"name": "qwen", "namespace": "byoc-pilot", "generation": int64(1)},
		"spec":       map[string]any{"clusterID": "qwen"},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), identity)
	store := &KubernetesSovereignStore{Dynamic: client}

	if err := store.RecordGeneration(context.Background(), "byoc-pilot", "qwen", 1, "nonce-1"); err != nil {
		t.Fatal(err)
	}
	got, err := client.Resource(identitiesGVR).Namespace("byoc-pilot").Get(context.Background(), "qwen", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if state, _, _ := unstructured.NestedString(got.Object, "status", "state"); state != "Ready" {
		t.Fatalf("identity state = %q, want Ready", state)
	}
	if lastSeenAt, _, _ := unstructured.NestedString(got.Object, "status", "lastSeenAt"); lastSeenAt == "" {
		t.Fatal("identity lastSeenAt was not recorded")
	}
}

func TestSignedSnapshotMaterializesOnlyPolicyAllowedService(t *testing.T) {
	identity := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "ClusterIdentity", "metadata": map[string]any{"name": "qwen", "namespace": "pilot"}, "spec": map[string]any{"clusterID": "qwen", "trustDomain": "qwen.test", "gatewayEndpoints": []any{"quic://qwen:8443"}}}}
	peer := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "Peer", "metadata": map[string]any{"name": "qwen-re8ch", "namespace": "pilot"}, "spec": map[string]any{"localIdentityRef": "qwen", "remoteIdentityRef": "re8ch"}}}
	policy := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "PeerPolicy", "metadata": map[string]any{"name": "api-export", "namespace": "pilot"}, "spec": map[string]any{"peerSelector": map[string]any{"matchNames": []any{"qwen-re8ch"}}, "serviceClasses": []any{"kubernetes.control-plane"}, "protocols": []any{"kubernetes-api"}, "ports": []any{int64(443)}, "directions": []any{"export"}, "maxAdvertisements": int64(1)}}}
	lists := map[schema.GroupVersionResource]string{advertisementsGVR: "ServiceAdvertisementList", importsGVR: "ImportedServiceList", peersGVR: "PeerList", schema.GroupVersionResource{Group: "peering.re8ch.com", Version: "v1alpha1", Resource: "peerpolicies"}: "PeerPolicyList"}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, identity, peer, policy)
	store := &KubernetesSovereignStore{Dynamic: client}
	service := model.AdvertisedService{Name: "kubernetes", Namespace: "default", ServiceClass: "kubernetes.control-plane", Protocol: "kubernetes-api", Port: 443, TargetPeers: []string{"qwen-re8ch"}, TTLSeconds: 60, PolicyRef: "api-export", Generation: 1}
	service.PublicIngress = &model.PublicIngress{Endpoints: []model.PublicIngressEndpoint{{ID: "site-a", Site: "site-a", Region: "region-a", Protocol: "https", Address: "192.0.2.10", Port: 443, Hostname: "canary.example.com", Path: "/health", Revision: "reviewed-1"}}}
	if err := store.SyncAdvertisements(context.Background(), "pilot", "qwen", []model.AdvertisedService{service}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ads, err := client.Resource(advertisementsGVR).Namespace("pilot").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(ads.Items) != 1 {
		t.Fatalf("ads=%d err=%v", len(ads.Items), err)
	}
	ingress, found, _ := unstructured.NestedMap(ads.Items[0].Object, "spec", "publicIngress")
	if !found || ingress["endpoints"].([]any)[0].(map[string]any)["address"] != "192.0.2.10" {
		t.Fatalf("ingress not published: %#v", ingress)
	}
	if got := stringSlice(ads.Items[0].Object, "spec", "gatewayEndpoints"); len(got) != 1 || got[0] != "quic://qwen:8443" {
		t.Fatalf("QUIC changed: %v", got)
	}
	service.PublicIngress.Endpoints[0].Address = "192.0.2.11"
	service.PublicIngress.Endpoints[0].Revision = "reviewed-2"
	if err := store.SyncAdvertisements(context.Background(), "pilot", "qwen", []model.AdvertisedService{service}, time.Now()); err != nil {
		t.Fatal(err)
	}
	updated, _ := client.Resource(advertisementsGVR).Namespace("pilot").Get(context.Background(), ads.Items[0].GetName(), metav1.GetOptions{})
	ingressUpdated, _, _ := unstructured.NestedMap(updated.Object, "spec", "publicIngress")
	address := ingressUpdated["endpoints"].([]any)[0].(map[string]any)["address"]
	if address != "192.0.2.11" {
		t.Fatalf("endpoint did not converge: %s", address)
	}
	imports, err := client.Resource(importsGVR).Namespace("pilot").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(imports.Items) != 1 {
		t.Fatalf("imports=%d err=%v", len(imports.Items), err)
	}
	secondPolicy := policy.DeepCopy()
	secondPolicy.SetName("api-export-second")
	_, err = client.Resource(schema.GroupVersionResource{Group: "peering.re8ch.com", Version: "v1alpha1", Resource: "peerpolicies"}).Namespace("pilot").Create(context.Background(), secondPolicy, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secondService := service
	secondService.Name = "another-real-service"
	secondService.PolicyRef = "api-export-second"
	if err = store.SyncAdvertisements(context.Background(), "pilot", "qwen", []model.AdvertisedService{service, secondService}, time.Now()); err != nil {
		t.Fatalf("unrelated policy consumed quota: %v", err)
	}
	secondService.PolicyRef = service.PolicyRef
	if err = store.SyncAdvertisements(context.Background(), "pilot", "qwen", []model.AdvertisedService{service, secondService}, time.Now()); err == nil {
		t.Fatal("same policy quota not enforced")
	}
}

func TestWildcardSnapshotExpandsOnlyReadyUnsuspendedPeers(t *testing.T) {
	identity := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "ClusterIdentity", "metadata": map[string]any{"name": "re8ch", "namespace": "pilot"}, "spec": map[string]any{"clusterID": "re8ch", "trustDomain": "re8ch.internal", "gatewayEndpoints": []any{"quic://provider:8443"}}}}
	ready := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "Peer", "metadata": map[string]any{"name": "re8ch-ready", "namespace": "pilot"}, "spec": map[string]any{"localIdentityRef": "re8ch", "remoteIdentityRef": "ready", "suspended": false}, "status": map[string]any{"state": "Ready"}}}
	pending := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "Peer", "metadata": map[string]any{"name": "re8ch-pending", "namespace": "pilot"}, "spec": map[string]any{"localIdentityRef": "re8ch", "remoteIdentityRef": "pending", "suspended": false}, "status": map[string]any{"state": "PendingConfirmation"}}}
	suspended := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "Peer", "metadata": map[string]any{"name": "re8ch-suspended", "namespace": "pilot"}, "spec": map[string]any{"localIdentityRef": "re8ch", "remoteIdentityRef": "suspended", "suspended": true}, "status": map[string]any{"state": "Ready"}}}
	policy := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "PeerPolicy", "metadata": map[string]any{"name": "open-ready", "namespace": "pilot"}, "spec": map[string]any{"peerSelector": map[string]any{"matchAllReady": true}, "serviceClasses": []any{"network.egress.mihomo"}, "protocols": []any{"tcp"}, "ports": []any{int64(17891)}, "directions": []any{"export"}, "maxAdvertisements": int64(1)}}}
	lists := map[schema.GroupVersionResource]string{advertisementsGVR: "ServiceAdvertisementList", importsGVR: "ImportedServiceList", peersGVR: "PeerList", schema.GroupVersionResource{Group: "peering.re8ch.com", Version: "v1alpha1", Resource: "peerpolicies"}: "PeerPolicyList"}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, identity, ready, pending, suspended, policy)
	store := &KubernetesSovereignStore{Dynamic: client}
	service := model.AdvertisedService{Name: "mihomos", Namespace: "egress-fabric", ServiceClass: "network.egress.mihomo", Protocol: "tcp", Port: 17891, TargetPeers: []string{"*"}, TTLSeconds: 60, PolicyRef: "open-ready", Generation: 1}
	if err := store.SyncAdvertisements(context.Background(), "pilot", "re8ch", []model.AdvertisedService{service}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ads, err := client.Resource(advertisementsGVR).Namespace("pilot").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(ads.Items) != 1 {
		t.Fatalf("ads=%d err=%v", len(ads.Items), err)
	}
	targets := stringSlice(ads.Items[0].Object, "spec", "targetPeers")
	if len(targets) != 1 || targets[0] != "re8ch-ready" {
		t.Fatalf("target peers = %v, want only re8ch-ready", targets)
	}
}

func TestPendingGrantCarriesExplicitClusterScope(t *testing.T) {
	now := time.Now().UTC()
	advertisement := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": "ServiceAdvertisement", "metadata": map[string]any{"name": "qwen-kubernetes", "namespace": "pilot"}, "spec": map[string]any{"publisherRef": "qwen", "protocol": "kubernetes-api"}}}
	grant := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "peering.re8ch.com/v1alpha1",
		"kind":       "ManagedAccessGrant",
		"metadata":   map[string]any{"name": "qwen-admin", "namespace": "pilot", "generation": int64(7)},
		"spec": map[string]any{
			"peerRef": "qwen-re8ch", "advertisementRef": "qwen-kubernetes", "scope": "Cluster",
			"expiresAt": now.Add(time.Hour).Format(time.RFC3339), "approved": true, "revoked": false,
			"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"configmaps"}, "verbs": []any{"get", "patch"}, "resourceNames": []any{"reviewed-canary"}}},
		},
	}}
	lists := map[schema.GroupVersionResource]string{grantsGVR: "ManagedAccessGrantList", advertisementsGVR: "ServiceAdvertisementList"}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, advertisement, grant)
	store := &KubernetesSovereignStore{Dynamic: client}
	grants, err := store.PendingGrants(context.Background(), "pilot", "qwen", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Scope != "Cluster" || grants[0].Generation != 7 {
		t.Fatalf("grants=%+v, want one generation-bound cluster-scoped grant", grants)
	}
	if len(grants[0].Rules) != 1 || len(grants[0].Rules[0].ResourceNames) != 1 || grants[0].Rules[0].ResourceNames[0] != "reviewed-canary" {
		t.Fatal("stored grant lost its exact object names before signed delivery")
	}
	if got, want := grants[0].ProxyURL, "https://qwen-kubernetes-qwen-re8ch.pilot.svc.cluster.local:6443"; got != want {
		t.Fatalf("proxy URL = %q, want %q", got, want)
	}
	// Even malformed migration/cache input cannot silently become an unscoped
	// instruction before it reaches protocol negotiation or native RBAC.
	grant.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["resourceNames"] = []any{int64(7)}
	if _, err := client.Resource(grantsGVR).Namespace("pilot").Update(context.Background(), grant, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if grants, err := store.PendingGrants(context.Background(), "pilot", "qwen", now); err == nil || len(grants) != 0 {
		t.Fatal("invalid stored resourceNames were dropped into an unrestricted rule")
	}
}
