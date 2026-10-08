package compute

import (
	"encoding/json"
	"errors"
	"net/http"
)

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
