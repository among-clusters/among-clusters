package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/getsentry/sentry-go"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func collector(t *testing.T) (*Diagnostics, chan []byte) {
	t.Helper()
	events := make(chan []byte, 128)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		events <- raw
		w.WriteHeader(200)
	}))
	t.Cleanup(server.Close)
	dsn := strings.Replace(server.URL, "://", "://public@", 1) + "/1"
	client, err := sentry.NewClient(sentry.ClientOptions{Dsn: dsn, SampleRate: 1, EnableTracing: true, TracesSampleRate: 1, BeforeSend: scrub, BeforeSendTransaction: scrub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Flush(2 * time.Second); client.Close() })
	return &Diagnostics{client: client, component: "test-broker"}, events
}
func TestConcurrentRealCollectorIsolationAndOneFailure(t *testing.T) {
	d, raw := collector(t)
	calls := atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !tracePattern.MatchString(r.Header.Get("sentry-trace")) || r.Header.Get("baggage") != "" {
			t.Error("invalid propagation")
		}
		if !operationPattern.MatchString(r.Header.Get(OperationHeader)) {
			t.Error("missing operation")
		}
		w.WriteHeader(503)
		w.Write([]byte("secretcanary output"))
	}))
	defer upstream.Close()
	client := &http.Client{Transport: Transport(http.DefaultTransport, "hub_request", upstream.URL)}
	server := httptest.NewServer(d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := http.NewRequestWithContext(r.Context(), "GET", upstream.URL+"/secretcanary", nil)
		q.Header.Set("Authorization", "Bearer secretcanary")
		resp, err := client.Do(q)
		if err != nil {
			t.Error(err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		http.Error(w, "secretcanary response", 503)
	})))
	defer server.Close()
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, _ := http.NewRequest("POST", server.URL+"/secretcanary?secretcanary", strings.NewReader("secretcanary body"))
			q.Header.Set("Authorization", "Bearer secretcanary")
			q.Header.Set("sentry-trace", "12345678901234567890123456789012-1234567890123456-0")
			q.Header.Set("baggage", "sentry-user_id=secretcanary")
			q.Header.Set(OperationHeader, "secretcanary")
			resp, err := http.DefaultClient.Do(q)
			if err != nil {
				t.Error(err)
				return
			}
			if !operationPattern.MatchString(resp.Header.Get(OperationHeader)) {
				t.Error("unsafe operation accepted")
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()
	if !d.client.Flush(5 * time.Second) {
		t.Fatal("collector did not flush")
	}
	if calls.Load() != n {
		t.Fatal("replayed request")
	}
	close(raw)
	errors, traces := 0, 0
	ids := map[string]bool{}
	for envelope := range raw {
		if bytes.Contains(envelope, []byte("secretcanary")) {
			t.Fatal("sensitive value in envelope")
		}
		for _, line := range bytes.Split(envelope, []byte("\n")) {
			var event struct {
				Type     string            `json:"type"`
				EventID  string            `json:"event_id"`
				Tags     map[string]string `json:"tags"`
				Contexts map[string]struct {
					TraceID string `json:"trace_id"`
				} `json:"contexts"`
			}
			if json.Unmarshal(line, &event) != nil || event.EventID == "" || event.Contexts == nil {
				continue
			}
			if event.Contexts["trace"].TraceID != "12345678901234567890123456789012" {
				t.Fatal("trace not continued")
			}
			if event.Type == "transaction" {
				traces++
			} else {
				errors++
				if event.Tags["phase"] != "hub_request" {
					t.Fatal("first stage overwritten")
				}
				ids[event.Tags["operation_id"]] = true
			}
		}
	}
	if errors != n || traces != n || len(ids) != n {
		t.Fatalf("errors=%d traces=%d isolated=%d", errors, traces, len(ids))
	}
}
func TestStrictTraceAndOperation(t *testing.T) {
	for _, v := range []string{"secretcanary", "00000000000000000000000000000000-1234567890123456-1", "12345678901234567890123456789012-0000000000000000-1", "12345678901234567890123456789012-1234567890123456-1,evil"} {
		if validTrace(v) != "" {
			t.Fatal("unsafe trace accepted")
		}
	}
	if !operationPattern.MatchString("op_12345678-1234-1234-1234-123456789012") {
		t.Fatal("Core UUID incompatible")
	}
}
func TestCancellationNoReplayAndNoKubeTrace(t *testing.T) {
	d, _ := collector(t)
	calls := atomic.Int32{}
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("sentry-trace") != "" || r.Header.Get("baggage") != "" || r.Header.Get(OperationHeader) != "" {
			t.Error("metadata leaked to Kubernetes API")
		}
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q, _ := http.NewRequestWithContext(r.Context(), "GET", server.URL, nil)
			q.Header.Set("sentry-trace", "untrusted")
			q.Header.Set("baggage", "secretcanary")
			_, err := (&http.Client{Transport: Transport(http.DefaultTransport, "local_kubernetes", "")}).Do(q)
			if err == nil {
				t.Error("cancellation missing")
			}
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/k8s/secretcanary", nil).WithContext(ctx))
	}()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
	if calls.Load() != 1 {
		t.Fatal("business request replayed")
	}
}
func TestStreamingFlushAndPanicSemantics(t *testing.T) {
	d, _ := collector(t)
	gate := make(chan struct{})
	server := httptest.NewServer(d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("a"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		<-gate
		w.Write([]byte("b"))
	})))
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err = io.ReadFull(resp.Body, b); err != nil || b[0] != 'a' {
		t.Fatal("first chunk not delivered")
	}
	close(gate)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	func() {
		defer func() {
			if recover() != "private panic" {
				t.Error("panic semantics changed")
			}
		}()
		d.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private panic") })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}()
}
func TestInvalidConfigurationFailsOpen(t *testing.T) {
	t.Setenv("SENTRY_DSN", "not a DSN secretcanary")
	d := NewFromEnv("test")
	if d.client != nil {
		t.Fatal("bad client")
	}
	w := httptest.NewRecorder()
	d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 204 || !operationPattern.MatchString(w.Header().Get(OperationHeader)) {
		t.Fatal("telemetry blocked business")
	}
}

