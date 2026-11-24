package saxess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const validRegistration = `{"device_type":"android","public_key":"0xabcd","fingerprint":"device-1","signed_msg":"card-signature","invite_token":"invite-token"}`

type registrationBackendFunc func(context.Context, PendingDeviceRegistration) (RegisteredDevice, error)

func (f registrationBackendFunc) RegisterPendingDevice(ctx context.Context, input PendingDeviceRegistration) (RegisteredDevice, error) {
	return f(ctx, input)
}

func registrationRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/employees/devices/register", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer enrollment-credential")
	return r
}

func TestRegisterDevicePending(t *testing.T) {
	h := NewRegistrationHTTP(registrationBackendFunc(func(_ context.Context, input PendingDeviceRegistration) (RegisteredDevice, error) {
		want := PendingDeviceRegistration{
			EnrollmentCredential: "enrollment-credential",
			DeviceType:           "android",
			PublicKey:            "0xabcd",
			Fingerprint:          "device-1",
			SignedMsg:            "card-signature",
			InviteToken:          "invite-token",
		}
		if input != want {
			t.Fatalf("unexpected backend input: %+v", input)
		}
		return RegisteredDevice{
			ID: 101, UserID: 2, DeviceType: input.DeviceType, PublicKey: input.PublicKey,
			Status: "pending", Fingerprint: input.Fingerprint,
			RegisteredAt: time.Date(2025, 11, 24, 5, 52, 22, 0, time.UTC),
		}, nil
	}))
	w := httptest.NewRecorder()
	h.RegisterDevice(w, registrationRequest(validRegistration))
	const want = `{"ID":101,"UserID":2,"DeviceType":"android","PublicKey":"0xabcd","Status":"pending","Fingerprint":"device-1","RegisteredAt":"2025-11-24T05:52:22Z"}`
	if w.Code != http.StatusOK || w.Body.String() != want+"\n" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
	}
}

func TestRegisterDeviceInvalidRequest(t *testing.T) {
	h := NewRegistrationHTTP(registrationBackendFunc(func(context.Context, PendingDeviceRegistration) (RegisteredDevice, error) {
		t.Fatal("invalid request reached backend")
		return RegisteredDevice{}, nil
	}))
	for _, tc := range []struct {
		name string
		body string
		edit func(*http.Request)
		want int
	}{
		{"non POST", validRegistration, func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
		{"missing bearer", validRegistration, func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		{"ambiguous bearer", validRegistration, func(r *http.Request) { r.Header.Add("Authorization", "Bearer other") }, http.StatusUnauthorized},
		{"wrong content type", validRegistration, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"oversized", validRegistration + strings.Repeat(" ", 16<<10), nil, http.StatusRequestEntityTooLarge},
		{"duplicate key", `{"invite\u005ftoken":"another",` + validRegistration[1:], nil, http.StatusBadRequest},
		{"missing invite", strings.ReplaceAll(validRegistration, `,"invite_token":"invite-token"`, ""), nil, http.StatusBadRequest},
		{"invalid type", strings.ReplaceAll(validRegistration, `"android"`, `"desktop"`), nil, http.StatusBadRequest},
		{"null signature", strings.ReplaceAll(validRegistration, `"card-signature"`, `null`), nil, http.StatusBadRequest},
		{"approval field", `{"Status":"approved",` + validRegistration[1:], nil, http.StatusBadRequest},
		{"trailing object", validRegistration + `{}`, nil, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := registrationRequest(tc.body), httptest.NewRecorder()
			if tc.edit != nil {
				tc.edit(r)
			}
			h.RegisterDevice(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestRegisterDeviceBackendRejections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status string
		want   int
	}{
		{"unauthorized", ErrRegistrationUnauthorized, "", http.StatusUnauthorized},
		{"rejected", ErrRegistrationRejected, "", http.StatusForbidden},
		{"duplicate device", ErrRegistrationConflict, "", http.StatusConflict},
		{"approved device", nil, "approved", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewRegistrationHTTP(registrationBackendFunc(func(context.Context, PendingDeviceRegistration) (RegisteredDevice, error) {
				return RegisteredDevice{Status: tc.status}, tc.err
			}))
			w := httptest.NewRecorder()
			h.RegisterDevice(w, registrationRequest(validRegistration))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if tc.want == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing Bearer challenge")
			}
		})
	}
}
