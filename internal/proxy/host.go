package proxy

import (
	"context"
	"errors"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	hostSettingName = "platform.host"
	hostCacheTTL    = 10 * time.Second
)

var ErrHostNotSet = errors.New("platform.host is not set")

type HostSource struct {
	reader client.Reader
	clock  func() time.Time
	mu     sync.Mutex
	value  string
	readAt time.Time
	loaded bool
}

func NewHostSource(reader client.Reader, clock func() time.Time) *HostSource {
	return &HostSource{reader: reader, clock: clock}
}

func (h *HostSource) Host(ctx context.Context) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	if !h.loaded || now.Sub(h.readAt) >= hostCacheTTL {
		var setting v1alpha1.Setting
		if err := h.reader.Get(ctx, client.ObjectKey{Name: hostSettingName}, &setting); err != nil {
			return "", err
		}
		h.value = setting.Spec.Value
		h.readAt = now
		h.loaded = true
	}
	if h.value == "" {
		return "", ErrHostNotSet
	}
	return h.value, nil
}
