package model

import "time"

// ExportRoute preserves explicit legacy static routes; dynamically renewed routes
// additionally carry a hub-accepted lease. Revoked entries are scoped tombstones.
type ExportRoute struct {
	ServiceIdentity    string   `json:"serviceIdentity"`
	Target             string   `json:"target,omitempty"`
	TargetPeers        []string `json:"targetPeers,omitempty"`
	LeaseRequired      bool     `json:"leaseRequired,omitempty"`
	ExpiresAt          string   `json:"expiresAt,omitempty"`
	Revoked            bool     `json:"revoked,omitempty"`
	AcceptedGeneration uint64   `json:"acceptedGeneration,omitempty"`
}

// ApprovedServiceExport records the hub-approved scope of one Service.
type ApprovedServiceExport struct {
	ServiceIdentity  string   `json:"serviceIdentity"`
	ApprovedPeerRefs []string `json:"approvedPeerRefs"`
	TTLSeconds       int64    `json:"ttlSeconds"`
}

// ServiceSnapshotReceipt binds route renewal to the hub-accepted control frame.
type ServiceSnapshotReceipt struct {
	PublishedAt time.Time               `json:"publishedAt"`
	Generation  uint64                  `json:"generation"`
	Exports     []ApprovedServiceExport `json:"exports"`
}
