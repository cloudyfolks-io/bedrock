package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func replying(status int) SubresourceHandler {
	return func(_ context.Context, _ client.Client, _ client.ObjectKey, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}

func statusOf(t *testing.T, h SubresourceHandler) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	h(context.Background(), nil, client.ObjectKey{Namespace: "default", Name: "web"}, recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	return recorder.Code
}

func TestRegisterThenLookup(t *testing.T) {
	registry := NewSubresources()
	if err := registry.Register("virtualmachines", "start", replying(http.StatusAccepted)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("virtualmachines", "console/vnc", replying(http.StatusSwitchingProtocols)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("volumes", "start", replying(http.StatusTeapot)); err != nil {
		t.Fatal(err)
	}
	start, found := registry.Lookup("virtualmachines", "start")
	if !found || statusOf(t, start) != http.StatusAccepted {
		t.Fatal("start of virtualmachines must return its own handler")
	}
	console, found := registry.Lookup("virtualmachines", "console/vnc")
	if !found || statusOf(t, console) != http.StatusSwitchingProtocols {
		t.Fatal("a subresource name may hold a slash")
	}
	other, found := registry.Lookup("volumes", "start")
	if !found || statusOf(t, other) != http.StatusTeapot {
		t.Fatal("the same name under another kind must stay separate")
	}
}

func TestLookupMissing(t *testing.T) {
	registry := NewSubresources()
	if err := registry.Register("virtualmachines", "start", replying(http.StatusOK)); err != nil {
		t.Fatal(err)
	}
	for _, probe := range [][2]string{{"virtualmachines", "stop"}, {"volumes", "start"}, {"", ""}} {
		if _, found := registry.Lookup(probe[0], probe[1]); found {
			t.Fatalf("lookup %v must not find a handler", probe)
		}
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	registry := NewSubresources()
	if err := registry.Register("virtualmachines", "start", replying(http.StatusOK)); err != nil {
		t.Fatal(err)
	}
	err := registry.Register("virtualmachines", "start", replying(http.StatusAccepted))
	if !errors.Is(err, ErrDuplicateSubresource) {
		t.Fatalf("want a duplicate error, got %v", err)
	}
	first, _ := registry.Lookup("virtualmachines", "start")
	if statusOf(t, first) != http.StatusOK {
		t.Fatal("a rejected duplicate must keep the first handler")
	}
}

func TestRegisterRejectsIncompleteInput(t *testing.T) {
	registry := NewSubresources()
	for name, call := range map[string]func() error{
		"empty kind":  func() error { return registry.Register("", "start", replying(http.StatusOK)) },
		"empty name":  func() error { return registry.Register("virtualmachines", "", replying(http.StatusOK)) },
		"nil handler": func() error { return registry.Register("virtualmachines", "start", nil) },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if _, found := registry.Lookup("virtualmachines", "start"); found {
		t.Fatal("a rejected registration must leave no handler")
	}
}
