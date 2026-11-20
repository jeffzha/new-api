package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// The same schema drives all four formats so a type cannot silently export
// consumption columns or totals in a different file format.
type logExportSchema struct {
	Title, Notice string
	Headers       []string
	Widths        []float64
	SummaryColumn int
}

func logExportSchemaFor(logType int) logExportSchema {
	schema := logExportSchema{Title: "按量消费明细", Notice: logExportBillingNotice, Headers: logExportHeaders, Widths: logExportColumnWidths, SummaryColumn: 12}
	switch logType {
	case model.LogTypeTopup:
		schema.Title = "充值明细"
		schema.Notice = "仅包含用户直充和代充；实付金额及币种来自支付凭据，历史缺失显示未记录。到账额度为原始额度单位，不等同于实付人民币。"
		schema.Headers = []string{"用户名称", "用户ID", "时间", "充值来源", "商户订单号", "支付方式", "实付金额", "币种", "到账额度（原始单位）", "说明"}
	case model.LogTypeQuotaAdjustment:
		schema.Title = "额度调整明细"
		schema.Notice = "包含管理员额度调整和兑换码兑换，含可识别的历史记录。额度为原始单位；未记录的方向及调整前后额度不作推算。"
		schema.Headers = []string{"用户名称", "用户ID", "时间", "调整来源", "增减方向", "变动额度（原始单位）", "调整前额度（原始单位）", "调整后额度（原始单位）", "请求ID", "说明"}
	case model.LogTypeSystem:
		schema.Title = "系统日志明细"
		schema.Notice = "仅导出所选范围内的系统记录；系统日志不作为消费或充值金额凭据。"
		schema.Headers = []string{"用户名称", "用户ID", "时间", "请求ID", "说明"}
	case model.LogTypeError:
		schema.Title = "错误日志明细"
		schema.Notice = "错误记录不等同于扣费记录，不汇总为消费金额。"
		schema.Headers = []string{"用户名称", "用户ID", "时间", "模型名称", "API Key名称", "分组", "请求ID", "上游请求ID", "接口", "HTTP状态码", "错误码", "错误类型", "耗时（秒）", "错误详情"}
	case model.LogTypeRefund:
		schema.Title = "退款明细"
		schema.Notice = "退款为调用额度退回，不代表支付渠道现金退款；原始额度与充值支付金额不可直接相加。"
		schema.Headers = []string{"用户名称", "用户ID", "时间", "模型名称", "退回额度（原始单位）", "任务ID", "请求ID", "上游请求ID", "退款说明"}
	default:
		return schema
	}
	schema.SummaryColumn = len(schema.Headers)
	schema.Widths = make([]float64, len(schema.Headers))
	for i := range schema.Widths {
		schema.Widths[i] = 22
	}
	schema.Widths[len(schema.Widths)-1] = 60
	return schema
}

func (schema logExportSchema) summary(label string, amount float64) []string {
	values := make([]string, len(schema.Headers))
	values[0] = label
	if schema.SummaryColumn < len(values) {
		values[schema.SummaryColumn] = fmt.Sprintf("%.8f", amount)
	} else {
		values[0] = fmt.Sprintf("%s：%.0f 条", label, amount)
	}
	return values
}

func (schema logExportSchema) totalLabel() string {
	if schema.SummaryColumn == 12 && schema.Title == "按量消费明细" {
		return "本期消费金额"
	}
	return "本期记录数"
}

func (schema logExportSchema) totalText(total float64) string {
	if schema.totalLabel() == "本期消费金额" {
		return fmt.Sprintf("本期消费金额：%.8f元", total)
	}
	return fmt.Sprintf("本期记录数：%.0f 条", total)
}

type logExportEventOther struct {
	Source    string `json:"quota_source"`
	Direction string `json:"quota_direction"`
	TradeNo   string `json:"trade_no"`
	TaskID    string `json:"task_id"`
	Reason    string `json:"reason"`
	Path      string `json:"request_path"`
	Status    *int   `json:"status_code"`
	ErrorCode string `json:"error_code"`
	ErrorType string `json:"error_type"`
	Op        struct {
		Action string `json:"action"`
		Params struct {
			From *int64 `json:"from"`
			To   *int64 `json:"to"`
		} `json:"params"`
	} `json:"op"`
}

