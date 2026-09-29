package httpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sample struct {
	Name string `json:"name"`
}

func post(body, contentType string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sample", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	return req
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var answer map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return answer["error"]
}

func TestWriteSetsJSONAndNoStore(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusConflict, "already_started")
	if rec.Code != http.StatusConflict || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	if code := errorCode(t, rec); code != "already_started" {
		t.Fatalf("error %q", code)
	}
}

func TestDecodeAcceptsJSONWithParameters(t *testing.T) {
	body, err := Decode[sample](httptest.NewRecorder(), post(`{"name":"alice"}`, "application/json; charset=utf-8"))
	if err != nil || body.Name != "alice" {
		t.Fatalf("body %+v err %v", body, err)
	}
}

func TestDecodeRefusesOtherMediaTypes(t *testing.T) {
	_, err := Decode[sample](httptest.NewRecorder(), post(`{"name":"alice"}`, "text/plain"))
	if !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("err %v", err)
	}
	rec := httptest.NewRecorder()
	WriteDecodeError(rec, err)
	if rec.Code != http.StatusUnsupportedMediaType || errorCode(t, rec) != "unsupported_media_type" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDecodeStopsAtTheBodyLimit(t *testing.T) {
	oversized := `{"name":"` + strings.Repeat("a", MaxBody) + `"}`
	_, err := Decode[sample](httptest.NewRecorder(), post(oversized, "application/json"))
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err %v, want a MaxBytesError", err)
	}
	rec := httptest.NewRecorder()
	WriteDecodeError(rec, err)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestInternalLogsTheCauseAndHidesIt(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	rec := httptest.NewRecorder()
	Internal(rec, post("{}", "application/json"), "sample request failed", errors.New("disk on fire"))
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal" || strings.Contains(rec.Body.String(), "disk on fire") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	written := logs.String()
	if !strings.Contains(written, "sample request failed") || !strings.Contains(written, "disk on fire") || !strings.Contains(written, "path=/api/v1/sample") {
		t.Fatalf("log %s", written)
	}
}
