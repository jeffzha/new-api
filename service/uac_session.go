package service

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/uac"
)

func validateProjectSession(session *model.UserSession) error {
	if session.LoginMethod != "uac" {
		if uac.Required() {
			return ErrLoginSessionRevoked
		}
		return nil
	}
	if !uac.Enabled() {
		return ErrLoginSessionRevoked
	}
	user, err := model.GetUserCache(session.UserID)
	if err != nil || user.Role < 10 {
		return ErrLoginSessionRevoked
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if uac.Default.Validate(ctx, session.SID) != nil {
		return ErrLoginSessionRevoked
	}
	return nil
}

// RevokeProjectLogin authenticates the local refresh secret even when UAC is
// unavailable, so an upstream outage never prevents signing out locally.
func RevokeProjectLogin(raw, expectedSID string) (string, error) {
	sid, secret, ok := splitRefreshToken(raw)
	if !ok {
		return "", ErrRefreshTokenInvalid
	}
	if expectedSID != "" && expectedSID != sid {
		return "", ErrLoginSessionMismatch
	}
	revoked, err := model.RevokeUserSessionByRefreshHash(sid, hashRefreshSecret(secret), "uac_logout")
	if err != nil {
		return "", err
	}
	if !revoked {
		return "", ErrRefreshTokenInvalid
	}
	return sid, nil
}
