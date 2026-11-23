package api

import "github.com/re8ch/among-clusters/internal/model"

// Legacy agents discard unknown JSON fields. Never let an exact-name grant
// become a namespace-wide role just because its agent predates resourceNames.
func grantSnapshotForVersion(grants []model.GrantInstruction, v2 bool) *model.GrantSnapshot {
	result := &model.GrantSnapshot{Grants: make([]model.GrantInstruction, 0, len(grants)), ProtocolVersion: 1}
	if v2 {
		result.ProtocolVersion = 2
	}
	for _, grant := range grants {
		if !v2 {
			for _, rule := range grant.Rules {
				if len(rule.ResourceNames) > 0 {
					// Also remove any previously installed broader role and broker
					// credential after an agent downgrade or a grant being narrowed.
					// This is an instruction projection, not a change to owner intent.
					grant.Revoked = true
					grant.Rules = nil
					break
				}
			}
		}
		result.Grants = append(result.Grants, grant)
	}
	return result
}
