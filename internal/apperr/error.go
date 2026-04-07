package apperr

import (
	"errors"
	"fmt"
)

// Code 表示稳定的错误分类码。
type Code string

const (
	CodeUnknown                Code = "UNKNOWN"
	CodeConfigInvalid          Code = "CONFIG_INVALID"
	CodeProviderInvalid        Code = "PROVIDER_INVALID"
	CodeProviderInitFailed     Code = "PROVIDER_INIT_FAILED"
	CodeStoreReadFailed        Code = "STORE_READ_FAILED"
	CodeStoreWriteFailed       Code = "STORE_WRITE_FAILED"
	CodeToolArgsInvalid        Code = "TOOL_ARGS_INVALID"
	CodeToolPreconditionFailed Code = "TOOL_PRECONDITION_FAILED"
	CodeToolConflict           Code = "TOOL_CONFLICT"
	CodePhaseTransitionInvalid Code = "PHASE_TRANSITION_INVALID"
	CodeFlowTransitionInvalid  Code = "FLOW_TRANSITION_INVALID"
)

// Error 是轻量结构化错误。
// 设计目标：
// 1. 保持兼容 Go 原生 error 链；
// 2. 先提供稳定分类码，再逐步扩展；
// 3. 不引入额外状态机或复杂元数据。
type Error struct {
	Code    Code
	Op      string
	Message string
	Err     error
}

func (e *Error) Error() string {
	base := string(e.Code)
	switch {
	case e.Message != "" && e.Err != nil:
		return fmt.Sprintf("[%s] %s: %v", base, e.Message, e.Err)
	case e.Message != "":
		return fmt.Sprintf("[%s] %s", base, e.Message)
	case e.Op != "" && e.Err != nil:
		return fmt.Sprintf("[%s] %s: %v", base, e.Op, e.Err)
	case e.Err != nil:
		return fmt.Sprintf("[%s] %v", base, e.Err)
	case e.Op != "":
		return fmt.Sprintf("[%s] %s", base, e.Op)
	default:
		return fmt.Sprintf("[%s]", base)
	}
}

func (e *Error) Unwrap() error { return e.Err }

// New 创建一个结构化错误。
func New(code Code, op, message string) error {
	return &Error{
		Code:    code,
		Op:      op,
		Message: message,
	}
}

// Wrap 包装已有错误，并附加稳定错误码。
func Wrap(err error, code Code, op, message string) error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:    code,
		Op:      op,
		Message: message,
		Err:     err,
	}
}

// CodeOf 返回错误链中的结构化错误码；未命中时返回 CodeUnknown。
func CodeOf(err error) Code {
	var target *Error
	if errors.As(err, &target) && target.Code != "" {
		return target.Code
	}
	return CodeUnknown
}

// OpOf 返回错误链中的操作名；未命中时返回空字符串。
func OpOf(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Op
	}
	return ""
}

// IsCode 判断错误链中是否包含指定错误码。
func IsCode(err error, code Code) bool {
	return CodeOf(err) == code
}

// Display 返回适合直接展示给用户的错误文本，不重复打印错误码。
func Display(err error) string {
	if err == nil {
		return ""
	}
	var target *Error
	if errors.As(err, &target) {
		switch {
		case target.Message != "" && target.Err != nil:
			return fmt.Sprintf("%s: %v", target.Message, target.Err)
		case target.Message != "":
			return target.Message
		case target.Err != nil:
			return target.Err.Error()
		case target.Op != "":
			return target.Op
		}
	}
	return err.Error()
}
