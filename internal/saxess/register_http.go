package saxess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrRegistrationUnauthorized = errors.New("invalid enrollment credential")
	ErrRegistrationRejected     = errors.New("device registration rejected")
	ErrRegistrationConflict     = errors.New("device already registered")
)

type PendingDeviceRegistration struct {
	EnrollmentCredential string
	DeviceType           string
	PublicKey            string
	Fingerprint          string
	SignedMsg            string
	InviteToken          string
}

type RegisteredDevice struct {
	ID           int64     `json:"ID"`
	UserID       int64     `json:"UserID"`
	DeviceType   string    `json:"DeviceType"`
	PublicKey    string    `json:"PublicKey"`
	Status       string    `json:"Status"`
	Fingerprint  string    `json:"Fingerprint"`
	RegisteredAt time.Time `json:"RegisteredAt"`
	deviceToken  string
}

type RegistrationBackend interface {
	// RegisterPendingDevice validates the enrollment credential and verifies SignedMsg
	// against InviteToken before persisting a pending device. It never approves a device.
	RegisterPendingDevice(context.Context, PendingDeviceRegistration) (RegisteredDevice, error)
}

type RegistrationHTTP struct {
	backend RegistrationBackend
}

func NewRegistrationHTTP(backend RegistrationBackend) *RegistrationHTTP {
	return &RegistrationHTTP{backend: backend}
}

func (h *RegistrationHTTP) RegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	credential := enrollmentBearer(r.Header.Values("Authorization"))
	if credential == "" {
		writeEnrollmentUnauthorized(w)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		mobileBodyError(w, err)
		return
	}
	input, ok := parseDeviceRegistration(body)
	if !ok {
		mobileBodyError(w, nil)
		return
	}
	if h.backend == nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	input.EnrollmentCredential = credential
	device, err := h.backend.RegisterPendingDevice(r.Context(), input)
	switch {
	case errors.Is(err, ErrRegistrationUnauthorized):
		writeEnrollmentUnauthorized(w)
	case errors.Is(err, ErrRegistrationRejected):
		http.Error(w, "device registration rejected", http.StatusForbidden)
	case errors.Is(err, ErrRegistrationConflict):
		http.Error(w, "device already registered", http.StatusConflict)
	case err != nil || device.Status != "pending":
		http.Error(w, "internal server error", http.StatusInternalServerError)
	default:
		if device.deviceToken != "" {
			w.Header().Set("X-SAxess-Device-Token", device.deviceToken)
		}
		writeMobileJSON(w, device)
	}
}

func writeEnrollmentUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func enrollmentBearer(headers []string) string {
	if len(headers) != 1 || len(headers[0]) > 8192 {
		return ""
	}
	scheme, token, found := strings.Cut(headers[0], " ")
	token = strings.TrimLeft(token, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.Contains(token, ",") ||
		strings.IndexFunc(token, unicode.IsSpace) >= 0 || strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return ""
	}
	return token
}

func parseDeviceRegistration(body []byte) (PendingDeviceRegistration, bool) {
	var input PendingDeviceRegistration
	if !utf8.Valid(body) {
		return input, false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return input, false
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return input, false
		}
		var field *string
		switch key {
		case "device_type":
			field = &input.DeviceType
		case "public_key":
			field = &input.PublicKey
		case "fingerprint":
			field = &input.Fingerprint
		case "signed_msg":
			field = &input.SignedMsg
		case "invite_token":
			field = &input.InviteToken
		default:
			return input, false
		}
		if *field != "" || d.Decode(field) != nil || *field == "" ||
			strings.TrimSpace(*field) != *field || strings.IndexFunc(*field, unicode.IsControl) >= 0 {
			return input, false
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return input, false
	}
	if _, err := d.Token(); err != io.EOF {
		return input, false
	}
	return input, (input.DeviceType == "android" || input.DeviceType == "ios") &&
		input.PublicKey != "" && input.Fingerprint != "" && input.SignedMsg != "" && input.InviteToken != ""
}
