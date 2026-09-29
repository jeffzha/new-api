/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package model

import (
	"errors"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"gorm.io/gorm"
)

const LogExportMaxRows = 100000
const logExportFundingBatchSize = 500

var ErrLogExportTooManyRows = errors.New("导出记录超过上限，请缩小时间范围或增加筛选条件")

// LogExportFunding contains the durable source split for one consume log.
// It is empty when the request predates source allocation tracking or was not
// billed through the agency wallet ledger.
type LogExportFunding struct {
	PaidQuota    int64
	NonpaidQuota int64
	DebtQuota    int64
}

type LogExportFundingKey struct {
	UserID    int64
	RequestID string
}

// LogExportAgencyPricing is the frozen customer-facing price for one charge.
// SettlementBPS is deliberately excluded because it is an agency cost, not a
// customer discount.
type LogExportAgencyPricing struct {
	StandardQuota     int64
	ChargedTotalQuota int64
	SalesBPS          int
	QuotaPerUnit      string
	ExchangeRate      string
}

type logExportSourceSnapshot struct {
	SourceKind string `json:"source_kind"`
}

type LogExportParams struct {
	UserID            int
	IsAdmin           bool
	StartTimestamp    int64
	EndTimestamp      int64
	ModelName         string
	Username          string
	TokenName         string
	Channel           int
	Group             string
	RequestID         string
	UpstreamRequestID string
}

func buildConsumeLogQuery(params LogExportParams) (*gorm.DB, error) {
	tx := LOG_DB.Where("logs.type = ?", LogTypeConsume)
	if !params.IsAdmin {
		tx = tx.Where("logs.user_id = ?", params.UserID)
	} else if params.Username != "" {
		var err error
		tx, err = applyExplicitLogTextFilter(tx, "logs.username", params.Username)
		if err != nil {
			return nil, err
		}
	}
	if params.ModelName != "" {
		var err error
		tx, err = applyExplicitLogTextFilter(tx, "logs.model_name", params.ModelName)
		if err != nil {
			return nil, err
		}
	}
	if params.TokenName != "" {
		tx = tx.Where("logs.token_name = ?", params.TokenName)
	}
	if params.Channel != 0 {
		tx = tx.Where("logs.channel_id = ?", params.Channel)
	}
	if params.Group != "" {
		tx = tx.Where("logs."+logGroupCol+" = ?", params.Group)
	}
	if params.RequestID != "" {
		tx = tx.Where("logs.request_id = ?", params.RequestID)
	}
	if params.UpstreamRequestID != "" {
		tx = tx.Where("logs.upstream_request_id = ?", params.UpstreamRequestID)
	}
	if params.StartTimestamp != 0 {
		tx = tx.Where("logs.created_at >= ?", params.StartTimestamp)
	}
	if params.EndTimestamp != 0 {
		tx = tx.Where("logs.created_at <= ?", params.EndTimestamp)
	}
	return tx, nil
}

