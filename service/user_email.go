package service

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var topUpEmailNotifications sync.Map

func SendUserEmail(email, subject, message string) error {
	if !common.SMTPConfigured() {
		return fmt.Errorf("SMTP email is not configured")
	}
	email = strings.TrimSpace(email)
	subject = strings.TrimSpace(subject)
	message = strings.TrimSpace(message)
	if email == "" || subject == "" || message == "" {
		return fmt.Errorf("email, subject, and message are required")
	}
	content := "<p>" + strings.ReplaceAll(html.EscapeString(message), "\n", "<br>") + "</p>"
	return common.SendEmail(subject, email, content)
}

// NotifyTopUpSuccess sends a best-effort receipt after a payment has committed.
// Delivery is asynchronous so an SMTP outage cannot delay the payment callback.
func NotifyTopUpSuccess(tradeNo string) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return
	}
	if _, loaded := topUpEmailNotifications.LoadOrStore(tradeNo, time.Now()); loaded {
		return
	}
	time.AfterFunc(24*time.Hour, func() {
		topUpEmailNotifications.Delete(tradeNo)
	})
	go func() {
		topUp := model.GetTopUpByTradeNo(tradeNo)
		if topUp == nil || topUp.Status != common.TopUpStatusSuccess {
			topUpEmailNotifications.Delete(tradeNo)
			return
		}
		user, err := model.GetUserById(topUp.UserId, false)
		if err != nil || common.Validate.Var(model.NormalizeEmail(user.Email), "required,email") != nil {
			return
		}
		subject := common.SystemName + " - Payment received"
		message := fmt.Sprintf("Your payment has been received successfully.\nOrder: %s\nAmount: %.2f", topUp.TradeNo, topUp.Money)
		if err := SendUserEmail(user.Email, subject, message); err != nil {
			common.SysError(fmt.Sprintf("failed to send top-up email: trade_no=%s user_id=%d error=%v", topUp.TradeNo, topUp.UserId, err))
		}
	}()
}
