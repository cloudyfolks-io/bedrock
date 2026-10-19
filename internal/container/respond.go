package container

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type ActionError struct {
	Code    int
	Reason  metav1.StatusReason
	Message string
}

func (e ActionError) Error() string {
	return e.Message
}

func (e ActionError) Status() metav1.Status {
	return metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Code:     int32(e.Code),
		Reason:   e.Reason,
		Message:  e.Message,
	}
}

func badRequest(format string, args ...any) ActionError {
	return ActionError{Code: http.StatusBadRequest, Reason: metav1.StatusReasonBadRequest, Message: fmt.Sprintf(format, args...)}
}

func notFoundError(format string, args ...any) ActionError {
	return ActionError{Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound, Message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) ActionError {
	return ActionError{Code: http.StatusConflict, Reason: metav1.StatusReasonConflict, Message: fmt.Sprintf(format, args...)}
}

func forbidden(message string) ActionError {
	return ActionError{Code: http.StatusForbidden, Reason: metav1.StatusReasonForbidden, Message: message}
}

func internalError(err error) ActionError {
	logf.Log.WithName("container").Error(err, "the request failed")
	return ActionError{Code: http.StatusInternalServerError, Reason: metav1.StatusReasonInternalError, Message: "the request failed; ask an administrator to read the operator log"}
}

func unreachable(err error) ActionError {
	logf.Log.WithName("container").Error(err, "the API server is not reachable")
	return ActionError{Code: http.StatusBadGateway, Reason: metav1.StatusReasonServiceUnavailable, Message: "the API server did not answer the request"}
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, ActionError{Code: http.StatusMethodNotAllowed, Reason: metav1.StatusReasonMethodNotAllowed, Message: "the method is not allowed"})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, err error) {
	var actionErr ActionError
	if !errors.As(err, &actionErr) {
		actionErr = internalError(err)
	}
	writeJSON(w, actionErr.Code, actionErr.Status())
}