func GetConsumeLogsForExport(params LogExportParams) ([]*Log, map[LogExportFundingKey]LogExportFunding, map[LogExportFundingKey]LogExportAgencyPricing, error) {
	if LOG_DB == nil {
		return nil, nil, nil, errors.New("log database is not initialized")
	}
	tx, err := buildConsumeLogQuery(params)
	if err != nil {
		return nil, nil, nil, err
	}
	var total int64
	if err := tx.Model(&Log{}).Count(&total).Error; err != nil {
		return nil, nil, nil, err
	}
	if total > LogExportMaxRows {
		return nil, nil, nil, ErrLogExportTooManyRows
	}
	order := "logs.created_at asc, logs.id asc"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = "logs.created_at asc, logs.request_id asc"
	}
	var logs []*Log
	if err := tx.Order(order).Limit(LogExportMaxRows).Find(&logs).Error; err != nil {
		return nil, nil, nil, err
	}

	funding := make(map[LogExportFundingKey]LogExportFunding)
	agencyPricing := make(map[LogExportFundingKey]LogExportAgencyPricing)
	if DB == nil || len(logs) == 0 {
		return logs, funding, agencyPricing, nil
	}
	chargeIDs := make([]string, 0, len(logs))
	seen := make(map[string]struct{}, len(logs))
	for _, log := range logs {
		if strings.TrimSpace(log.RequestId) == "" {
			continue
		}
		if _, exists := seen[log.RequestId]; exists {
			continue
		}
		seen[log.RequestId] = struct{}{}
		chargeIDs = append(chargeIDs, log.RequestId)
	}
	if len(chargeIDs) == 0 {
		return logs, funding, agencyPricing, nil
	}
	if DB.Migrator().HasTable(&AgencyFundingAllocation{}) {
		allocations := make([]AgencyFundingAllocation, 0)
		for start := 0; start < len(chargeIDs); start += logExportFundingBatchSize {
			end := min(start+logExportFundingBatchSize, len(chargeIDs))
			var batch []AgencyFundingAllocation
			if err := DB.Where("charge_id IN ?", chargeIDs[start:end]).Find(&batch).Error; err != nil {
				return nil, nil, nil, err
			}
			allocations = append(allocations, batch...)
		}
		for _, allocation := range allocations {
			paid, nonpaid, debt, err := agencyFundingAllocationActiveParts(allocation)
			if err != nil {
				continue
			}
			if paid <= 0 && nonpaid <= 0 && debt <= 0 {
				continue
			}
			key := LogExportFundingKey{UserID: allocation.UserID, RequestID: allocation.ChargeID}
			current := funding[key]
			current.PaidQuota += paid
			var source logExportSourceSnapshot
			if common.UnmarshalJsonStr(allocation.SourceSnapshotJSON, &source) == nil && isRedemptionSource(source.SourceKind) {
				current.NonpaidQuota += nonpaid
			}
			current.DebtQuota += debt
			funding[key] = current
		}
	}

	if DB.Migrator().HasTable(&AgencyBillingOperation{}) {
		invalidPricing := make(map[LogExportFundingKey]struct{})
		for start := 0; start < len(chargeIDs); start += logExportFundingBatchSize {
			end := min(start+logExportFundingBatchSize, len(chargeIDs))
			var operations []AgencyBillingOperation
			if err := DB.Where("charge_id IN ? AND operation = ?", chargeIDs[start:end], "finalize").Find(&operations).Error; err != nil {
				return nil, nil, nil, err
			}
			for _, operation := range operations {
				var event agencycontract.BillingEvent
				if common.UnmarshalJsonStr(operation.CommittedResult, &event) != nil || event.UserID <= 0 ||
					event.FinancialChargeID != operation.ChargeID || event.StandardQuota < 0 ||
					event.ChargedTotalQuota < 0 || event.SalesBPS < 0 || event.SalesBPS > agencycontract.MaxCoefficientBPS {
					continue
				}
				key := LogExportFundingKey{UserID: event.UserID, RequestID: operation.ChargeID}
				if _, invalid := invalidPricing[key]; invalid {
					continue
				}
				current, exists := agencyPricing[key]
				if exists && (current.SalesBPS != event.SalesBPS || current.QuotaPerUnit != event.QuotaPerUnit || current.ExchangeRate != event.ExchangeRate) {
					delete(agencyPricing, key)
					invalidPricing[key] = struct{}{}
					continue
				}
				if event.StandardQuota > math.MaxInt64-current.StandardQuota ||
					event.ChargedTotalQuota > math.MaxInt64-current.ChargedTotalQuota {
					delete(agencyPricing, key)
					invalidPricing[key] = struct{}{}
					continue
				}
				current.StandardQuota += event.StandardQuota
				current.ChargedTotalQuota += event.ChargedTotalQuota
				current.SalesBPS = event.SalesBPS
				current.QuotaPerUnit = event.QuotaPerUnit
				current.ExchangeRate = event.ExchangeRate
				agencyPricing[key] = current
			}
		}
	}
	return logs, funding, agencyPricing, nil
}
