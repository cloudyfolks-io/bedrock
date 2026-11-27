package saxess

import (
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
	ErrMobileUnauthorized    = errors.New("invalid mobile credentials")
	ErrMobileConsentRejected = errors.New("mobile consent rejected")
)

// MobilePrincipal comes only from authenticated credentials, never the request body.
type MobilePrincipal struct {
	EmployeeID string
	Email      string
	DeviceID   string
}

// MobileAuthenticate verifies a bearer token. Return ErrMobileUnauthorized for invalid
// credentials and another error for service failures.
type MobileAuthenticate func(context.Context, string) (MobilePrincipal, error)

type MobileConsent struct {
	AuthReqID string
	Email     string
	SignedMsg string
	DeviceID  string
}

type MobileRequest struct {
	AuthReqID string              `json:"auth_req_id"`
	Status    string              `json:"status"`
	Notify    MobileRequestNotify `json:"notify"`
	CreatedAt time.Time           `json:"created_at"`
}

type MobileRequestNotify struct {
	Data         MobileRequestData         `json:"data"`
	Notification MobileRequestNotification `json:"notification"`
}

type MobileRequestData struct {
	AuthReqID            string `json:"auth_req_id"`
	AuthorizationDetails string `json:"authorization_details"`
	SignPayload          string `json:"sign_payload"`
	Timestamp            string `json:"timestamp"`
	Type                 string `json:"type"`
}

type MobileRequestNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type MobileBackend interface {
	// GetRequests returns only requests owned by the authenticated principal.
	GetRequests(context.Context, MobilePrincipal) ([]MobileRequest, error)
	// SendConsent must check that the email and request belong to the principal.
	// It must verify the active user, approved device, pending status, expiry, and
	// card signature, then persist the transition, proof, and audit entry before
	// returning nil. Return ErrMobileConsentRejected for a rejected submission.
	SendConsent(context.Context, MobilePrincipal, MobileConsent) error
}

// MobileHTTP exposes handlers for a caller to mount explicitly.
type MobileHTTP struct {
	authenticate MobileAuthenticate
	backend      MobileBackend
}

func NewMobileHTTP(authenticate MobileAuthenticate, backend MobileBackend) *MobileHTTP {
	return &MobileHTTP{authenticate: authenticate, backend: backend}
}

func (h *MobileHTTP) GetRequests(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authorize(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		mobileBodyError(w, err)
		return
	}
	requests, err := h.backend.GetRequests(r.Context(), principal)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if requests == nil {
		requests = []MobileRequest{}
	}
	writeMobileJSON(w, requests)
}

func (h *MobileHTTP) SendConsent(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authorize(w, r)
	if !ok {
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
	consent, ok := parseMobileConsent(body)
	if !ok {
		mobileBodyError(w, nil)
		return
	}
	if consent.Email != principal.Email {
		http.Error(w, "consent rejected", http.StatusForbidden)
		return
	}
	if err := h.backend.SendConsent(r.Context(), principal, consent); err != nil {
		if errors.Is(err, ErrMobileConsentRejected) {
			http.Error(w, "consent rejected", http.StatusForbidden)
		} else {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	writeMobileJSON(w, struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}{"verified", "Consent verified successfully"})
}

func (h *MobileHTTP) authorize(w http.ResponseWriter, r *http.Request) (MobilePrincipal, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return MobilePrincipal{}, false
	}
	headers := r.Header.Values("Authorization")
	if len(headers) == 1 && len(headers[0]) <= 8192 {
		scheme, token, found := strings.Cut(headers[0], " ")
		token = strings.TrimLeft(token, " ")
		if found && strings.EqualFold(scheme, "Bearer") && token != "" &&
			!strings.ContainsAny(token, ",") && strings.IndexFunc(token, unicode.IsSpace) < 0 &&
			strings.IndexFunc(token, unicode.IsControl) < 0 {
			if h.authenticate == nil || h.backend == nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return MobilePrincipal{}, false
			}
			principal, err := h.authenticate(r.Context(), token)
			if err == nil && principal.EmployeeID != "" && principal.Email != "" {
				return principal, true
			}
			if err != nil && !errors.Is(err, ErrMobileUnauthorized) {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return MobilePrincipal{}, false
			}
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return MobilePrincipal{}, false
}

func parseMobileConsent(body []byte) (MobileConsent, bool) {
	var consent MobileConsent
	if !utf8.Valid(body) {
		return MobileConsent{}, false
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return MobileConsent{}, false
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return MobileConsent{}, false
		}
		var field *string
		var limit int
		switch key {
		case "auth_req_id":
			field, limit = &consent.AuthReqID, 512
		case "email":
			field, limit = &consent.Email, 254
		case "signed_msg":
			field, limit = &consent.SignedMsg, 8192
		case "device_id":
			field, limit = &consent.DeviceID, 128
		default:
			return MobileConsent{}, false
		}
		if *field != "" || d.Decode(field) != nil || *field == "" || len(*field) > limit ||
			strings.TrimSpace(*field) != *field || strings.IndexFunc(*field, unicode.IsControl) >= 0 {
			return MobileConsent{}, false
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return MobileConsent{}, false
	}
	if _, err := d.Token(); err != io.EOF {
		return MobileConsent{}, false
	}
	return consent, consent.AuthReqID != "" && consent.Email != "" && consent.SignedMsg != "" && consent.DeviceID != ""
}

func mobileBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "invalid request body", http.StatusBadRequest)
}

func writeMobileJSON(w http.ResponseWriter, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(body, '\n'))
}