func TestTransportRetainsUpgradeAndInformationalResponses(t *testing.T) {
	d, _ := collector(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: fixture\r\n\r\n")
		rw.Flush()
		b, err := rw.ReadByte()
		if err == nil {
			rw.WriteByte(b)
			rw.Flush()
		}
	}))
	defer upstream.Close()
	d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := http.NewRequestWithContext(r.Context(), "GET", upstream.URL, nil)
		q.Header.Set("Connection", "Upgrade")
		q.Header.Set("Upgrade", "fixture")
		resp, err := (&http.Client{Transport: Transport(http.DefaultTransport, "remote_kubernetes", "")}).Do(q)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		stream, ok := resp.Body.(io.ReadWriteCloser)
		if !ok {
			t.Error("HTTP upgrade body lost Write")
			return
		}
		stream.Write([]byte("x"))
		b := make([]byte, 1)
		if _, err = io.ReadFull(stream, b); err != nil || b[0] != 'x' {
			t.Error("upgrade echo failed")
		}
		w.WriteHeader(204)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	server := httptest.NewServer(d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(103); w.WriteHeader(204) })))
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal("informational response suppressed final status")
	}
}

func TestHubRedirectDoesNotExportTraceToAnotherOrigin(t *testing.T) {
	d, _ := collector(t)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("sentry-trace") != "" || r.Header.Get("baggage") != "" || r.Header.Get(OperationHeader) != "" {
			t.Error("trace exported beyond reviewed Hub origin")
		}
		w.WriteHeader(204)
	}))
	defer foreign.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("sentry-trace") == "" {
			t.Error("Hub trace missing")
		}
		http.Redirect(w, r, foreign.URL, 302)
	}))
	defer hub.Close()
	d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := http.NewRequestWithContext(r.Context(), "GET", hub.URL, nil)
		resp, err := (&http.Client{Transport: Transport(http.DefaultTransport, "hub_request", hub.URL)}).Do(q)
		if err != nil {
			t.Error(err)
			return
		}
		resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}
