package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

type StreamOptions struct {
	Context         context.Context
	Clock           func() time.Time
	WatchTimeout    time.Duration
	Backoff         []time.Duration
	SessionCheck    time.Duration
	PingInterval    time.Duration
	PongWait        time.Duration
	SendQueue       int
	WriteWait       time.Duration
	SendBytes       int64
	GlobalSendBytes int64
	MaxConnections  int
}

type streamHub struct {
	upstream     *Upstream
	sessions     SessionSource
	opts         StreamOptions
	mu           sync.Mutex
	users        map[string]int
	total        int
	overloads    atomic.Int32
	globalQueued atomic.Int64
}

type closeRequest struct {
	code int
	text string
}

type subscription struct {
	frame   ClientFrame
	key     string
	ctx     context.Context
	cancel  context.CancelFunc
	stopped bool
}

type streamConn struct {
	hub     *streamHub
	ws      *websocket.Conn
	cookie  string
	ctx     context.Context
	cancel  context.CancelFunc
	out     chan ServerFrame
	queued  atomic.Int64
	closing chan closeRequest
	wg      sync.WaitGroup
	mu      sync.Mutex
	subs    map[string]*subscription
}

func StreamHandler(u *Upstream, sessions SessionSource, opts StreamOptions) http.Handler {
	return http.HandlerFunc(newStreamHub(u, sessions, opts).serve)
}

func newStreamHub(u *Upstream, sessions SessionSource, opts StreamOptions) *streamHub {
	return &streamHub{upstream: u, sessions: sessions, opts: streamDefaults(opts), users: map[string]int{}}
}

func streamDefaults(opts StreamOptions) StreamOptions {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.WatchTimeout == 0 {
		opts.WatchTimeout = defaultWatchTimeout
	}
	if opts.Backoff == nil {
		opts.Backoff = defaultBackoff
	}
	if opts.SessionCheck == 0 {
		opts.SessionCheck = defaultSessionCheck
	}
	if opts.PingInterval == 0 {
		opts.PingInterval = pingInterval
	}
	if opts.PongWait == 0 {
		opts.PongWait = pongWait
	}
	if opts.SendQueue == 0 {
		opts.SendQueue = sendQueue
	}
	if opts.SendBytes == 0 {
		opts.SendBytes = sendBytes
	}
	if opts.GlobalSendBytes == 0 {
		opts.GlobalSendBytes = globalSendBytes
	}
	if opts.MaxConnections == 0 {
		opts.MaxConnections = maxStreamConnections
	}
	if opts.WriteWait == 0 {
		opts.WriteWait = writeWait
	}
	return opts
}

