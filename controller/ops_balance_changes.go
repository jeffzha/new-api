package controller

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/service/opsbilling"
	"github.com/QuantumNous/new-api/service/opsclient"
	"github.com/QuantumNous/new-api/service/opswallet"
	"github.com/gin-gonic/gin"
)

func manageOPSBalance(c *gin.Context, req ManageRequest) bool {
	if !opsauthority.Enabled() {
		return false
	}
	if !opswallet.Enabled() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "OPS balance changes are disabled", "retry_same_operation": false})
		return true
	}
	user, err := model.GetUserById(req.Id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "User unavailable", "retry_same_operation": false})
		return true
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Permission denied", "retry_same_operation": false})
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
	// OPS owns the ledger. A local top-up log would credit the same money twice.
	recordManageAuditFor(c, user.Id, "user.ops_balance_change", map[string]any{"request_id": req.RequestID, "operation": operation, "amount": result.Amount, "currency": opsbilling.Currency(), "replayed": result.Replayed, "ledger_entry_id": result.LedgerEntryID})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
	return true
}
