package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/marginreport"
	"github.com/QuantumNous/new-api/claw-control/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Registry owns the low-cardinality process metrics for claw-control. Database
// gauges are calculated at scrape time so restarts and Blue/Green overlap do
// not leave stale process-local values behind.
type Registry struct {
	db       *gorm.DB
	mu       sync.RWMutex
	counters map[string]uint64
}

func New(db *gorm.DB) *Registry {
	return &Registry{db: db, counters: make(map[string]uint64)}
}

func (r *Registry) IncControlEventFailure(stage string) {
	if stage != "publish" && stage != "database" {
		stage = "other"
	}
	r.inc("workbench_control_event_failures_total", "stage", stage)
}

func (r *Registry) ObserveHTTP(path string, status int) {
	result := "success"
	if status >= http.StatusBadRequest {
		result = "failure"
	}
	if strings.HasSuffix(path, "/app/verify") || (strings.Contains(path, "/apps/") && strings.HasSuffix(path, "/verify")) {
		r.inc("workbench_app_verify_total", "result", result)
	}
	if path == "/api/internal/workbench/identities/confirm" {
		r.inc("workbench_identity_bind_total", "result", result)
	}
	if status < http.StatusBadRequest {
		return
	}
	reason := "denied"
	if status >= http.StatusInternalServerError {
		reason = "error"
	}
	switch path {
	case "/api/internal/workbench/authz":
		r.inc("workbench_authz_denied_total", "reason", reason)
	case "/api/workbench/plan":
		r.inc("workbench_gate_failures_total", "gate", "plan", "reason", reason)
	case "/api/workbench/entry", "/api/workbench/config", "/api/internal/workbench/app-context":
		r.inc("workbench_gate_failures_total", "gate", "app", "reason", reason)
	}
}

func (r *Registry) inc(name string, labels ...string) {
	key := name
	for index := 0; index < len(labels); index += 2 {
		key += "\x00" + labels[index] + "\x00" + labels[index+1]
	}
	r.mu.Lock()
	r.counters[key]++
	r.mu.Unlock()
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/internal/metrics" {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if _, err := io.WriteString(w, r.Render(time.Now().UTC())); err != nil {
			return
		}
	})
}