func (h *streamHub) serve(w http.ResponseWriter, r *http.Request) {
	caller, found := CallerFrom(r.Context())
	cookie, cookieErr := r.Cookie(CookieSession)
	if !found || caller.Principal == nil || cookieErr != nil {
		httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	subject := caller.Principal.Subject
	switch h.acquire(subject) {
	case acquireUserLimit:
		httpjson.WriteError(w, http.StatusTooManyRequests, "rate_limited")
		return
	case acquireGlobalLimit:
		httpjson.WriteError(w, http.StatusServiceUnavailable, "rate_limited")
		return
	}
	defer h.release(subject)
	upgrader := websocket.Upgrader{Subprotocols: []string{StreamSubprotocol}, CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(h.opts.Context)
	conn := &streamConn{
		hub: h, ws: ws, cookie: cookie.Value, ctx: ctx, cancel: cancel,
		out: make(chan ServerFrame, h.opts.SendQueue), closing: make(chan closeRequest, 1), subs: map[string]*subscription{},
	}
	conn.run(*caller.Principal)
}

type acquireResult int

const (
	acquired acquireResult = iota
	acquireUserLimit
	acquireGlobalLimit
)

func (h *streamHub) acquire(subject string) acquireResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= h.opts.MaxConnections {
		return acquireGlobalLimit
	}
	if h.users[subject] >= maxConnectionsPerUser {
		return acquireUserLimit
	}
	h.users[subject]++
	h.total++
	return acquired
}

func (h *streamHub) release(subject string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total--
	if h.users[subject]--; h.users[subject] <= 0 {
		delete(h.users, subject)
	}
}

func (c *streamConn) run(principal Principal) {
	defer c.cancel()
	c.enqueue(ServerFrame{Type: "hello", ExpiresAt: principal.ExpiresAt.UTC().Format(time.RFC3339), Limits: &StreamLimits{MaxSubscriptions: maxSubscriptions}})
	writerDone := make(chan struct{})
	go c.write(writerDone)
	go c.watchSession()
	go c.watchShutdown()
	c.read()
	<-writerDone
	c.drain()
	_ = c.ws.Close()
	c.hub.globalQueued.Add(-c.queued.Swap(0))
	c.cancel()
	c.wg.Wait()
}

func (c *streamConn) close(code int, text string) {
	select {
	case c.closing <- closeRequest{code: code, text: text}:
	default:
	}
}

func frameSize(f ServerFrame) int64 {
	return int64(len(f.Object) + len(f.ID) + len(f.Message) + len(f.Event) + len(f.ResourceVersion) + frameOverhead)
}

func (c *streamConn) enqueue(frame ServerFrame) {
	size := frameSize(frame)
	if len(c.out) > 0 && c.queued.Load()+size > c.hub.opts.SendBytes || c.hub.globalQueued.Load()+size > c.hub.opts.GlobalSendBytes {
		c.overloaded()
		return
	}
	select {
	case c.out <- frame:
		c.queued.Add(size)
		c.hub.globalQueued.Add(size)
	default:
		c.overloaded()
	}
}

func (c *streamConn) overloaded() {
	c.hub.overloads.Add(1)
	c.close(CloseOverloaded, "client reads too slowly")
}

func (c *streamConn) emit(sub *subscription, frame ServerFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !sub.stopped {
		c.enqueue(frame)
	}
}

func (c *streamConn) finish(sub *subscription, frame ServerFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs[sub.frame.ID] == sub {
		delete(c.subs, sub.frame.ID)
	}
	if !sub.stopped {
		c.enqueue(frame)
	}
}

func (c *streamConn) write(done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(c.hub.opts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case frame := <-c.out:
			c.queued.Add(-frameSize(frame))
			c.hub.globalQueued.Add(-frameSize(frame))
			_ = c.ws.SetWriteDeadline(time.Now().Add(c.hub.opts.WriteWait))
			if err := c.ws.WriteJSON(frame); err != nil {
				c.cancel()
				_ = c.ws.Close()
				return
			}
		case <-ticker.C:
			_ = c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.hub.opts.WriteWait))
		case request := <-c.closing:
			c.cancel()
			if request.code != 0 {
				_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(request.code, request.text), time.Now().Add(c.hub.opts.WriteWait))
				_ = c.ws.SetReadDeadline(time.Now().Add(c.hub.opts.WriteWait))
			}
			return
		}
	}
}

func (c *streamConn) drain() {
	_ = c.ws.SetReadDeadline(time.Now().Add(c.hub.opts.WriteWait))
	for {
		_, reader, err := c.ws.NextReader()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, reader)
	}
}

func (c *streamConn) watchShutdown() {
	select {
	case <-c.hub.opts.Context.Done():
		c.close(CloseGoingAway, "server shutting down")
	case <-c.ctx.Done():
	}
}

func (c *streamConn) watchSession() {
	ticker := time.NewTicker(c.hub.opts.SessionCheck)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			_, err := c.hub.sessions.Resolve(c.ctx, c.cookie)
			if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrUnauthenticated) {
				c.close(CloseSessionEnded, "session ended")
				return
			}
		}
	}
}

