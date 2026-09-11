package agencyhub

import (
	"bytes"
	"errors"
	"strconv"
)

// decimalInt64 is the wire representation for API quantities whose exact
// value must survive JavaScript and other JSON number implementations. The
// JSON contract is deliberately string-only; database code continues to use
// int64.
type decimalInt64 int64

func (value *decimalInt64) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return errors.New("value must be a decimal string")
	}
	text := string(raw[1 : len(raw)-1])
	if text == "" {
		return errors.New("value must be a decimal string")
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || strconv.FormatInt(parsed, 10) != text {
		return errors.New("value must be a canonical decimal string")
	}
	*value = decimalInt64(parsed)
	return nil
}

func (value decimalInt64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(strconv.FormatInt(int64(value), 10))), nil
}

func (value decimalInt64) Int64() int64 {
	return int64(value)
}
