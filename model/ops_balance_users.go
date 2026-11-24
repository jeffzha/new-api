package model

import "context"

// Read only IDs in indexed bounded pages; never import the product wallet.
func ListOPSBalanceUserIDs(ctx context.Context, after, limit int) ([]int, error) {
	var ids []int
	err := DB.WithContext(ctx).Model(&User{}).Where("id > ?", after).Order("id ASC").Limit(min(max(limit, 1), 500)).Pluck("id", &ids).Error
	return ids, err
}