func (c *streamConn) read() {
	defer c.close(0, "")
	c.ws.SetReadLimit(readHardLimit)
	_ = c.ws.SetReadDeadline(time.Now().Add(c.hub.opts.PongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(c.hub.opts.PongWait))
	})
	for {
		kind, reader, err := c.ws.NextReader()
		if err != nil {
			return
		}
		if kind != websocket.TextMessage {
			c.close(CloseProtocol, "text frames only")
			return
		}
		payload, err := io.ReadAll(io.LimitReader(reader, clientFrameLimit+1))
		if err != nil {
			return
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(c.hub.opts.PongWait))
		var frame ClientFrame
		if len(payload) > clientFrameLimit || json.Unmarshal(payload, &frame) != nil {
			c.close(CloseProtocol, "malformed frame")
			return
		}
		if !c.handle(frame) {
			c.close(CloseProtocol, "unknown operation")
			return
		}
	}
}

func (c *streamConn) handle(frame ClientFrame) bool {
	switch frame.Op {
	case "subscribe":
		c.subscribe(frame)
	case "unsubscribe":
		c.unsubscribe(frame.ID)
	case "ping":
		c.enqueue(ServerFrame{Type: "pong"})
	default:
		return false
	}
	return true
}

func (c *streamConn) subscribe(frame ClientFrame) {
	if refusal := c.refusal(frame); refusal != nil {
		c.enqueue(*refusal)
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	sub := &subscription{frame: frame, key: subscriptionKey(frame), ctx: ctx, cancel: cancel}
	c.mu.Lock()
	c.subs[frame.ID] = sub
	c.mu.Unlock()
	c.wg.Add(1)
	go c.watch(sub)
}

func (c *streamConn) refusal(frame ClientFrame) *ServerFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	var refused ServerFrame
	switch {
	case !idPattern.MatchString(frame.ID):
		refused = errorFrame(frame.ID, "invalid", "the id is not valid")
	case c.subs[frame.ID] != nil:
		refused = errorFrame(frame.ID, "duplicate_id", "the id is in use")
	case frame.Group != v1alpha1.GroupVersion.Group:
		refused = errorFrame(frame.ID, "resource_not_allowed", "only the bedrock API group streams")
	case !validSubscription(frame):
		refused = errorFrame(frame.ID, "invalid", "the subscription fields are not valid")
	case c.hasKey(subscriptionKey(frame)):
		refused = errorFrame(frame.ID, "duplicate_subscription", "the connection already holds this subscription")
	case len(c.subs) >= maxSubscriptions:
		refused = errorFrame(frame.ID, "limit_exceeded", "too many subscriptions")
	default:
		return nil
	}
	return &refused
}

func (c *streamConn) hasKey(key string) bool {
	for _, sub := range c.subs {
		if sub.key == key {
			return true
		}
	}
	return false
}

func (c *streamConn) unsubscribe(id string) {
	c.mu.Lock()
	sub := c.subs[id]
	if sub != nil {
		sub.stopped = true
		delete(c.subs, id)
	}
	c.mu.Unlock()
	if sub != nil {
		sub.cancel()
	}
}

func (c *streamConn) watch(sub *subscription) {
	defer c.wg.Done()
	defer sub.cancel()
	last := sub.frame.ResourceVersion
	failures := 0
	announced := false
	for sub.ctx.Err() == nil {
		principal, err := c.hub.sessions.Resolve(sub.ctx, c.cookie)
		if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrUnauthenticated) {
			c.close(CloseSessionEnded, "session ended")
			return
		}
		var watcher watch.Interface
		if err == nil {
			watcher, err = c.open(sub, principal.Access, last)
		}
		if err != nil {
			if final := terminalFrame(sub.frame.ID, err); final != nil {
				c.finish(sub, *final)
				return
			}
			failures++
		} else {
			if !announced {
				announced = true
				c.emit(sub, ServerFrame{Type: "subscribed", ID: sub.frame.ID})
			}
			final, healthy := c.consume(sub, watcher, &last)
			if final != nil {
				c.finish(sub, *final)
				return
			}
			if healthy {
				failures = 0
			} else {
				failures++
			}
		}
		if failures > 0 && !c.pause(sub, failures) {
			return
		}
	}
}

