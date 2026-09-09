package secrets

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var providerReferencePattern = regexp.MustCompile(`^env://WORKBENCH_PROVIDER_[A-Z0-9_]+$`)

type Resolver interface {
	Resolve(reference string) (string, error)
}

type EnvironmentResolver struct{}

func (EnvironmentResolver) Resolve(reference string) (string, error) {
	if !ValidProviderReference(reference) {
		return "", fmt.Errorf("unsupported secret reference; expected env://WORKBENCH_PROVIDER_[A-Z0-9_]+")
	}
	name := strings.TrimPrefix(reference, "env://")
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("secret reference %s is unavailable", reference)
	}
	return value, nil
}

func ValidProviderReference(reference string) bool {
	normalized := strings.TrimSpace(reference)
	return providerReferencePattern.MatchString(normalized) || ValidVaultReference(normalized)
}
