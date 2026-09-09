package opsmonitor

import (
	"math"

	"github.com/QuantumNous/new-api/model"
)

func computeHealthScore(overview DashboardOverview, latency LatencySummary, systems []model.OpsSystemMetric, jobs []JobHealth) HealthScore {
	if overview.RequestCount == 0 {
		return HealthScore{State: "idle", Score: 100, BusinessScore: 100, InfraScore: 100, ErrorScore: 100, TTFTScore: 100, StorageScore: 100, ComputeScore: 100, JobScore: 100}
	}
	errorPct := math.Max(overview.RequestErrorRate, overview.UpstreamErrorRate) * 100
	errorScore := linearDescendingScore(errorPct, 1, 10)
	ttftScore := 100.0
	if latency.TTFT.Samples > 0 {
		ttftScore = linearDescendingScore(float64(latency.TTFT.P99), 1000, 3000)
	}
	business := errorScore*0.5 + ttftScore*0.5
	storage := 100.0
	compute := 100.0
	for _, system := range systems {
		if !system.DBHealthy {
			storage = 0
		}
		if storage > 0 && system.RedisEnabled && !system.RedisHealthy && storage > 50 {
			storage = 50
		}
		cpuScore := linearDescendingScore(system.CPUPercent, 80, 100)
		memoryScore := linearDescendingScore(system.MemoryPercent, 85, 100)
		nodeCompute := (cpuScore + memoryScore) / 2
		if nodeCompute < compute {
			compute = nodeCompute
		}
	}
	jobScore := 100.0
	if len(jobs) > 0 {
		failed := 0
		for _, job := range jobs {
			if job.Status != "healthy" {
				failed++
			}
		}
		jobScore = 100 * (1 - float64(failed)/float64(len(jobs)))
	}
	infra := storage*0.4 + compute*0.3 + jobScore*0.3
	score := int(math.Round(business*0.7 + infra*0.3))
	state := "healthy"
	if score < 60 {
		state = "critical"
	} else if score < 90 {
		state = "warning"
	}
	return HealthScore{State: state, Score: score, BusinessScore: business, InfraScore: infra, ErrorScore: errorScore, TTFTScore: ttftScore, StorageScore: storage, ComputeScore: compute, JobScore: jobScore}
}

func linearDescendingScore(value, healthyMax, criticalMin float64) float64 {
	if value <= healthyMax {
		return 100
	}
	if value >= criticalMin {
		return 0
	}
	return (criticalMin - value) / (criticalMin - healthyMax) * 100
}
