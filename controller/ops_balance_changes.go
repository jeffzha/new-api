package controller

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/QuantumNous/new-api/service/opswallet"
	"github.com/gin-gonic/gin"
)

func manageOPSBalance(c *gin.Context, req ManageRequest) bool {
	if !opswallet.Enabled() {
		return false
	}
	user, err := model.GetUserById(req.Id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该用户不存在，请刷新后重试", "retry_same_operation": false})
		return true
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无权调整该用户余额", "retry_same_operation": false})
		return true
	}
	operation := req.Mode
	if operation == "override" {
		operation = "set"
	}
	a := opswallet.Adjustment{RequestID: req.RequestID, UserID: strconv.Itoa(user.Id), Username: user.Username, Operation: operation, Amount: req.OPSAmount, Operator: c.GetString("username"), Reason: req.Reason}
	if err := opswallet.Validate(a); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error(), "retry_same_operation": false})
		return true
	}
	client := opsclient.New(os.Getenv("OPS_BILLING_URL"), os.Getenv("OPS_BILLING_KEY_ID"), os.Getenv("OPS_BILLING_SECRET"), 8*time.Second)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	result, err := opswallet.Adjust(ctx, client, a)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "code": "ops_balance_adjustment_unconfirmed", "message": opswallet.ErrorMessage(err), "request_id": req.RequestID, "retry_same_operation": !opswallet.CanEditAfterError(err)})
		return true
	}
	// The OPS ledger is the financial audit. No local quota/top-up log is
	// emitted, so the usage pusher cannot credit this money a second time.
	recordManageAuditFor(c, user.Id, "user.ops_balance_change", map[string]any{"request_id": req.RequestID, "operation": operation, "amount_cny": result.Amount, "replayed": result.Replayed, "ledger_entry_id": result.LedgerEntryID})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
	return true
}
