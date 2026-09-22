package agencycontract

import "errors"

// TierNode is one immutable root-to-leaf cost boundary. SalesBPS is used only
// by the leaf that directly serves the customer.
type TierNode struct {
	AgencyID     int64
	CostBPS      int
	MinSpreadBPS int
	SalesBPS     *int
}

type TierCommissionSegment struct {
	AgencyID           int64
	TheoreticalQuota   int64
	PaidAllocatedQuota int64
	CommissionQuota    int64
}

type TierCommissionResult struct {
	CustomerChargedQuota int64
	PlatformCostQuota    int64
	PlatformMarginQuota  int64
	Segments             []TierCommissionSegment
	TotalCommissionQuota int64
}

// CalculateTieredCommission computes the conserved commission split for a
// root-to-leaf chain. Nodes must be ordered root to leaf. Each parent keeps
// the difference between its child's cost and its own cost; the leaf keeps
// sales minus its cost. Platform cost is the root-side lower boundary.
func CalculateTieredCommission(standardQuota int64, nodes []TierNode, platformCostBPS, minSpreadBPS int, paidAllocatedQuota int64, round bool) (TierCommissionResult, error) {
	if standardQuota < 0 || platformCostBPS < 0 || minSpreadBPS < 0 || paidAllocatedQuota < 0 || len(nodes) == 0 || paidAllocatedQuota > standardQuota {
		return TierCommissionResult{}, errors.New("invalid tiered commission inputs")
	}
	if platformCostBPS > MaxCoefficientBPS || minSpreadBPS > MaxCoefficientBPS {
		return TierCommissionResult{}, errors.New("invalid tiered commission coefficient")
	}
	effectiveSpread := minSpreadBPS
	for _, node := range nodes {
		if node.MinSpreadBPS > effectiveSpread {
			effectiveSpread = node.MinSpreadBPS
		}
	}
	for i, node := range nodes {
		if node.AgencyID <= 0 || node.CostBPS < 0 || node.CostBPS > MaxCoefficientBPS {
			return TierCommissionResult{}, errors.New("invalid tier node")
		}
		if i > 0 && node.CostBPS < nodes[i-1].CostBPS+effectiveSpread {
			return TierCommissionResult{}, errors.New("tier cost spread is below minimum")
		}
		if i < len(nodes)-1 && node.SalesBPS != nil {
			return TierCommissionResult{}, errors.New("only leaf tier may define sales coefficient")
		}
	}
	leaf := nodes[len(nodes)-1]
	if leaf.SalesBPS == nil || *leaf.SalesBPS < leaf.CostBPS+effectiveSpread || *leaf.SalesBPS > MaxCoefficientBPS {
		return TierCommissionResult{}, errors.New("leaf sales coefficient is invalid")
	}
	charged, err := applyBPS(standardQuota, *leaf.SalesBPS, round)
	if err != nil {
		return TierCommissionResult{}, err
	}
	platformCost, err := applyBPS(standardQuota, platformCostBPS, round)
	if err != nil {
		return TierCommissionResult{}, err
	}
	if platformCost > charged {
		return TierCommissionResult{}, errors.New("platform cost exceeds customer charge")
	}
	rootCost, err := applyBPS(standardQuota, nodes[0].CostBPS, round)
	if err != nil {
		return TierCommissionResult{}, err
	}
	if rootCost < platformCost {
		return TierCommissionResult{}, errors.New("platform cost exceeds root agency cost")
	}
	platformMargin := rootCost - platformCost
	segments := make([]TierCommissionSegment, 0, len(nodes))
	allocated := int64(0)
	for i, node := range nodes {
		var quota int64
		if i == len(nodes)-1 {
			leafCost, leafErr := applyBPS(standardQuota, node.CostBPS, round)
			if leafErr != nil {
				return TierCommissionResult{}, leafErr
			}
			quota = charged - leafCost
		} else {
			quota, err = applyBPS(standardQuota, nodes[i+1].CostBPS-node.CostBPS, round)
			if err != nil {
				return TierCommissionResult{}, err
			}
		}
		if quota < 0 {
			return TierCommissionResult{}, errors.New("negative tier commission")
		}
		paid, err := CommissionForPaid(quota, paidAllocatedQuota, charged, round)
		if err != nil {
			return TierCommissionResult{}, err
		}
		segments = append(segments, TierCommissionSegment{AgencyID: node.AgencyID, TheoreticalQuota: quota, PaidAllocatedQuota: paid, CommissionQuota: paid})
		allocated += quota
	}
	if allocated+platformMargin+platformCost != charged {
		return TierCommissionResult{}, errors.New("tier commission conservation failed")
	}
	return TierCommissionResult{CustomerChargedQuota: charged, PlatformCostQuota: platformCost, PlatformMarginQuota: platformMargin, Segments: segments, TotalCommissionQuota: allocated}, nil
}
