package common

import "fmt"

// AICCException 定义AICC异常
type AICCException struct {
	msg string
}

// NewAICCException 创建新的AICC异常
func NewAICCException(msg string) *AICCException {
	return &AICCException{msg: msg}
}

// Error 实现error接口
func (e *AICCException) Error() string {
	return e.msg
}

// GetMsg 获取错误消息
func (e *AICCException) GetMsg() string {
	return e.msg
}

// String 字符串表示
func (e *AICCException) String() string {
	return e.GetMsg()
}

// WrapError 包装错误
func WrapError(err error, msg string) error {
	if err == nil {
		return NewAICCException(msg)
	}
	return NewAICCException(fmt.Sprintf("%s: %v", msg, err))
}
