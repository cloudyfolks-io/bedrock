package saxess

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mobileBackendStub struct {
	principal MobilePrincipal
	consent   MobileConsent
	requests  []MobileRequest
	err       error
	calls     int
}

func (b *mobileBackendStub) GetRequests(_ context.Context, p MobilePrincipal) ([]MobileRequest, error) {
	b.calls++
	b.principal = p
	return b.requests, b.err
}

func (b *mobileBackendStub) SendConsent(_ context.Context, p MobilePrincipal, consent MobileConsent) error {
	b.calls++
	b.principal, b.consent = p, consent
	return b.err
}

const mobileConsentBody = `{"auth_req_id":"201::1763974961::employee@example.com","email":"employee@example.com","signed_msg":"card-proof","device_id":"101"}`

func TestMobileHTTPSuccess(t *testing.T) {
	principal := MobilePrincipal{EmployeeID: "employee-1", Email: "employee@example.com"}
	id := "201::1763974961::employee@example.com"
	backend := &mobileBackendStub{requests: []MobileRequest{{
		AuthReqID: id,
		Status:    "used",
		Notify: MobileRequestNotify{
			Data:         MobileRequestData{id, `[{"type":"biometric_auth"}]`, id, "1763974841", "DASHBOARD_LOGIN_AUTH_REQUEST"},
			Notification: MobileRequestNotification{"Biometric Approval Request", "Review and approve the pending request."},
		},
		CreatedAt: time.Date(2025, 11, 24, 9, 0, 41, 174644000, time.UTC),
	}}}
	h := NewMobileHTTP(func(_ context.Context, token string) (MobilePrincipal, error) {
		if token != "auth-code" {
			t.Fatalf("token = %q", token)
		}
		return principal, nil
	}, backend)

	list := serveMobile(h.GetRequests, http.MethodPost, "/api/employees/get-requests", "Bearer auth-code", "", "")
	wantList := `[{"auth_req_id":"201::1763974961::employee@example.com","status":"used","notify":{"data":{"auth_req_id":"201::1763974961::employee@example.com","authorization_details":"[{\"type\":\"biometric_auth\"}]","sign_payload":"201::1763974961::employee@example.com","timestamp":"1763974841","type":"DASHBOARD_LOGIN_AUTH_REQUEST"},"notification":{"title":"Biometric Approval Request","body":"Review and approve the pending request."}},"created_at":"2025-11-24T09:00:41.174644Z"}]`
	checkMobileSuccess(t, list, wantList)
	if backend.principal != principal || backend.calls != 1 {
		t.Fatalf("list backend: %+v", backend)
	}
	backend.requests = nil
	checkMobileSuccess(t, serveMobile(h.GetRequests, http.MethodPost, "/api/employees/get-requests", "Bearer auth-code", "", ""), `[]`)
	checkMobileSuccess(t, serveMobile(h.SendConsent, http.MethodPost, "/api/auth/send_consent", "Bearer auth-code", "application/json; charset=utf-8", mobileConsentBody),
		`{"status":"verified","message":"Consent verified successfully"}`)
	if backend.principal != principal || backend.consent != (MobileConsent{id, principal.Email, "card-proof", "101"}) || backend.calls != 3 {
		t.Fatalf("consent backend: %+v", backend)
	}
}

