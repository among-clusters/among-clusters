package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func managedIngressService(now time.Time) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "owner", UID: types.UID("service-uid"), Generation: 3,
		Labels: map[string]string{"peering.re8ch.com/advertise": "true"}, Annotations: map[string]string{
			"peering.re8ch.com/protocol": "https", "peering.re8ch.com/service-class": "web", "peering.re8ch.com/policy-ref": "reviewed", "peering.re8ch.com/target-peers": "consumer",
			"peering.re8ch.com/public-ingress": `{"endpoints":[{"id":"local-a","site":"local-a","region":"region-a","protocol":"https","address":"192.0.2.10","port":443,"hostname":"api.example.com","path":"/health","revision":"service-uid-revision"}]}`,
			publicIngressOwnerAnnotation:       "owner-cluster", publicIngressSourceUIDAnnotation: "service-uid", publicIngressValidUntilAnnotation: now.Add(107 * time.Second).Format(time.RFC3339),
		}}, Spec: corev1.ServiceSpec{ClusterIP: "10.43.0.10", Ports: []corev1.ServicePort{{Port: 8443}}}}
}

func TestManagedPublicIngressSourceIdentityAndValidity(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*corev1.Service)
		want bool
	}{
		{"valid", func(*corev1.Service) {}, true},
		{"maximum-protocol-validity", func(s *corev1.Service) {
			s.Annotations[publicIngressValidUntilAnnotation] = now.Add(86400 * time.Second).Format(time.RFC3339)
		}, true},
		{"foreign-owner", func(s *corev1.Service) { s.Annotations[publicIngressOwnerAnnotation] = "foreign-cluster" }, false},
		{"empty-owner-present", func(s *corev1.Service) { s.Annotations[publicIngressOwnerAnnotation] = "" }, false},
		{"source-uid-missing", func(s *corev1.Service) { delete(s.Annotations, publicIngressSourceUIDAnnotation) }, false},
		{"source-uid-mismatch", func(s *corev1.Service) { s.Annotations[publicIngressSourceUIDAnnotation] = "other-service" }, false},
		{"service-recreated", func(s *corev1.Service) { s.UID = types.UID("new-service-uid") }, false},
		{"actual-uid-empty", func(s *corev1.Service) { s.UID = ""; s.Annotations[publicIngressSourceUIDAnnotation] = "" }, false},
		{"expiry-missing", func(s *corev1.Service) { delete(s.Annotations, publicIngressValidUntilAnnotation) }, false},
		{"expiry-malformed", func(s *corev1.Service) { s.Annotations[publicIngressValidUntilAnnotation] = "not-a-date" }, false},
		{"expiry-now", func(s *corev1.Service) { s.Annotations[publicIngressValidUntilAnnotation] = now.Format(time.RFC3339) }, false},
		{"expiry-past", func(s *corev1.Service) {
			s.Annotations[publicIngressValidUntilAnnotation] = now.Add(-time.Second).Format(time.RFC3339)
		}, false},
		{"expiry-beyond-protocol-maximum", func(s *corev1.Service) {
			s.Annotations[publicIngressValidUntilAnnotation] = now.Add(86401 * time.Second).Format(time.RFC3339)
		}, false},
		{"manual-legacy", func(s *corev1.Service) {
			delete(s.Annotations, publicIngressOwnerAnnotation)
			delete(s.Annotations, publicIngressSourceUIDAnnotation)
			delete(s.Annotations, publicIngressValidUntilAnnotation)
		}, true},
	}
	a := &agent{clusterID: "owner-cluster"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := managedIngressService(now)
			test.edit(svc)
			before := svc.DeepCopy()
			if got := a.publicIngressSourceCurrent(svc, now); got != test.want {
				t.Fatalf("valid=%v want%v", got, test.want)
			}
			if !reflect.DeepEqual(svc, before) {
				t.Fatal("source validation mutated Service")
			}
		})
	}
}

func TestBadManagedProjectionDoesNotInterruptOrdinaryServiceSnapshot(t *testing.T) {
	now := time.Now()
	for _, bad := range []string{"expired", "malformed-json", "invalid-endpoints", "validation-failed", "trailing-json", "cross-owner", "recreated"} {
		t.Run(bad, func(t *testing.T) {
			svc := managedIngressService(now)
			switch bad {
			case "expired":
				svc.Annotations[publicIngressValidUntilAnnotation] = now.Add(-time.Second).Format(time.RFC3339)
			case "malformed-json":
				svc.Annotations["peering.re8ch.com/public-ingress"] = "{"
			case "invalid-endpoints":
				svc.Annotations["peering.re8ch.com/public-ingress"] = `{"protocol":"quic"}`
			case "validation-failed":
				svc.Annotations["peering.re8ch.com/public-ingress"] = strings.Replace(svc.Annotations["peering.re8ch.com/public-ingress"], `"port":443`, `"port":0`, 1)
			case "trailing-json":
				svc.Annotations["peering.re8ch.com/public-ingress"] += " {}"
			case "cross-owner":
				svc.Annotations[publicIngressOwnerAnnotation] = "foreign-cluster"
			case "recreated":
				svc.UID = types.UID("new-service")
			}
			other := managedIngressService(now)
			other.Name = "another-service"
			a := &agent{client: fake.NewSimpleClientset(svc, other), clusterID: "owner-cluster"}
			services, err := a.advertisedServices(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(services) != 2 {
				t.Fatalf("managed error withdrew other services: %d", len(services))
			}
			for _, s := range services {
				if s.Name == "api" {
					if s.PublicIngress != nil || s.GatewayTarget != "10.43.0.10:8443" || s.Protocol != "https" || len(s.TargetPeers) != 1 {
						t.Fatalf("ordinary export was altered: %#v", s)
					}
				} else if s.PublicIngress == nil {
					t.Fatal("healthy other public ingress was withdrawn")
				}
			}
		})
	}
}

func TestManagedLeaseRenewalDoesNotChangeAdvertisementPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	svc := managedIngressService(now)
	client := fake.NewSimpleClientset(svc)
	a := &agent{client: client, clusterID: "owner-cluster"}
	before, err := a.advertisedServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := client.CoreV1().Services(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	updated.Annotations[publicIngressValidUntilAnnotation] = now.Add(122 * time.Second).Format(time.RFC3339)
	if _, err = client.CoreV1().Services(svc.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := a.advertisedServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("lease heartbeat changed advertisement specification/generation")
	}
	wire, _ := json.Marshal(after)
	if strings.Contains(string(wire), "public-ingress-valid-until") || strings.Contains(string(wire), updated.Annotations[publicIngressValidUntilAnnotation]) {
		t.Fatal("source lease leaked into advertisement payload")
	}
}
