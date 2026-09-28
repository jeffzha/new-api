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

package controller

import (
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWriteLogExportRowIncludesOnlyAvailableFundingColumns(t *testing.T) {
	log := &model.Log{Username: "alice", CreatedAt: 1_758_000_000, ModelName: "deepseek", PromptTokens: 10, CompletionTokens: 5, Quota: 100}
	modelRatio, completionRatio, cacheRatio, cacheCreationRatio, groupRatio := 1.0, 2.0, 0.5, 0.25, 1.5
	row := logExportRow{
		Log:      log,
		Other:    logExportOther{QuotaPerUnit: 100, ModelRatio: &modelRatio, CompletionRatio: &completionRatio, CacheRatio: &cacheRatio, CacheCreationRatio: &cacheCreationRatio, GroupRatio: &groupRatio},
		Funding:  model.LogExportFunding{PaidQuota: 100},
		HasFunds: true,
	}
	var output strings.Builder
	writer := csv.NewWriter(&output)
	require.NoError(t, writeLogExportRow(writer, row, logExportFundingColumns{Paid: true}))
	writer.Flush()
	require.NoError(t, writer.Error())
	values, err := csv.NewReader(strings.NewReader(output.String())).Read()
	require.NoError(t, err)
	require.Len(t, values, 14)
	require.Equal(t, "21.90000000", values[7])
	require.Equal(t, "43.80000000", values[8])
	require.Equal(t, "7.30000000", values[13])
}

func TestGetConsumeLogsForExportLoadsFundingAcrossQueryBatches(t *testing.T) {
	dsn := "file:log-export-batches-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.AgencyFundingAllocation{}))

	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})

	logs := make([]model.Log, 502)
	allocations := make([]model.AgencyFundingAllocation, 502)
	for i := range logs {
		requestID := fmt.Sprintf("bill-request-%03d", i)
		userID := 1
		if i == 501 {
			requestID = "bill-request-000"
			userID = 2
		}
		logs[i] = model.Log{Type: model.LogTypeConsume, UserId: userID, RequestId: requestID, CreatedAt: int64(i + 1)}
		allocations[i] = model.AgencyFundingAllocation{
			ChargeID: requestID, ComponentID: "default", UserID: int64(userID), Consumed: 1, Version: 1,
		}
	}
	require.NoError(t, db.CreateInBatches(logs, 100).Error)
	require.NoError(t, db.CreateInBatches(allocations, 100).Error)

	exported, funding, err := model.GetConsumeLogsForExport(model.LogExportParams{IsAdmin: true})
	require.NoError(t, err)
	require.Len(t, exported, 502)
	require.Len(t, funding, 502)
	require.Equal(t, int64(1), funding[model.LogExportFundingKey{UserID: 1, RequestID: "bill-request-500"}].PaidQuota)
	require.Equal(t, int64(1), funding[model.LogExportFundingKey{UserID: 2, RequestID: "bill-request-000"}].PaidQuota)
}
