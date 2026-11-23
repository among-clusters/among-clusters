// Package telemetry reports fixed HTTP stages without credentials or resource data.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/getsentry/sentry-go"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const OperationHeader = "X-Re8ch-Operation-Id"

var tracePattern = regexp.MustCompile(`^[0-9a-f]{32}-[0-9a-f]{16}(?:-[01])?$`)
var operationPattern = regexp.MustCompile(`^op_(?:[0-9a-f]{32}|[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12})$`)

type key struct{}
type requestState struct {
	operation string
	eventID   string
	category  string
	mu        sync.Mutex
	failed    bool
}
type Diagnostics struct {
	client      *sentry.Client
	component   string
	release     string
	environment string
	logger      *log.Logger
}

func NewFromEnv(component string) *Diagnostics {
	d := &Diagnostics{component: component, release: os.Getenv("SENTRY_RELEASE"), environment: os.Getenv("SENTRY_ENVIRONMENT")}
	if os.Getenv("SENTRY_DSN") == "" {
		return d
	}
	client, err := sentry.NewClient(sentry.ClientOptions{Dsn: os.Getenv("SENTRY_DSN"), Release: os.Getenv("SENTRY_RELEASE"), Environment: os.Getenv("SENTRY_ENVIRONMENT"), EnableTracing: true, TracesSampleRate: 1, SampleRate: 1, SendDefaultPII: false, AttachStacktrace: true, BeforeSend: scrub, BeforeSendTransaction: scrub})
	if err != nil {
		log.Print("HTTP diagnostics configuration invalid; local operation identifiers remain enabled")
		return d
	}
	d.client = client
	return d
}
func scrub(e *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	e.Request = nil
	e.User = sentry.User{}
	e.ServerName = ""
	e.Breadcrumbs = nil
	e.Attachments = nil
	e.Modules = nil
	for name := range e.Contexts {
		if name != "trace" {
			delete(e.Contexts, name)
		}
	}
	for i := range e.Exception {
		e.Exception[i].Value = "HTTP operation failed"
		if st := e.Exception[i].Stacktrace; st != nil {
			for j := range st.Frames {
				st.Frames[j].Vars = nil
			}
		}
	}
	for _, thread := range e.Threads {
		if thread.Stacktrace != nil {
			for j := range thread.Stacktrace.Frames {
				thread.Stacktrace.Frames[j].Vars = nil
			}
		}
	}
	return e
}
func validTrace(value string) string {
	if !tracePattern.MatchString(value) {
		return ""
	}
	parts := strings.Split(value, "-")
	if parts[0] == strings.Repeat("0", 32) || parts[1] == strings.Repeat("0", 16) {
		return ""
	}
	return value
}
func OperationID(ctx context.Context) string {
	if state, ok := ctx.Value(key{}).(*requestState); ok {
		return state.operation
	}
	return ""
}
func newOperationID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return "op_" + hex.EncodeToString(id[:])
}

