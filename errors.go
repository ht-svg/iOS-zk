package main

import (
	"errors"
	"log"
	"net/http"
)

// Stable error codes shared by HTTP responses, batch results and logs.
const (
	CodeBadRequest      = "bad_request"
	CodeSessionRequired = "session_required"
	CodeSessionLost     = "session_lost"
	CodeUnsupported     = "unsupported"
	CodeUpstream        = "upstream_error"
	CodeDevice          = "device_error"
	CodeInternal        = "internal_error"
	CodeService         = "service_unavailable"
	CodeUnauthorized    = "unauthorized"
)

// appError carries a stable code and HTTP status through the call stack.
// Handlers, batch execution and logs all read from the same type.
type appError struct {
	Code    string
	Message string
	Status  int
	Cause   error
}

func (e *appError) Error() string {
	return e.Message
}

func (e *appError) Unwrap() error {
	return e.Cause
}

func newAppError(code, message string, status int, cause error) *appError {
	return &appError{Code: code, Message: message, Status: status, Cause: cause}
}

var (
	errSessionRequired  = newAppError(CodeSessionRequired, "请先为此设备启动 Appium/WDA 会话", http.StatusConflict, nil)
	errSessionLost      = newAppError(CodeSessionLost, "设备会话已失效，正在重新连接", http.StatusConflict, nil)
	errDeviceUnlicensed = newAppError(CodeUnauthorized, "该设备未授权，请在中控界面查看授权状态", http.StatusForbidden, nil)
)

func appErrorFrom(err error) *appError {
	if err == nil {
		return nil
	}
	var appErr *appError
	if errors.As(err, &appErr) {
		return appErr
	}
	return nil
}

func errorCode(err error) string {
	if appErr := appErrorFrom(err); appErr != nil {
		return appErr.Code
	}
	if errors.Is(err, errSessionLost) {
		return CodeSessionLost
	}
	return CodeDevice
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusConflict:
		return CodeSessionRequired
	case http.StatusForbidden:
		return CodeUnauthorized
	case http.StatusNotImplemented:
		return CodeUnsupported
	case http.StatusServiceUnavailable:
		return CodeService
	case http.StatusInternalServerError:
		return CodeInternal
	case http.StatusBadGateway:
		return CodeUpstream
	default:
		return CodeInternal
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeErrorWithCode(w, status, codeForStatus(status), err)
}

func writeErrorWithCode(w http.ResponseWriter, status int, code string, err error) {
	message := "未知错误"
	if err != nil {
		message = err.Error()
	}
	log.Printf("event=http_error status=%d code=%s error=%q", status, code, chineseError(err))
	writeJSON(w, status, map[string]string{
		"error": message,
		"code":  code,
	})
}

func (s *server) writeAPIError(w http.ResponseWriter, fallbackStatus int, err error) {
	if appErr := appErrorFrom(err); appErr != nil {
		writeErrorWithCode(w, appErr.Status, appErr.Code, err)
		return
	}
	if errors.Is(err, errSessionLost) {
		writeErrorWithCode(w, http.StatusConflict, CodeSessionLost, err)
		return
	}
	writeErrorWithCode(w, fallbackStatus, codeForStatus(fallbackStatus), err)
}

func upstreamError(err error) error {
	return newAppError(CodeUpstream, err.Error(), http.StatusBadGateway, err)
}

func unsupportedError(message string) error {
	return newAppError(CodeUnsupported, message, http.StatusNotImplemented, nil)
}
