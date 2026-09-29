package httpjson

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
)

const MaxBody = 64 << 10

var ErrUnsupportedMedia = errors.New("httpjson: body is not application/json")

func Write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func WriteError(w http.ResponseWriter, status int, code string) {
	Write(w, status, map[string]string{"error": code})
}

func Decode[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var body T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return body, ErrUnsupportedMedia
	}
	err = json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody)).Decode(&body)
	return body, err
}

func WriteDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnsupportedMedia) {
		WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	WriteError(w, http.StatusBadRequest, "invalid_request")
}

func Internal(w http.ResponseWriter, r *http.Request, message string, err error) {
	slog.ErrorContext(r.Context(), message, "method", r.Method, "path", r.URL.Path, "error", err)
	WriteError(w, http.StatusInternalServerError, "internal")
}