// Failure captures the first stage only. Error strings and recovered values are never recorded.
func Failure(ctx context.Context, stage, category string) {
	state, ok := ctx.Value(key{}).(*requestState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failed {
		return
	}
	state.failed = true
	state.category = category
	if span := sentry.SpanFromContext(ctx); span != nil {
		span.Status = sentry.SpanStatusInternalError
	}
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil || hub.Client() == nil {
		return
	}
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Message = "Among HTTP operation failed"
	event.Tags = map[string]string{"phase": stage, "error_category": category, "operation_id": state.operation}
	event.Exception = []sentry.Exception{{Type: "HTTPStageError", Value: "HTTP operation failed", Stacktrace: sentry.NewStacktrace()}}
	event.Fingerprint = []string{"among-http", stage, category}
	if id := hub.CaptureEvent(event); id != nil {
		state.eventID = string(*id)
	}
}
func category(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport"
}

type writer struct {
	http.ResponseWriter
	status     int
	writeError bool
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *writer) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *writer) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		w.writeError = true
	}
	return n, err
}
func (d *Diagnostics) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		op := r.Header.Get(OperationHeader)
		if !operationPattern.MatchString(op) {
			op = newOperationID()
		}
		hub := sentry.NewHub(d.client, sentry.NewScope())
		hub.Scope().SetTag("component", d.component)
		hub.Scope().SetTag("operation_id", op)
		ctx := sentry.SetHubOnContext(r.Context(), hub)
		span := sentry.StartTransaction(ctx, "among.http", sentry.WithOpName("http.server"), sentry.ContinueFromHeaders(validTrace(r.Header.Get("sentry-trace")), ""), sentry.WithSpanSampled(sentry.SampledTrue))
		span.SetTag("component", d.component)
		span.SetTag("operation_id", op)
		state := &requestState{operation: op}
		ctx = context.WithValue(span.Context(), key{}, state)
		out := &writer{ResponseWriter: w}
		out.Header().Set(OperationHeader, op)
		start := time.Now()
		defer func() {
			recovered := recover()
			if recovered != nil {
				Failure(ctx, "http_entry", "panic")
			} else if out.writeError {
				Failure(ctx, "http_response", "delivery")
			} else if out.status >= 400 {
				Failure(ctx, "http_response", "http_error")
			} else if ctx.Err() != nil {
				Failure(ctx, "http_response", category(ctx.Err()))
			}
			span.SetData("http_status", out.status)
			span.SetData("duration_ms", time.Since(start).Milliseconds())
			state.mu.Lock()
			failed := state.failed
			errorCategory, eventID := state.category, state.eventID
			state.mu.Unlock()
			if failed {
				span.Status = sentry.SpanStatusInternalError
			} else {
				span.Status = sentry.SpanStatusOK
			}
			span.Finish()
			if errorCategory == "" {
				errorCategory = "none"
			}
			d.logCompletion(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "traceId": span.TraceID.String(), "operationId": op, "sentryEventId": eventID, "release": d.release, "component": d.component, "environment": d.environment, "errorCategory": errorCategory, "durationMs": time.Since(start).Milliseconds()})
			if recovered != nil {
				panic(recovered)
			}
		}()
		next.ServeHTTP(out, r.WithContext(ctx))
	})
}

type transport struct {
	base   http.RoundTripper
	stage  string
	origin *url.URL
}

func Transport(base http.RoundTripper, stage string, origin string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	target, _ := url.Parse(origin)
	return transport{base, stage, target}
}
func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if OperationID(r.Context()) == "" {
		return t.base.RoundTrip(r)
	}
	span := sentry.StartSpan(r.Context(), "http.client", sentry.WithDescription(t.stage))
	request := r.Clone(span.Context())
	request.Header = r.Header.Clone()
	request.Header.Del("sentry-trace")
	request.Header.Del("baggage")
	request.Header.Del(OperationHeader)
	if t.origin != nil && t.origin.Host != "" && request.URL.Scheme == t.origin.Scheme && request.URL.Host == t.origin.Host {
		request.Header.Set("sentry-trace", span.ToSentryTrace())
		request.Header.Set(OperationHeader, OperationID(r.Context()))
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		Failure(span.Context(), t.stage, category(err))
		span.Status = sentry.SpanStatusInternalError
		span.Finish()
		return response, err
	}
	span.SetData("http_status", response.StatusCode)
	if response.StatusCode >= 400 {
		Failure(span.Context(), t.stage, "http_error")
		span.Status = sentry.SpanStatusInternalError
	} else {
		span.Status = sentry.SpanStatusOK
	}
	wrapped := &body{ReadCloser: response.Body, span: span, stage: t.stage}
	if stream, ok := response.Body.(io.ReadWriteCloser); ok {
		response.Body = &upgradeBody{body: wrapped, writer: stream}
	} else {
		response.Body = wrapped
	}
	return response, nil
}

type body struct {
	io.ReadCloser
	span     *sentry.Span
	stage    string
	mu       sync.Mutex
	finished bool
}

func (b *body) complete(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return
	}
	if err != nil && err != io.EOF {
		Failure(b.span.Context(), b.stage, category(err))
		b.span.Status = sentry.SpanStatusInternalError
	}
	b.finished = true
	b.span.Finish()
}
func (b *body) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.complete(err)
	}
	return n, err
}
func (b *body) Close() error { err := b.ReadCloser.Close(); b.complete(err); return err }

// Preserve ReverseProxy's existing 101/SPDY/WebSocket transport contract.
type upgradeBody struct {
	*body
	writer io.Writer
}

func (b *upgradeBody) Write(p []byte) (int, error) {
	n, err := b.writer.Write(p)
	if err != nil {
		b.complete(err)
	}
	return n, err
}

var completionLogger = log.New(os.Stdout, "", 0)

func (d *Diagnostics) logCompletion(fields map[string]any) {
	data, err := json.Marshal(fields)
	if err != nil {
		return
	}
	logger := d.logger
	if logger == nil {
		logger = completionLogger
	}
	logger.Print(string(data))
}
