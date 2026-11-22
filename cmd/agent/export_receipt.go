package main

import (
	"errors"
	"fmt"

	"github.com/re8ch/among-clusters/internal/model"
)

func approvedRoutePeers(trustDomain string, services []model.AdvertisedService, receipt model.ServiceSnapshotReceipt) (map[string][]string, error) {
	if len(receipt.Exports) != len(services) {
		return nil, errors.New("hub did not approve every requested Service")
	}
	wanted := map[string]model.AdvertisedService{}
	for _, service := range services {
		identity := fmt.Sprintf("spiffe://%s/ns/%s/service/%s", trustDomain, service.Namespace, service.Name)
		if _, ok := wanted[identity]; ok {
			return nil, errors.New("duplicate requested Service")
		}
		wanted[identity] = service
	}
	result := map[string][]string{}
	for _, approved := range receipt.Exports {
		service, ok := wanted[approved.ServiceIdentity]
		if !ok || approved.TTLSeconds != service.TTLSeconds || len(approved.ApprovedPeerRefs) == 0 {
			return nil, errors.New("hub export receipt scope mismatch")
		}
		if _, duplicate := result[approved.ServiceIdentity]; duplicate {
			return nil, errors.New("duplicate hub export approval")
		}
		wildcard := len(service.TargetPeers) == 1 && service.TargetPeers[0] == "*"
		seen := map[string]bool{}
		for _, peer := range approved.ApprovedPeerRefs {
			if peer == "" || peer == "*" || seen[peer] {
				return nil, errors.New("hub approval needs exact unique peer refs")
			}
			allowed := wildcard
			for _, requested := range service.TargetPeers {
				allowed = allowed || requested == peer
			}
			if !allowed {
				return nil, errors.New("hub approved an unrequested peer")
			}
			seen[peer] = true
		}
		result[approved.ServiceIdentity] = append([]string(nil), approved.ApprovedPeerRefs...)
	}
	return result, nil
}
