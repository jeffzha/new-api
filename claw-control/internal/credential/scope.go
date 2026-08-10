package credential

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/claw-control/internal/model"
)

const PlatformOwnerScope = "platform"

func CustomerOwnerScope(customerID uint64) string {
	return fmt.Sprintf("customer:%d", customerID)
}

func NormalizeOwnerScope(ownerScope string, customerID *uint64) (string, *uint64, bool) {
	ownerScope = strings.ToLower(strings.TrimSpace(ownerScope))
	if ownerScope == "" && customerID == nil {
		return PlatformOwnerScope, nil, true
	}
	if ownerScope == PlatformOwnerScope {
		return ownerScope, nil, customerID == nil
	}
	if customerID == nil || *customerID == 0 {
		return "", nil, false
	}
	expected := CustomerOwnerScope(*customerID)
	if ownerScope == "" {
		return expected, customerID, true
	}
	return expected, customerID, ownerScope == expected
}

func AvailableToCustomer(profile model.CredentialProfile, customerID uint64) bool {
	if !ValidOwner(profile) {
		return false
	}
	if customerID == 0 {
		return false
	}
	if profile.OwnerScope == PlatformOwnerScope {
		return profile.CustomerID == nil
	}
	return profile.CustomerID != nil && *profile.CustomerID == customerID &&
		profile.OwnerScope == CustomerOwnerScope(customerID)
}

func ValidOwner(profile model.CredentialProfile) bool {
	ownerScope, customerID, valid := NormalizeOwnerScope(profile.OwnerScope, profile.CustomerID)
	if !valid || ownerScope != profile.OwnerScope {
		return false
	}
	if customerID == nil || profile.CustomerID == nil {
		return customerID == nil && profile.CustomerID == nil
	}
	return *customerID == *profile.CustomerID
}

func SameOwner(first, second model.CredentialProfile) bool {
	if !ValidOwner(first) || !ValidOwner(second) || first.OwnerScope != second.OwnerScope {
		return false
	}
	if first.CustomerID == nil || second.CustomerID == nil {
		return first.CustomerID == nil && second.CustomerID == nil
	}
	return *first.CustomerID == *second.CustomerID
}
