package model

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// LogTypeQuotaAdjustment is a query/display category, never a persisted log type.
const LogTypeQuotaAdjustment = 8

// logCategorySQL keeps counts, pagination and display consistent without rewriting
// legacy type=1 rows or changing the raw type consumed by accounting integrations.
func logCategorySQL() string {
	jsonText := "COALESCE(NULLIF(logs.other, ''), '{}')"
	field := func(path ...string) string {
		switch common.LogDatabaseType() {
		case common.DatabaseTypePostgreSQL:
			return fmt.Sprintf("COALESCE((%s)::json #>> '{%s}', '')", jsonText, strings.Join(path, ","))
		case common.DatabaseTypeMySQL:
			return fmt.Sprintf("COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(%s), %s, '{}'), '$.%s')), 'null'), '')", jsonText, jsonText, strings.Join(path, "."))
		case common.DatabaseTypeClickHouse:
			return fmt.Sprintf("JSONExtractString(%s, '%s')", jsonText, strings.Join(path, "', '"))
		default:
			return fmt.Sprintf("COALESCE(json_extract(CASE WHEN json_valid(%s) THEN %s ELSE '{}' END, '$.%s'), '')", jsonText, jsonText, strings.Join(path, "."))
		}
	}
	source, action := field("quota_source"), field("op", "action")
	return fmt.Sprintf(
		"CASE WHEN logs.type NOT IN (1, 3) THEN logs.type "+
			"WHEN logs.type = 3 AND %s IN ('user.quota_add', 'user.quota_subtract', 'user.quota_override') THEN 8 "+
			"WHEN logs.type <> 1 THEN logs.type "+
			"WHEN %s IN ('online_topup', 'admin_topup') THEN 1 "+
			"WHEN %s IN ('admin_adjustment', 'redemption') THEN 8 "+
			"WHEN %s <> '' THEN 0 "+
			"WHEN %s IN ('user.quota_add', 'user.quota_subtract', 'user.quota_override') THEN 8 "+
			"WHEN logs.content LIKE '通过兑换码充值%%' "+
			"OR logs.content LIKE 'Increased user quota by %%' "+
			"OR logs.content LIKE 'Decreased user quota by %%' "+
			"OR logs.content LIKE 'Overrode user quota from %%' THEN 8 "+
			"WHEN logs.content LIKE '使用在线充值成功%%' "+
			"OR logs.content LIKE '管理员补单成功%%' "+
			"OR logs.content LIKE '使用Creem充值成功%%' "+
			"OR logs.content LIKE 'Waffo充值成功%%' "+
			"OR logs.content LIKE 'Waffo Pancake充值成功%%' THEN 1 ELSE 0 END",
		action, source, source, source, action)
}

func applyLogTypeFilter(tx *gorm.DB, logType int) *gorm.DB {
	switch logType {
	case LogTypeUnknown:
		return tx
	case LogTypeTopup:
		return tx.Where("logs.type = ?", LogTypeTopup).Where("("+logCategorySQL()+") = ?", logType)
	case LogTypeQuotaAdjustment:
		return tx.Where("logs.type IN ?", []int{LogTypeTopup, LogTypeManage}).Where("("+logCategorySQL()+") = ?", logType)
	case LogTypeManage:
		return tx.Where("logs.type = ?", logType).Where("("+logCategorySQL()+") = ?", logType)
	default:
		return tx.Where("logs.type = ?", logType)
	}
}
