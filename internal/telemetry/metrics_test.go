package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestUnifiedREDAndBuildLabelsWithoutTraceSeries(t *testing.T) {
	d := &Diagnostics{component: "among-broker", release: "among-clusters@0123456789abcdef0123456789abcdef01234567", environment: "production"}
	q := httptest.NewRequest("POST", "/k8s/privatecanary/privatecanary/api", strings.NewReader("privatecanary"))
	q.Header.Set(OperationHeader, "op_12345678123412341234123456789012")
	q.Header.Set("sentry-trace", "12345678901234567890123456789012-1234567890123456-1")
	d.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Failure(r.Context(), "credential_decode", "invalid_or_expired")
		w.WriteHeader(403)
	})).ServeHTTP(httptest.NewRecorder(), q)
	families, err := d.metricsSet().registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"component", "environment", "phase", "method", "status_class", "outcome", "error_category"}
	sort.Strings(expected)
	seen := map[string]bool{}
	for _, f := range families {
		seen[f.GetName()] = true
		for _, m := range f.Metric {
			names := []string{}
			values := map[string]string{}
			for _, l := range m.Label {
				names = append(names, l.GetName())
				values[l.GetName()] = l.GetValue()
			}
			sort.Strings(names)
			if f.GetName() == "re8ch_toolchain_build_info" {
				want := []string{"component", "environment", "release", "repository"}
				if !reflect.DeepEqual(names, want) || values["repository"] != "among-clusters/among-clusters" || values["release"] != d.release {
					t.Fatal("build labels mismatch")
				}
				continue
			}
			if !reflect.DeepEqual(names, expected) {
				t.Fatalf("RED label set differs: %v", names)
			}
			if values["phase"] != "kubernetes_proxy" || values["status_class"] != "4xx" || values["outcome"] != "error" || values["error_category"] != "invalid_or_expired" {
				t.Fatal("HTTP outcome not preserved")
			}
		}
	}
	if len(seen) != 3 || !seen["re8ch_toolchain_http_requests_total"] || !seen["re8ch_toolchain_http_request_duration_seconds"] || !seen["re8ch_toolchain_build_info"] {
		t.Fatal("unified series missing")
	}
	server := httptest.NewServer(d.MetricsHandler())
	defer server.Close()
	request, _ := http.NewRequest("GET", server.URL+"/metrics", nil)
	request.Header.Set("Accept", "application/openmetrics-text; version=1.0.0; charset=utf-8")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	text := string(raw)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/openmetrics-text") || strings.Contains(text, "trace_id") || !strings.Contains(text, `le="3600.0"`) {
		t.Fatal("OpenMetrics format, trace exclusion or long-call bucket differs")
	}
	for _, private := range []string{"privatecanary", "op_12345678123412341234123456789012"} {
		if strings.Contains(text, private) {
			t.Fatal("request data became a metric label")
		}
	}
}
func TestREDNoneStatusCancellationPanicAndNormalImplicit200(t *testing.T) {
	d := &Diagnostics{component: "among-hub", environment: "test"}
	d.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/identities/private", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PRIVATE", "/private", nil).WithContext(ctx))
	func() {
		defer func() {
			if recover() != "private panic" {
				t.Fatal("panic changed")
			}
		}()
		d.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private panic") })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/peers/private", nil))
	}()
	f, _ := d.metricsSet().registry.Gather()
	found := map[string]bool{}
	for _, family := range f {
		if family.GetName() != "re8ch_toolchain_http_requests_total" {
			continue
		}
		for _, m := range family.Metric {
			labels := map[string]string{}
			for _, l := range m.Label {
				labels[l.GetName()] = l.GetValue()
			}
			switch labels["error_category"] {
			case "none":
				if labels["status_class"] != "2xx" || labels["outcome"] != "success" {
					t.Fatal("implicit actual200 lost")
				}
			case "canceled", "panic":
				if labels["status_class"] != "none" || labels["outcome"] != "error" {
					t.Fatal("invented status or outcome")
				}
			}
			found[labels["error_category"]] = true
		}
	}
	if len(found) != 3 {
		t.Fatal("missing classification")
	}
}
func TestMetricsBindingNeverWildcardAndPublicMuxUnchanged(t *testing.T) {
	for _, test := range []struct{ ip, want string }{{"", "127.0.0.1:9090"}, {"0.0.0.0", "127.0.0.1:9090"}, {"::", "127.0.0.1:9090"}, {"privatecanary", "127.0.0.1:9090"}, {"10.250.0.128", "10.250.0.128:9090"}, {"fd00::1", "[fd00::1]:9090"}} {
		if metricsAddress(test.ip) != test.want {
			t.Fatalf("bind differs for %q", test.ip)
		}
	}
	d := &Diagnostics{component: "among-hub"}
	app := http.NewServeMux()
	app.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	out := httptest.NewRecorder()
	d.Handler(app).ServeHTTP(out, httptest.NewRequest("GET", "/metrics", nil))
	if out.Code != 404 || strings.Contains(out.Body.String(), "re8ch_toolchain_") {
		t.Fatal("metrics exposed on application mux")
	}
}
func TestLongCallHistogramHasUnifiedBucketAndBoundedFallbacks(t *testing.T) {
	d := &Diagnostics{component: "among-broker", release: "privatecanary", environment: "privatecanary"}
	d.observeHTTP(httptest.NewRequest("PRIVATE", "/privatecanary", nil), 0, true, "privatecanary", 70*time.Second)
	families, _ := d.metricsSet().registry.Gather()
	for _, family := range families {
		for _, m := range family.Metric {
			for _, label := range m.Label {
				if strings.Contains(label.GetValue(), "privatecanary") {
					t.Fatal("unbounded value escaped")
				}
			}
			if family.GetName() == "re8ch_toolchain_http_request_duration_seconds" {
				if m.Histogram.GetSampleCount() != 1 || m.Histogram.GetSampleSum() != 70 {
					t.Fatal("long-call measurement changed")
				}
				found := false
				for _, bucket := range m.Histogram.Bucket {
					if bucket.GetUpperBound() == 120 && bucket.GetCumulativeCount() == 1 {
						found = true
					}
				}
				if !found {
					t.Fatal("70s call not in120s bucket")
				}
			}
		}
	}
}
