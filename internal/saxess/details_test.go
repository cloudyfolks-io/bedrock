package saxess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestGetDetails(t *testing.T) {
	b, store, _ := newTestBackend()
	ctx := context.Background()
	token := testRegister(t, b, "operator", "101")
	if err := b.ApproveDevice(ctx, "operator", "101", "key-101"); err != nil {
		t.Fatal(err)
	}
	testApprove(t, b, "operator", "42")
	testRegister(t, b, "operator", "102")
	testApprove(t, b, "operator", "103")
	if err := b.RevokeDevice(ctx, "operator", "103"); err != nil {
		t.Fatal(err)
	}
	testApprove(t, b, "other", "104")
	op := store.state.Operators["operator"]
	op.UserID = 27
	store.state.Operators["operator"] = op

	for _, tt := range []struct {
		name, method, token, body string
		status                    int
	}{
		{"success", http.MethodPost, token, "", 200},
		{"missing token", http.MethodPost, "", "", 401},
		{"bad token", http.MethodPost, strings.Repeat("a", 64), "", 401},
		{"wrong method", http.MethodGet, token, "", 405},
		{"unexpected body", http.MethodPost, token, "x", 400},
		{"oversized body", http.MethodPost, token, `{"email":"other@example.com"}`, 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/employees/get-details?email=other@example.com", strings.NewReader(tt.body))
			if tt.token != "" {
				r.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			b.GetDetails(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body.String())
			}
			if tt.status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("missing bearer challenge: %v", w.Header())
			}
			if tt.status != 200 {
				return
			}
			wantHeaders := http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
			if !reflect.DeepEqual(w.Header(), wantHeaders) {
				t.Fatalf("headers = %v, want %v", w.Header(), wantHeaders)
			}
			want := `{"id":27,"email":"operator@example.com","full_name":"","status":"active","org":{"id":1,"name":"Bedrock"},"devices":[{"id":42,"device_type":"ios","fingerprint":"42","public_key":"key-42","status":"approved"},{"id":101,"device_type":"ios","fingerprint":"101","public_key":"key-101","status":"approved"}],"role_names":[],"roles":[]}` + "\n"
			if got := w.Body.String(); got != want {
				t.Fatalf("body = %s, want %s", got, want)
			}
		})
	}
}
