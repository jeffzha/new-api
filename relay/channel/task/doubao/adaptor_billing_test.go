package doubao

import (
	"testing"

	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdjustBillingOnCompleteUsesOfficialPriceTable(t *testing.T) {
	oldQuotaPerUnit := appcommon.QuotaPerUnit
	appcommon.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		appcommon.QuotaPerUnit = oldQuotaPerUnit
	})

	task := &model.Task{}
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		OriginModelName: "doubao-seedance-2-0-filter-off",
		ModelRatio:      999,
		GroupRatio:      1,
		OtherRatios: map[string]float64{
			videoInputRatioKey: 4.3 / 7.0,
		},
	}
	taskResult := &relaycommon.TaskInfo{TotalTokens: 1_000_000}

	got := (&TaskAdaptor{}).AdjustBillingOnComplete(task, taskResult)

	require.Positive(t, got)
	assert.Equal(t, 2_150_000, got)
}

func TestAdjustBillingOnCompleteAppliesGroupRatio(t *testing.T) {
	oldQuotaPerUnit := appcommon.QuotaPerUnit
	appcommon.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		appcommon.QuotaPerUnit = oldQuotaPerUnit
	})

	task := &model.Task{}
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		OriginModelName: "doubao-seedance-2-0-fast-filter-off",
		GroupRatio:      0.5,
	}
	taskResult := &relaycommon.TaskInfo{TotalTokens: 1_000_000}

	got := (&TaskAdaptor{}).AdjustBillingOnComplete(task, taskResult)

	assert.Equal(t, 1_400_000, got)
}

func TestAdjustBillingOnCompleteUsesSeedance25Published1080pPrice(t *testing.T) {
	oldQuotaPerUnit := appcommon.QuotaPerUnit
	appcommon.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		appcommon.QuotaPerUnit = oldQuotaPerUnit
	})

	ratio, ok := GetVideoInputRatio(seedance25Model, "1080p", true)
	require.True(t, ok)
	task := &model.Task{}
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		OriginModelName: seedance25Model,
		GroupRatio:      1,
		OtherRatios: map[string]float64{
			videoInputRatioKey: ratio,
		},
	}
	taskResult := &relaycommon.TaskInfo{TotalTokens: 1_000_000}

	got := (&TaskAdaptor{}).AdjustBillingOnComplete(task, taskResult)

	assert.Equal(t, appcommon.QuotaRound(7.00*500_000), got)
}

func TestAdjustBillingOnCompleteRoundsSupplierUsageExactly(t *testing.T) {
	oldQuotaPerUnit := appcommon.QuotaPerUnit
	appcommon.QuotaPerUnit = 500_000
	t.Cleanup(func() {
		appcommon.QuotaPerUnit = oldQuotaPerUnit
	})

	task := &model.Task{}
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		OriginModelName: "doubao-seedance-2-0-fast-filter-off",
		GroupRatio:      1,
	}
	taskResult := &relaycommon.TaskInfo{TotalTokens: 48_400}

	got := (&TaskAdaptor{}).AdjustBillingOnComplete(task, taskResult)

	assert.Equal(t, 135_520, got)
}

func TestAdjustBillingOnCompleteFallsBackForUnsupportedCases(t *testing.T) {
	tests := []struct {
		name       string
		task       *model.Task
		taskResult *relaycommon.TaskInfo
	}{
		{
			name: "nil task",
		},
		{
			name:       "nil task result",
			task:       &model.Task{},
			taskResult: nil,
		},
		{
			name: "missing billing context",
			task: &model.Task{},
			taskResult: &relaycommon.TaskInfo{
				TotalTokens: 1_000_000,
			},
		},
		{
			name: "zero total tokens",
			task: &model.Task{
				PrivateData: model.TaskPrivateData{
					BillingContext: &model.TaskBillingContext{
						OriginModelName: "doubao-seedance-2-0-filter-off",
						GroupRatio:      1,
					},
				},
			},
			taskResult: &relaycommon.TaskInfo{},
		},
		{
			name: "legacy model keeps generic completion billing",
			task: &model.Task{
				PrivateData: model.TaskPrivateData{
					BillingContext: &model.TaskBillingContext{
						OriginModelName: "doubao-seedance-2-0-260128",
						GroupRatio:      1,
						OtherRatios: map[string]float64{
							videoInputRatioKey: 28.0 / 46.0,
						},
					},
				},
			},
			taskResult: &relaycommon.TaskInfo{
				TotalTokens: 1_000_000,
			},
		},
		{
			name: "invalid group ratio",
			task: &model.Task{
				PrivateData: model.TaskPrivateData{
					BillingContext: &model.TaskBillingContext{
						OriginModelName: "doubao-seedance-2-0-filter-off",
					},
				},
			},
			taskResult: &relaycommon.TaskInfo{
				TotalTokens: 1_000_000,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&TaskAdaptor{}).AdjustBillingOnComplete(tt.task, tt.taskResult)
			assert.Zero(t, got)
		})
	}
}
