package workbenchbridge

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

type Identity struct {
	UserID      int
	Username    string
	DisplayName string
	Role        int
	Status      int
	CreatedAt   int64
}

// IdentityVersion is a non-secret fingerprint of the current identity state.
// It avoids adding a workbench-specific column to users while still changing
// when fields relevant to authentication or display identity change.
func IdentityVersion(identity Identity) string {
	state := fmt.Sprintf(
		"v1\n%d\n%s\n%s\n%d\n%d\n%d",
		identity.UserID,
		identity.Username,
		identity.DisplayName,
		identity.Role,
		identity.Status,
		identity.CreatedAt,
	)
	digest := sha256.Sum256([]byte(state))
	return "v1." + base64.RawURLEncoding.EncodeToString(digest[:16])
}
