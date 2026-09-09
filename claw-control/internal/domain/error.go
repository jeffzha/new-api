package domain

import "fmt"

type ErrorKind string

const (
	KindInvalid     ErrorKind = "invalid_request"
	KindNotFound    ErrorKind = "not_found"
	KindConflict    ErrorKind = "conflict"
	KindForbidden   ErrorKind = "forbidden"
	KindRecentAuth  ErrorKind = "recent_auth_required"
	KindUnavailable ErrorKind = "unavailable"
)

type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

func Invalid(format string, args ...any) error {
	return &Error{Kind: KindInvalid, Message: fmt.Sprintf(format, args...)}
}

func NotFound(format string, args ...any) error {
	return &Error{Kind: KindNotFound, Message: fmt.Sprintf(format, args...)}
}

func Conflict(format string, args ...any) error {
	return &Error{Kind: KindConflict, Message: fmt.Sprintf(format, args...)}
}

func Forbidden(format string, args ...any) error {
	return &Error{Kind: KindForbidden, Message: fmt.Sprintf(format, args...)}
}

func RecentAuth(format string, args ...any) error {
	return &Error{Kind: KindRecentAuth, Message: fmt.Sprintf(format, args...)}
}

func Unavailable(format string, args ...any) error {
	return &Error{Kind: KindUnavailable, Message: fmt.Sprintf(format, args...)}
}
