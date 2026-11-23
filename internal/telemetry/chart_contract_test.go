package telemetry

import (
	"bytes"
	"context"
	"io"
	"k8s.io/apimachinery/pkg/util/yaml"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestChartPrivateMetricsAndSentryEgress(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm renderer not installed; CI invokes this test after setup-helm")
	}
	for _, enabled := range []bool{false, true} {
		args := []string{"template", "contract", "../../charts/among-clusters-hub", "--namespace", "among-clusters", "--set", "image.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--set", "managedAccess.enabled=true", "--set", "ciliumPolicy.enabled=true"}
		if enabled {
			args = append(args, "--set", "sentry.enabled=true", "--set", "sentry.dsn=https://public@example.invalid/1", "--set", "sentry.release=among-clusters@0123456789abcdef0123456789abcdef01234567")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
		services, policies := 0, 0
		for {
			var o map[string]any
			err = decoder.Decode(&o)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(o) == 0 {
				continue
			}
			kind, _ := o["kind"].(string)
			meta := o["metadata"].(map[string]any)
			spec, _ := o["spec"].(map[string]any)
			switch kind {
			case "Service":
				name := meta["name"].(string)
				if name != "among-clusters-hub" && name != "among-clusters-credential-broker" {
					continue
				}
				services++
				if meta["labels"].(map[string]any)["app.kubernetes.io/name"] != name {
					t.Fatal("scrape service selector missing")
				}
				metrics := false
				for _, p := range spec["ports"].([]any) {
					port := p.(map[string]any)
					if port["name"] == "metrics" {
						metrics = true
						if port["port"] != float64(9090) || port["targetPort"] != "metrics" {
							t.Fatal("metrics named port differs")
						}
					}
				}
				if !metrics {
					t.Fatal("metrics port absent")
				}
			case "NetworkPolicy":
				name := meta["name"].(string)
				if name != "among-clusters-hub" && name != "among-clusters-credential-broker" {
					continue
				}
				policies++
				broad := 0
				for _, e := range spec["egress"].([]any) {
					rule := e.(map[string]any)
					destinations, _ := rule["to"].([]any)
					cidrs := map[string]bool{}
					for _, d := range destinations {
						ip, _ := d.(map[string]any)["ipBlock"].(map[string]any)
						if ip != nil {
							cidrs[ip["cidr"].(string)] = true
						}
					}
					if cidrs["0.0.0.0/0"] && cidrs["::/0"] {
						broad++
						ports := rule["ports"].([]any)
						if len(ports) != 1 || ports[0].(map[string]any)["port"] != float64(443) || ports[0].(map[string]any)["protocol"] != "TCP" {
							t.Fatal("Sentry egress wider than HTTPS443")
						}
					}
				}
				expected := 0
				if enabled {
					expected = 1
				}
				if broad != expected {
					t.Fatal("enabled/disabled Sentry egress differs")
				}
				metrics := false
				for _, r := range spec["ingress"].([]any) {
					rule := r.(map[string]any)
					for _, p := range rule["ports"].([]any) {
						if p.(map[string]any)["port"] != float64(9090) {
							continue
						}
						metrics = true
						peers := rule["from"].([]any)
						if len(peers) != 1 {
							t.Fatal("metrics ingress broad")
						}
						peer := peers[0].(map[string]any)
						ns := peer["namespaceSelector"].(map[string]any)["matchLabels"].(map[string]any)
						pods := peer["podSelector"].(map[string]any)["matchLabels"].(map[string]any)
						if ns["kubernetes.io/metadata.name"] != "observability-system" || pods["app.kubernetes.io/name"] != "vmagent" || pods["app.kubernetes.io/instance"] != "re8ch" {
							t.Fatal("metrics ingress not exact scraper")
						}
					}
				}
				if !metrics {
					t.Fatal("private scraper rule absent")
				}
			case "HTTPRoute":
				for _, r := range spec["rules"].([]any) {
					backends, _ := r.(map[string]any)["backendRefs"].([]any)
					for _, b := range backends {
						if b.(map[string]any)["port"] == float64(9090) {
							t.Fatal("metrics published through public route")
						}
					}
				}
			}
		}
		if services != 2 || policies != 2 {
			t.Fatalf("changed existing object cohort: services=%d policies=%d", services, policies)
		}
	}
}

func TestNativeCrossCompilationContract(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{"FROM --platform=$BUILDPLATFORM golang:", "ARG TARGETOS", "ARG TARGETARCH", "CGO_ENABLED=0", "GOOS=\"$TARGETOS\"", "GOARCH=\"$TARGETARCH\"", "FROM gcr.io/distroless/static-debian12:nonroot"} {
		if !strings.Contains(source, required) {
			t.Fatalf("native BuildKit contract missing %s", required)
		}
	}
	for _, binary := range []string{"hub", "agent", "gateway", "broker", "migrate"} {
		if !strings.Contains(source, "./cmd/"+binary) || !strings.Contains(source, "COPY --from=build /out/"+binary+" /"+binary) {
			t.Fatalf("artifact binary missing: %s", binary)
		}
	}
}
