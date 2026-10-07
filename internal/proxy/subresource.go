package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrDuplicateSubresource = errors.New("subresource already registered")

type SubresourceHandler func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request)

type subresourceKey struct {
	kind string
	name string
}

type Subresources struct {
	mu       sync.RWMutex
	handlers map[subresourceKey]SubresourceHandler
}

func NewSubresources() *Subresources {
	return &Subresources{handlers: map[subresourceKey]SubresourceHandler{}}
}

func (s *Subresources) Register(kind, name string, h SubresourceHandler) error {
	if kind == "" || name == "" || h == nil {
		return fmt.Errorf("register subresource %q of %q: kind, name and handler are required", name, kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := subresourceKey{kind: kind, name: name}
	if _, found := s.handlers[key]; found {
		return fmt.Errorf("register subresource %q of %q: %w", name, kind, ErrDuplicateSubresource)
	}
	s.handlers[key] = h
	return nil
}

func (s *Subresources) Lookup(kind, name string) (SubresourceHandler, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, found := s.handlers[subresourceKey{kind: kind, name: name}]
	return h, found
}
