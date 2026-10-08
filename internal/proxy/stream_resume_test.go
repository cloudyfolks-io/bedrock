package proxy

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const expiredStatus = `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Expired","code":410,"message":"too old resource version"}`

func TestStreamResumesFromTheResourceVersion(t *testing.T) {
	front, _ := realStream(t, StreamOptions{})
	newSetting(t, "resume-a")
	second := newSetting(t, "resume-b")
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", second.ResourceVersion))
	expectFrame(t, conn, "subscribed")
	newSetting(t, "resume-c")
	for {
		event := expectFrame(t, conn, "event")
		if strings.Contains(string(event.Object), `"resume-a"`) || strings.Contains(string(event.Object), `"resume-b"`) {
			t.Fatalf("resume replayed an old event: %s", event.Object)
		}
		if strings.Contains(string(event.Object), `"resume-c"`) && event.Event == "ADDED" {
			return
		}
	}
}

func TestStreamRelistsOn410(t *testing.T) {
	var expired atomic.Bool
	expired.Store(true)
	front, _, _ := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		if expired.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(expiredStatus))
			return
		}
		holdOpen(w, r)
	}, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "1"))
	if frame := expectFrame(t, conn, "relist"); frame.ID != "s1" {
		t.Fatalf("relist %+v", frame)
	}
	expired.Store(false)
	send(t, conn, settingsFrame("s1", "200"))
	expectFrame(t, conn, "subscribed")
}

func TestStreamRelistsOnErrorEvent410(t *testing.T) {
	front, _, _ := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		flushed(w, `{"type":"ERROR","object":`+expiredStatus+`}`+"\n")
		<-r.Context().Done()
	}, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "1"))
	expectFrame(t, conn, "subscribed")
	expectFrame(t, conn, "relist")
}

func TestStreamReopensTheUpstreamWatchQuietly(t *testing.T) {
	var requests atomic.Int32
	front, api, _ := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			flushed(w, watchLine("ADDED", "one", "11")+watchLine("MODIFIED", "one", "12"))
			return
		}
		flushed(w, watchLine("MODIFIED", "one", "13"))
		<-r.Context().Done()
	}, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "10"))
	expectFrame(t, conn, "subscribed")
	for _, want := range []string{"11", "12", "13"} {
		if event := expectFrame(t, conn, "event"); event.ResourceVersion != want {
			t.Fatalf("event %+v, want version %s", event, want)
		}
	}
	watches := watchRequests(api)
	if len(watches) != 2 {
		t.Fatalf("%d upstream watches", len(watches))
	}
	parsed, err := url.Parse(watches[1].URI)
	if err != nil || parsed.Query().Get("resourceVersion") != "12" {
		t.Fatalf("second watch %s: %v", watches[1].URI, err)
	}
}

func TestStreamUnavailableAfterThreeAttempts(t *testing.T) {
	var requests atomic.Int32
	backoff := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	front, _, _ := fakeStream(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, StreamOptions{Backoff: backoff})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "1"))
	if frame := expectFrame(t, conn, "error"); frame.Code != "unavailable" || frame.ID != "s1" {
		t.Fatalf("frame %+v", frame)
	}
	if requests.Load() != 4 {
		t.Fatalf("%d upstream requests", requests.Load())
	}
}

func TestStreamSlowConsumerIsClosed(t *testing.T) {
	big := strings.Repeat("x", 200<<10)
	emitted := make(chan struct{})
	var once sync.Once
	front, _, _, hub := fakeStreamHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for i := 0; i < 300 && r.Context().Err() == nil; i++ {
			flushed(w, strings.Replace(watchLine("ADDED", "big", "50"), `"managedFields"`, `"note":"`+big+`","managedFields"`, 1))
		}
		once.Do(func() { close(emitted) })
		<-r.Context().Done()
	}, StreamOptions{SendQueue: 2, WriteWait: time.Minute})
	slow := openStream(t, front, "alice")
	expectFrame(t, slow, "hello")
	send(t, slow, settingsFrame("s1", "1"))
	other := openStream(t, front, "alice")
	expectFrame(t, other, "hello")
	select {
	case <-emitted:
	case <-time.After(60 * time.Second):
		t.Fatal("the upstream did not finish writing")
	}
	waitUntil(t, "the proxy to evict the slow client", func() bool { return hub.overloads.Load() > 0 })
	if code := closeCode(t, slow); code != CloseOverloaded {
		t.Fatalf("close code %d", code)
	}
	send(t, other, ClientFrame{Op: "ping"})
	expectFrame(t, other, "pong")
}

func TestStreamClosesWhenTheSessionEnds(t *testing.T) {
	ended := make(chan struct{})
	var once sync.Once
	front, _, sessions := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		once.Do(func() { close(ended) })
	}, StreamOptions{SessionCheck: 50 * time.Millisecond})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "1"))
	expectFrame(t, conn, "subscribed")
	sessions.set("alice", Principal{}, ErrSessionEnded)
	if code := closeCode(t, conn); code != CloseSessionEnded {
		t.Fatalf("close code %d", code)
	}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream watch did not end")
	}
}

func TestStreamUsesAFreshTokenForReopen(t *testing.T) {
	var requests atomic.Int32
	finishFirst := make(chan struct{})
	front, api, sessions := fakeStream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			flushed(w, watchLine("ADDED", "one", "11"))
			<-finishFirst
			return
		}
		flushed(w, watchLine("MODIFIED", "one", "12"))
		<-r.Context().Done()
	}, StreamOptions{})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	send(t, conn, settingsFrame("s1", "10"))
	expectFrame(t, conn, "subscribed")
	expectFrame(t, conn, "event")
	sessions.set("alice", principalWithToken("alice", "tok-2"), nil)
	close(finishFirst)
	expectFrame(t, conn, "event")
	watches := watchRequests(api)
	if len(watches) != 2 || watches[0].Header.Get("Authorization") != "Bearer tok-1" || watches[1].Header.Get("Authorization") != "Bearer tok-2" {
		t.Fatalf("watch requests %+v", watches)
	}
}

func TestStreamShutdownClosesWith1001(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	front, _, _ := fakeStream(t, holdOpen, StreamOptions{Context: ctx})
	conn := openStream(t, front, "alice")
	expectFrame(t, conn, "hello")
	cancel()
	if code := closeCode(t, conn); code != CloseGoingAway {
		t.Fatalf("close code %d", code)
	}
}

func TestStreamNeverSharesWatchesBetweenUsers(t *testing.T) {
	front, api, _ := fakeStream(t, holdOpen, StreamOptions{})
	for _, cookie := range []string{"alice", "bob"} {
		conn := openStream(t, front, cookie)
		expectFrame(t, conn, "hello")
		send(t, conn, settingsFrame("s1", "1"))
		expectFrame(t, conn, "subscribed")
	}
	watches := watchRequests(api)
	if len(watches) != 2 || watches[0].Header.Get("Authorization") == watches[1].Header.Get("Authorization") {
		t.Fatalf("watch requests %+v", watches)
	}
}
