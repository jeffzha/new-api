package opsauthority

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Binding freezes the credential-owned ledger selected when usage is accepted.
// It contains no secret; rotating a secret does not move an outstanding charge.
type Binding struct {
	Version int    `json:"version"`
	Scope   string `json:"scope"`
	Target  string `json:"target"`
}

func NewBinding(base, key, scope string) (*Binding, error) {
	target, err := Target(base, key)
	if err != nil {
		return nil, err
	}
	return &Binding{Version: 1, Scope: scope, Target: target}, nil
}

func (b *Binding) Matches(base, key, scope string) bool {
	if b == nil || b.Version != 1 || b.Scope != scope {
		return false
	}
	target, err := Target(base, key)
	return err == nil && b.Target == target
}

func Target(base, key string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") || key == "" {
		return "", errors.New("invalid OPS wallet target")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", errors.New("invalid OPS wallet target")
	}
	u.Host = strings.ToLower(u.Host)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.String()+"\n"+key))), nil
}