func logExportEventValues(logType int, row logExportRow) []string {
	if logType == 0 || logType == model.LogTypeConsume {
		return logExportRowValues(row)
	}
	log := row.Log
	var other logExportEventOther
	_ = common.UnmarshalJsonStr(log.Other, &other)
	values := []string{log.Username, strconv.Itoa(log.UserId), time.Unix(log.CreatedAt, 0).Format("2006-01-02 15:04:05")}
	source := "未记录"
	switch other.Source {
	case model.LogQuotaSourceOnlineTopup:
		source = "用户直充"
	case model.LogQuotaSourceAdminTopup:
		source = "管理员补单"
	case model.LogQuotaSourceAdminAdjustment:
		source = "管理员调整"
	case model.LogQuotaSourceRedemption:
		source = "兑换码兑换"
	}
	switch logType {
	case model.LogTypeTopup:
		method, amount, currency := "未记录", "未记录", "未记录"
		if other.Source == "" {
			if strings.HasPrefix(log.Content, "管理员补单成功") {
				source = "管理员补单"
			} else if strings.HasPrefix(log.Content, "使用在线充值成功") || strings.HasPrefix(log.Content, "使用Creem充值成功") || strings.HasPrefix(log.Content, "Waffo充值成功") || strings.HasPrefix(log.Content, "Waffo Pancake充值成功") {
				source = "用户直充"
			}
		}
		if row.Topup != nil {
			method = row.Topup.PaymentMethod
			switch row.Topup.FundingSource {
			case "payment_self":
				source = "用户直充"
			case "payment_assisted":
				source = "代充"
			}
			var payment model.TopupPaymentSnapshot
			if common.UnmarshalJsonStr(row.Topup.PaymentSnapshot, &payment) == nil && payment.ActualMoney != "" && payment.CurrencyCode != "" {
				amount, currency = payment.ActualMoney, payment.CurrencyCode
			}
		}
		quota := "未记录"
		if other.Source != "" || log.Quota != 0 {
			quota = strconv.Itoa(log.Quota)
		}
		values = append(values, source, other.TradeNo, method, amount, currency, quota, log.Content)
	case model.LogTypeQuotaAdjustment:
		direction, before, after := "未记录", "未记录", "未记录"
		if other.Direction == "credit" || other.Op.Action == "user.quota_add" || strings.HasPrefix(log.Content, "Increased user quota by ") {
			direction = "增加"
		} else if other.Direction == "debit" || other.Op.Action == "user.quota_subtract" || strings.HasPrefix(log.Content, "Decreased user quota by ") {
			direction = "减少"
		}
		if other.Source == model.LogQuotaSourceRedemption || (other.Source == "" && strings.HasPrefix(log.Content, "通过兑换码充值")) {
			source, direction = "兑换码兑换", "增加"
		} else if strings.HasPrefix(other.Op.Action, "user.quota_") || strings.HasPrefix(log.Content, "Increased user quota by ") || strings.HasPrefix(log.Content, "Decreased user quota by ") || strings.HasPrefix(log.Content, "Overrode user quota from ") {
			source = "管理员调整"
		}
		if other.Op.Params.From != nil {
			before = strconv.FormatInt(*other.Op.Params.From, 10)
		}
		if other.Op.Params.To != nil {
			after = strconv.FormatInt(*other.Op.Params.To, 10)
		}
		if other.Op.Params.From != nil && other.Op.Params.To != nil {
			direction = "不变"
			if *other.Op.Params.To > *other.Op.Params.From {
				direction = "增加"
			} else if *other.Op.Params.To < *other.Op.Params.From {
				direction = "减少"
			}
		}
		quota := "未记录"
		if other.Source != "" || log.Quota != 0 {
			quota = strconv.Itoa(log.Quota)
		}
		values = append(values, source, direction, quota, before, after, log.RequestId, log.Content)
	case model.LogTypeSystem:
		values = append(values, log.RequestId, log.Content)
	case model.LogTypeError:
		status := "未记录"
		if other.Status != nil {
			status = strconv.Itoa(*other.Status)
		}
		values = append(values, log.ModelName, log.TokenName, log.Group, log.RequestId, log.UpstreamRequestId, other.Path, status, other.ErrorCode, other.ErrorType, strconv.Itoa(log.UseTime), log.Content)
	case model.LogTypeRefund:
		reason := log.Content
		if reason == "" {
			reason = other.Reason
		}
		values = append(values, log.ModelName, strconv.Itoa(log.Quota), other.TaskID, log.RequestId, log.UpstreamRequestId, reason)
	}
	for i := range values {
		values[i] = logExportCSVSafe(values[i])
	}
	return values
}

func logExportBatchRows(ctx context.Context, logType int, logs []*model.Log, funds map[model.LogExportFundingKey]model.LogExportFunding, prices map[model.LogExportFundingKey]model.LogExportAgencyPricing) ([]logExportRow, error) {
	var topups map[model.LogExportFundingKey]model.TopUp
	if logType == model.LogTypeTopup {
		var err error
		topups, err = model.LoadLogExportTopups(ctx, logs)
		if err != nil {
			return nil, err
		}
	}
	rows := make([]logExportRow, 0, len(logs))
	for _, log := range logs {
		other, parsed := parseLogExportOther(log)
		if !parsed {
			other = logExportOther{}
		}
		key := model.LogExportFundingKey{UserID: int64(log.UserId), RequestID: log.RequestId}
		funding, hasFunding := funds[key]
		price, hasPrice := prices[key]
		row := logExportRow{Log: log, Other: other, Funding: funding, HasFunds: hasFunding, AgencyPricing: price, HasAgencyPricing: hasPrice}
		if topups != nil {
			var event logExportEventOther
			_ = common.UnmarshalJsonStr(log.Other, &event)
			if topup, ok := topups[model.LogExportFundingKey{UserID: int64(log.UserId), RequestID: event.TradeNo}]; ok {
				row.Topup = &topup
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