func (r *Registry) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		r.ObserveHTTP(request.URL.Path, recorder.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (r *Registry) Render(now time.Time) string {
	var output strings.Builder
	writeFamilyHeader(&output, "workbench_active_sessions", "Active, selected, non-revoked claw-control browser sessions.", "gauge")
	writeFamilyHeader(&output, "workbench_customer_apps", "Customer Apps grouped into a bounded status set.", "gauge")
	writeFamilyHeader(&output, "workbench_control_event_lag_seconds", "Age of the oldest pending control event in seconds.", "gauge")
	writeFamilyHeader(&output, "workbench_usage_audit_stale_days", "Worst age in days of the latest locked usage audit across active Apps.", "gauge")
	writeFamilyHeader(&output, "workbench_usage_audit_missing_apps", "Active Apps with no locked usage audit.", "gauge")
	writeFamilyHeader(&output, "workbench_plan_expiring_total", "Paid plan periods ending within the configured day horizon.", "gauge")
	writeFamilyHeader(&output, "workbench_upstream_cost_cny", "Manually recorded upstream cost grouped by bounded allocation confidence.", "gauge")
	writeFamilyHeader(&output, "workbench_estimated_margin_cny", "Paid fixed-plan revenue minus locked reviewed upstream cost.", "gauge")

	r.renderDatabaseGauges(&output, now)
	r.renderCounters(&output)
	return output.String()
}

func (r *Registry) renderDatabaseGauges(output *strings.Builder, now time.Time) {
	if r.db == nil {
		r.renderCollectorFailure(output, "database")
		return
	}

	var activeSessions int64
	if err := r.db.Model(&model.ControlSession{}).
		Where("selection_state = ? AND revoked_at IS NULL AND expires_at > ?", model.ControlSessionStateSelected, now).
		Count(&activeSessions).Error; err != nil {
		r.renderCollectorFailure(output, "sessions")
	} else {
		fmt.Fprintf(output, "workbench_active_sessions %d\n", activeSessions)
	}

	type statusCount struct {
		Status string
		Count  int64
	}
	var appCounts []statusCount
	if err := r.db.Model(&model.CustomerApp{}).Select("status, COUNT(*) AS count").Group("status").Scan(&appCounts).Error; err != nil {
		r.renderCollectorFailure(output, "apps")
	} else {
		counts := map[string]int64{"draft": 0, "verified": 0, "active": 0, "suspended": 0, "disabled": 0, "archived": 0, "other": 0}
		for _, row := range appCounts {
			status := strings.ToLower(strings.TrimSpace(row.Status))
			if _, known := counts[status]; !known {
				status = "other"
			}
			counts[status] += row.Count
		}
		for _, status := range []string{"draft", "verified", "active", "suspended", "disabled", "archived", "other"} {
			fmt.Fprintf(output, "workbench_customer_apps{status=%s} %d\n", quote(status), counts[status])
		}
	}

	var oldestPending model.ControlOutbox
	query := r.db.Where("status = ?", model.OutboxStatusPending).Order("created_at asc").Limit(1).Find(&oldestPending)
	if query.Error == nil && query.RowsAffected > 0 {
		lag := math.Max(0, now.Sub(oldestPending.CreatedAt).Seconds())
		fmt.Fprintf(output, "workbench_control_event_lag_seconds{stage=%s} %s\n", quote("publish_queue"), formatFloat(lag))
	} else if query.Error == nil {
		fmt.Fprintf(output, "workbench_control_event_lag_seconds{stage=%s} 0\n", quote("publish_queue"))
	} else {
		r.renderCollectorFailure(output, "outbox")
	}

	var activeApps []model.CustomerApp
	query = r.db.Select("id", "created_at").Where("status = ?", model.AppStatusActive).Find(&activeApps)
	if query.Error != nil {
		r.renderCollectorFailure(output, "usage_audit")
	} else {
		type latestAudit struct {
			CustomerAppID uint64
			PeriodEnd     time.Time
		}
		latestByApp := make(map[uint64]time.Time, len(activeApps))
		auditCollectionOK := true
		if len(activeApps) > 0 {
			appIDs := make([]uint64, 0, len(activeApps))
			for _, app := range activeApps {
				appIDs = append(appIDs, app.ID)
			}
			var audits []latestAudit
			query = r.db.Model(&model.UsageAudit{}).
				Select("customer_app_id, MAX(period_end) AS period_end").
				Where("status = ? AND customer_app_id IN ?", model.UsageAuditStatusLocked, appIDs).
				Group("customer_app_id").Scan(&audits)
			if query.Error != nil {
				r.renderCollectorFailure(output, "usage_audit")
				auditCollectionOK = false
			} else {
				for _, audit := range audits {
					latestByApp[audit.CustomerAppID] = audit.PeriodEnd.UTC()
				}
			}
		}
		if auditCollectionOK {
			missing := 0
			worstStaleDays := 0.0
			for _, app := range activeApps {
				lastCoveredAt, found := latestByApp[app.ID]
				if !found {
					missing++
					lastCoveredAt = app.CreatedAt.UTC()
				}
				worstStaleDays = math.Max(worstStaleDays, math.Max(0, now.Sub(lastCoveredAt).Hours()/24))
			}
			fmt.Fprintf(output, "workbench_usage_audit_missing_apps %d\n", missing)
			if len(activeApps) > 0 {
				fmt.Fprintf(output, "workbench_usage_audit_stale_days %s\n", formatFloat(worstStaleDays))
			}
		}
	}

	for _, days := range []int{7, 30} {
		var count int64
		end := now.Add(time.Duration(days) * 24 * time.Hour)
		err := r.db.Model(&model.PlanPeriod{}).
			Where("payment_status = ? AND status = ? AND end_at > ? AND end_at <= ?", model.PaymentStatusPaid, model.PeriodStatusActive, now, end).
			Count(&count).Error
		if err != nil {
			r.renderCollectorFailure(output, "plans")
			break
		}
		fmt.Fprintf(output, "workbench_plan_expiring_total{days=%s} %d\n", quote(strconv.Itoa(days)), count)
	}

	report, err := marginreport.New(r.db).Build(marginreport.Command{})
	if err != nil {
		r.renderCollectorFailure(output, "cost")
	} else {
		costs := map[string]string{
			model.AllocationAppExact:            report.Breakdown.AppExactCostCNY,
			model.AllocationEstimatedAllocation: report.Breakdown.EstimatedAllocationCostCNY,
			model.AllocationAccountOnly:         report.Breakdown.AccountOnlyCostCNY,
			model.AllocationUnverified:          report.Breakdown.UnverifiedCostCNY,
		}
		valid := true
		for _, confidence := range []string{
			model.AllocationAppExact, model.AllocationEstimatedAllocation,
			model.AllocationAccountOnly, model.AllocationUnverified,
		} {
			amount, parseErr := decimal.NewFromString(costs[confidence])
			if parseErr != nil || amount.IsNegative() {
				valid = false
				break
			}
			fmt.Fprintf(output, "workbench_upstream_cost_cny{confidence=%s} %s\n", quote(confidence), amount.String())
		}
		margin, marginErr := decimal.NewFromString(report.MarginCNY)
		confidence := report.Confidence
		if _, known := costs[confidence]; !known {
			confidence = model.AllocationUnverified
		}
		if !valid || marginErr != nil {
			r.renderCollectorFailure(output, "cost")
		} else {
			fmt.Fprintf(output, "workbench_estimated_margin_cny{confidence=%s} %s\n", quote(confidence), margin.String())
		}
	}
}

func (r *Registry) renderCollectorFailure(output *strings.Builder, collector string) {
	r.inc("workbench_metrics_collection_failures_total", "collector", collector)
}

func (r *Registry) renderCounters(output *strings.Builder) {
	r.mu.RLock()
	rows := make([]string, 0, len(r.counters))
	for key, value := range r.counters {
		parts := strings.Split(key, "\x00")
		name := parts[0]
		labels := ""
		if len(parts) > 1 {
			pairs := make([]string, 0, (len(parts)-1)/2)
			for index := 1; index+1 < len(parts); index += 2 {
				pairs = append(pairs, parts[index]+"="+quote(parts[index+1]))
			}
			labels = "{" + strings.Join(pairs, ",") + "}"
		}
		rows = append(rows, fmt.Sprintf("%s%s %d\n", name, labels, value))
	}
	r.mu.RUnlock()
	sort.Strings(rows)

	writeFamilyHeader(output, "workbench_authz_denied_total", "Denied or failed continuous authorization checks.", "counter")
	writeFamilyHeader(output, "workbench_gate_failures_total", "App or plan access gate failures.", "counter")
	writeFamilyHeader(output, "workbench_control_event_failures_total", "Control event publish or persistence failures.", "counter")
	writeFamilyHeader(output, "workbench_metrics_collection_failures_total", "Failures while collecting database-backed metrics.", "counter")
	writeFamilyHeader(output, "workbench_app_verify_total", "Customer App verification attempts by result.", "counter")
	writeFamilyHeader(output, "workbench_identity_bind_total", "ADP shadow-account identity confirmations by result.", "counter")
	for _, row := range rows {
		output.WriteString(row)
	}
}

func writeFamilyHeader(output *strings.Builder, name, help, metricType string) {
	fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func quote(value string) string {
	value = strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
	return "\"" + value + "\""
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
