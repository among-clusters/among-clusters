package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/re8ch/among-clusters/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func exportedIdentity(owner model.IdentityRegistration, service model.AdvertisedService) string {
	return fmt.Sprintf("spiffe://%s/ns/%s/service/%s", owner.TrustDomain, service.Namespace, service.Name)
}

// ApprovedExports reads only the exact owner/frame's materialized advertisements.
// No caller-provided '*' reaches the gateway as a dynamic approval.
func (s *KubernetesSovereignStore) ApprovedExports(ctx context.Context, owner model.IdentityRegistration, services []model.AdvertisedService, published time.Time, generation uint64) ([]model.ApprovedServiceExport, error) {
	result := []model.ApprovedServiceExport{}
	for _, service := range services {
		ad, err := s.Dynamic.Resource(advertisementsGVR).Namespace(owner.Tenant).Get(ctx, advertisementName(owner.ClusterID, service), metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		identity, _, _ := unstructured.NestedString(ad.Object, "spec", "serviceIdentity")
		publisher, _, _ := unstructured.NestedString(ad.Object, "spec", "publisherRef")
		revoked, _, _ := unstructured.NestedBool(ad.Object, "spec", "revoked")
		ttl, _, _ := unstructured.NestedInt64(ad.Object, "spec", "ttlSeconds")
		port, _, _ := unstructured.NestedInt64(ad.Object, "spec", "port")
		declaredGeneration, _, _ := unstructured.NestedInt64(ad.Object, "spec", "generation")
		class, _, _ := unstructured.NestedString(ad.Object, "spec", "serviceClass")
		protocol, _, _ := unstructured.NestedString(ad.Object, "spec", "protocol")
		policy, _, _ := unstructured.NestedString(ad.Object, "spec", "policyRef")
		if generation == 0 || ad.GetDeletionTimestamp() != nil || revoked || publisher != owner.ClusterID || identity != exportedIdentity(owner, service) || ad.GetLabels()["peering.re8ch.com/publisher"] != owner.ClusterID || ad.GetAnnotations()["peering.re8ch.com/control-generation"] != strconv.FormatUint(generation, 10) || ad.GetAnnotations()["peering.re8ch.com/last-published-at"] != published.UTC().Format(time.RFC3339) || ttl != service.TTLSeconds || port != int64(service.Port) || declaredGeneration != service.Generation || class != service.ServiceClass || protocol != service.Protocol || policy != service.PolicyRef {
			return nil, errors.New("publication owner/frame mismatch")
		}
		peers := []string{}
		for _, peerRef := range stringSlice(ad.Object, "spec", "targetPeers") {
			if peerRef == "" || peerRef == "*" || containsString(peers, peerRef) {
				return nil, errors.New("invalid approved peer scope")
			}
			peer, err := s.Dynamic.Resource(peersGVR).Namespace(owner.Tenant).Get(ctx, peerRef, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			suspended, _, _ := unstructured.NestedBool(peer.Object, "spec", "suspended")
			state, _, _ := unstructured.NestedString(peer.Object, "status", "state")
			local, _, _ := unstructured.NestedString(peer.Object, "spec", "localIdentityRef")
			remote, _, _ := unstructured.NestedString(peer.Object, "spec", "remoteIdentityRef")
			if peer.GetDeletionTimestamp() == nil && !suspended && state == "Ready" && (owner.ClusterID == local || owner.ClusterID == remote) {
				peers = append(peers, peerRef)
			}
		}
		if len(peers) == 0 {
			return nil, errors.New("no Ready approved target peers")
		}
		sort.Strings(peers)
		result = append(result, model.ApprovedServiceExport{ServiceIdentity: identity, ApprovedPeerRefs: peers, TTLSeconds: ttl})
	}
	return result, nil
}

func (s *MemorySovereignStore) ApprovedExports(_ context.Context, owner model.IdentityRegistration, services []model.AdvertisedService, published time.Time, generation uint64) ([]model.ApprovedServiceExport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := owner.Tenant + "/" + owner.ClusterID
	if generation == 0 || s.PublicationGenerations[key] != generation || !s.PublicationTimes[key].Equal(published) {
		return nil, errors.New("publication frame mismatch")
	}
	result := []model.ApprovedServiceExport{}
	for _, service := range s.Advertisements[key] {
		peers := append([]string(nil), service.TargetPeers...)
		if len(peers) == 0 || containsString(peers, "*") {
			return nil, errors.New("test store needs exact approved peers")
		}
		result = append(result, model.ApprovedServiceExport{ServiceIdentity: exportedIdentity(owner, service), ApprovedPeerRefs: peers, TTLSeconds: service.TTLSeconds})
	}
	return result, nil
}
