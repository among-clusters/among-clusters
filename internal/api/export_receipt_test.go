package api

import (
	"context"
	"github.com/re8ch/among-clusters/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"testing"
	"time"
)

func TestHubWildcardReceiptUsesOnlyReadyApprovedPeersAndExactFrame(t *testing.T) {
	owner := model.IdentityRegistration{ClusterID: "provider", Tenant: "tenant", TrustDomain: "provider.example"}
	object := func(kind, name string, spec map[string]any, status map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "peering.re8ch.com/v1alpha1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": "tenant"}, "spec": spec, "status": status}}
	}
	identity := object("ClusterIdentity", "provider", map[string]any{"clusterID": "provider", "trustDomain": "provider.example"}, nil)
	ready := object("Peer", "ready", map[string]any{"localIdentityRef": "provider", "remoteIdentityRef": "consumer", "suspended": false}, map[string]any{"state": "Ready"})
	other := object("Peer", "other-trusted", map[string]any{"localIdentityRef": "provider", "remoteIdentityRef": "other", "suspended": false}, map[string]any{"state": "PendingConfirmation"})
	policy := object("PeerPolicy", "export", map[string]any{"peerSelector": map[string]any{"matchAllReady": true}, "serviceClasses": []any{"canary"}, "protocols": []any{"http"}, "ports": []any{int64(8080)}, "directions": []any{"export"}, "maxAdvertisements": int64(1)}, nil)
	lists := map[schema.GroupVersionResource]string{advertisementsGVR: "ServiceAdvertisementList", importsGVR: "ImportedServiceList", peersGVR: "PeerList"}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, identity, ready, other, policy)
	store := &KubernetesSovereignStore{Dynamic: client}
	now := time.Now().UTC().Truncate(time.Second)
	service := model.AdvertisedService{Name: "canary", Namespace: "apps", ServiceClass: "canary", Protocol: "http", Port: 8080, TargetPeers: []string{"*"}, TTLSeconds: 60, PolicyRef: "export", Generation: 1}
	if err := store.SyncAdvertisements(context.Background(), "tenant", "provider", []model.AdvertisedService{service}, now, 7); err != nil {
		t.Fatal(err)
	}
	approved, err := store.ApprovedExports(context.Background(), owner, []model.AdvertisedService{service}, now, 7)
	if err != nil || len(approved) != 1 || len(approved[0].ApprovedPeerRefs) != 1 || approved[0].ApprovedPeerRefs[0] != "ready" {
		t.Fatalf("scope not narrowed to Ready subset: %#v %v", approved, err)
	}
	if _, err = store.ApprovedExports(context.Background(), owner, []model.AdvertisedService{service}, now, 8); err == nil {
		t.Fatal("wrong generation approved")
	}
	if _, err = store.ApprovedExports(context.Background(), owner, []model.AdvertisedService{service}, now.Add(time.Second), 7); err == nil {
		t.Fatal("wrong publication time approved")
	}
	ready.SetDeletionTimestamp(&metav1.Time{Time: now})
	_, _ = client.Resource(peersGVR).Namespace("tenant").Update(context.Background(), ready, metav1.UpdateOptions{})
	if _, err = store.ApprovedExports(context.Background(), owner, []model.AdvertisedService{service}, now, 7); err == nil {
		t.Fatal("terminating target peer approved")
	}
}
