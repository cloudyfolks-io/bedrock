package proxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

type countingReader struct {
	client.Reader
	reads int
}

func (c *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.reads++
	return c.Reader.Get(ctx, key, obj, opts...)
}

func hostSetting(value string) *v1alpha1.Setting {
	return &v1alpha1.Setting{
		ObjectMeta: metav1.ObjectMeta{Name: "platform.host"},
		Spec:       v1alpha1.SettingSpec{Value: value},
	}
}

func fakeReader(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestHostSourceReadsTheSetting(t *testing.T) {
	source := NewHostSource(fakeReader(t, hostSetting("lab.example")), newFakeClock().Now)
	got, err := source.Host(context.Background())
	if err != nil || got != "lab.example" {
		t.Fatalf("Host() = %q, %v", got, err)
	}
}

func TestHostSourceReportsUnsetHost(t *testing.T) {
	source := NewHostSource(fakeReader(t, hostSetting("")), newFakeClock().Now)
	if _, err := source.Host(context.Background()); !errors.Is(err, ErrHostNotSet) {
		t.Fatalf("error = %v, want ErrHostNotSet", err)
	}
}

func TestHostSourceCachesForTenSeconds(t *testing.T) {
	inner := fakeReader(t, hostSetting("lab.example"))
	reader := &countingReader{Reader: inner}
	clock := newFakeClock()
	source := NewHostSource(reader, clock.Now)
	ctx := context.Background()
	if _, err := source.Host(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if _, err := source.Host(ctx); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 1 {
		t.Fatalf("reads after 5 seconds = %d, want 1", reader.reads)
	}
	var setting v1alpha1.Setting
	if err := inner.Get(ctx, client.ObjectKey{Name: "platform.host"}, &setting); err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = "other.example"
	if err := inner.Update(ctx, &setting); err != nil {
		t.Fatal(err)
	}
	clock.Advance(6 * time.Second)
	got, err := source.Host(ctx)
	if err != nil || got != "other.example" || reader.reads != 2 {
		t.Fatalf("Host() = %q, %v after %d reads", got, err, reader.reads)
	}
}