func TestMobileHTTPFailures(t *testing.T) {
	failure := errors.New("private dependency failure")
	for _, tc := range []struct {
		name, method, authorization, contentType, body string
		list, emptyPrincipal                           bool
		authErr, backendErr                            error
		status, calls                                  int
	}{
		{name: "wrong method", method: "GET", status: 405},
		{name: "missing bearer", status: 401},
		{name: "malformed bearer", authorization: "Bearer a, b", status: 401},
		{name: "oversize bearer", authorization: "Bearer " + strings.Repeat("a", 8192), status: 401},
		{name: "bad token", authorization: "Bearer token", authErr: fmt.Errorf("wrapped: %w", ErrMobileUnauthorized), status: 401},
		{name: "empty principal", authorization: "Bearer token", emptyPrincipal: true, status: 401},
		{name: "auth service failure", authorization: "Bearer token", authErr: failure, status: 500},
		{name: "list body not permitted", list: true, authorization: "Bearer token", body: " ", status: 400},
		{name: "list oversized", list: true, authorization: "Bearer token", body: "{}", status: 413},
		{name: "list service failure", list: true, authorization: "Bearer token", backendErr: failure, status: 500, calls: 1},
		{name: "wrong content type", authorization: "Bearer token", contentType: "text/plain", body: mobileConsentBody, status: 415},
		{name: "oversize", authorization: "Bearer token", body: strings.Repeat(" ", (16<<10)+1), status: 413},
		{name: "missing", authorization: "Bearer token", body: `{}`, status: 400},
		{name: "null", authorization: "Bearer token", body: strings.Replace(mobileConsentBody, `"101"`, `null`, 1), status: 400},
		{name: "wrong type", authorization: "Bearer token", body: strings.Replace(mobileConsentBody, `"101"`, `101`, 1), status: 400},
		{name: "field too long", authorization: "Bearer token", body: strings.Replace(mobileConsentBody, "101", strings.Repeat("x", 129), 1), status: 400},
		{name: "control character", authorization: "Bearer token", body: strings.Replace(mobileConsentBody, "card-proof", `card\nproof`, 1), status: 400},
		{name: "unknown", authorization: "Bearer token", body: mobileConsentBody[:len(mobileConsentBody)-1] + `,"employee_id":"other"}`, status: 400},
		{name: "duplicate", authorization: "Bearer token", body: mobileConsentBody[:len(mobileConsentBody)-1] + `,"email":"other"}`, status: 400},
		{name: "trailing document", authorization: "Bearer token", body: mobileConsentBody + `{}`, status: 400},
		{name: "wrong employee", authorization: "Bearer token", body: strings.ReplaceAll(mobileConsentBody, "employee@example.com", "other@example.com"), status: 403},
		{name: "consent rejected", authorization: "Bearer token", body: mobileConsentBody, backendErr: fmt.Errorf("wrapped: %w", ErrMobileConsentRejected), status: 403, calls: 1},
		{name: "consent service failure", authorization: "Bearer token", body: mobileConsentBody, backendErr: failure, status: 500, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &mobileBackendStub{err: tc.backendErr}
			h := NewMobileHTTP(func(context.Context, string) (MobilePrincipal, error) {
				if tc.emptyPrincipal {
					return MobilePrincipal{}, nil
				}
				return MobilePrincipal{EmployeeID: "employee-1", Email: "employee@example.com"}, tc.authErr
			}, backend)
			handler, path := h.SendConsent, "/api/auth/send_consent"
			if tc.list {
				handler, path = h.GetRequests, "/api/employees/get-requests"
			}
			method, contentType := tc.method, tc.contentType
			if method == "" {
				method = http.MethodPost
			}
			if contentType == "" {
				contentType = "application/json"
			}
			res := serveMobile(handler, method, path, tc.authorization, contentType, tc.body)
			if res.Code != tc.status || backend.calls != tc.calls || strings.Contains(res.Body.String(), failure.Error()) {
				t.Fatalf("status = %d, backend calls = %d, body = %q", res.Code, backend.calls, res.Body.String())
			}
			if tc.status == 401 && res.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing bearer challenge")
			}
			if tc.status == 405 && res.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow: POST")
			}
		})
	}
}

func TestMobileHTTPRejectsDuplicateAuthorization(t *testing.T) {
	backend := &mobileBackendStub{}
	authCalls := 0
	h := NewMobileHTTP(func(context.Context, string) (MobilePrincipal, error) {
		authCalls++
		return MobilePrincipal{EmployeeID: "employee-1", Email: "employee@example.com"}, nil
	}, backend)
	req := httptest.NewRequest(http.MethodPost, "/api/employees/get-requests", nil)
	req.Header.Add("Authorization", "Bearer first")
	req.Header.Add("Authorization", "Bearer second")
	res := httptest.NewRecorder()
	h.GetRequests(res, req)
	if res.Code != http.StatusUnauthorized || authCalls != 0 || backend.calls != 0 {
		t.Fatalf("status = %d, auth calls = %d, backend calls = %d", res.Code, authCalls, backend.calls)
	}
}

func serveMobile(handler http.HandlerFunc, method, path, authorization, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", contentType)
	res := httptest.NewRecorder()
	handler(res, req)
	return res
}

func checkMobileSuccess(t *testing.T, res *httptest.ResponseRecorder, body string) {
	t.Helper()
	if res.Code != 200 || res.Header().Get("Content-Type") != "application/json" ||
		res.Header().Get("Cache-Control") != "no-store" || res.Body.String() != body+"\n" {
		t.Fatalf("status = %d, headers = %v, body = %q", res.Code, res.Header(), res.Body.String())
	}
}