func (c *streamConn) pause(sub *subscription, failures int) bool {
	backoff := c.hub.opts.Backoff
	if failures > len(backoff) {
		c.finish(sub, errorFrame(sub.frame.ID, "unavailable", "the upstream watch failed"))
		return false
	}
	select {
	case <-sub.ctx.Done():
		return false
	case <-time.After(backoff[failures-1]):
		return true
	}
}

func (c *streamConn) open(sub *subscription, token, resourceVersion string) (watch.Interface, error) {
	dyn, err := dynamic.NewForConfig(c.hub.upstream.RESTConfig(token))
	if err != nil {
		return nil, err
	}
	gvr := schema.GroupVersionResource{Group: sub.frame.Group, Version: sub.frame.Version, Resource: sub.frame.Resource}
	resource := dyn.Resource(gvr)
	var target dynamic.ResourceInterface = resource
	if sub.frame.Namespace != "" {
		target = resource.Namespace(sub.frame.Namespace)
	}
	timeout := int64(c.hub.opts.WatchTimeout.Seconds())
	return target.Watch(sub.ctx, metav1.ListOptions{
		ResourceVersion:     resourceVersion,
		LabelSelector:       sub.frame.LabelSelector,
		FieldSelector:       sub.frame.FieldSelector,
		AllowWatchBookmarks: true,
		TimeoutSeconds:      &timeout,
	})
}

func (c *streamConn) consume(sub *subscription, watcher watch.Interface, last *string) (*ServerFrame, bool) {
	defer watcher.Stop()
	started := time.Now()
	received := false
	for {
		select {
		case <-sub.ctx.Done():
			return nil, true
		case event, open := <-watcher.ResultChan():
			if !open {
				return nil, received || time.Since(started) >= healthyWatchAge
			}
			frame, final := frameOf(sub.frame.ID, event)
			if final != nil {
				return final, false
			}
			if frame == nil {
				return nil, false
			}
			received = true
			*last = frame.ResourceVersion
			c.emit(sub, *frame)
		}
	}
}

func frameOf(id string, event watch.Event) (*ServerFrame, *ServerFrame) {
	switch event.Type {
	case watch.Added, watch.Modified, watch.Deleted:
		object, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		unstructured.RemoveNestedField(object.Object, "metadata", "managedFields")
		raw, err := json.Marshal(object.Object)
		if err != nil {
			return nil, nil
		}
		return &ServerFrame{Type: "event", ID: id, Event: string(event.Type), ResourceVersion: object.GetResourceVersion(), Object: raw}, nil
	case watch.Bookmark:
		object, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		return &ServerFrame{Type: "bookmark", ID: id, ResourceVersion: object.GetResourceVersion()}, nil
	case watch.Error:
		if isGone(apierrors.FromObject(event.Object)) {
			return nil, &ServerFrame{Type: "relist", ID: id}
		}
	}
	return nil, nil
}

func terminalFrame(id string, err error) *ServerFrame {
	switch {
	case isGone(err):
		return &ServerFrame{Type: "relist", ID: id}
	case apierrors.IsForbidden(err):
		frame := errorFrame(id, "forbidden", "access to this resource is not allowed")
		return &frame
	case apierrors.IsNotFound(err):
		frame := errorFrame(id, "not_found", "the resource does not exist")
		return &frame
	case apierrors.IsBadRequest(err) || apierrors.IsInvalid(err):
		frame := errorFrame(id, "invalid", "the subscription is not valid")
		return &frame
	}
	slog.Warn("upstream watch failed", "error", err)
	return nil
}

func isGone(err error) bool {
	var status apierrors.APIStatus
	return apierrors.IsGone(err) || apierrors.IsResourceExpired(err) || errors.As(err, &status) && status.Status().Code == http.StatusGone
}
