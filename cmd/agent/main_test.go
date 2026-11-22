package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/re8ch/among-clusters/internal/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAdvertisedServicesRequireExplicitContract(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "tenant", Labels: map[string]string{"peering.re8ch.com/advertise": "true"}, Annotations: map[string]string{"peering.re8ch.com/protocol": "kubernetes-api", "peering.re8ch.com/service-class": "kubernetes.control-plane", "peering.re8ch.com/policy-ref": "managed-api", "peering.re8ch.com/target-peers": "re8ch-qwen", "peering.re8ch.com/ttl-seconds": "120"}}, Spec: corev1.ServiceSpec{ClusterIP: "10.43.0.1", Ports: []corev1.ServicePort{{Name: "https", Port: 6443}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "unapproved", Namespace: "tenant", Labels: map[string]string{"peering.re8ch.com/advertise": "true"}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}},
	)
	a := &agent{client: client}
	services, err := a.advertisedServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("got %d services", len(services))
	}
	service := services[0]
	if service.Protocol != "kubernetes-api" || service.Port != 6443 || service.PolicyRef != "managed-api" {
		t.Fatalf("unexpected service: %#v", service)
	}
	if service.Generation != 1 {
		t.Fatalf("generation = %d, want normalized generation 1", service.Generation)
	}
	if service.GatewayTarget != "10.43.0.1:6443" {
		t.Fatalf("gateway target = %q, want local ClusterIP without DNS dependency", service.GatewayTarget)
	}
}

func TestManagedGrantRequiresLocalCustomerApproval(t *testing.T) {
	client := fake.NewSimpleClientset()
	a := &agent{client: client, namespace: "among-clusters"}
	grant := model.GrantInstruction{Name: "admin", Namespaces: []string{"default"}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := a.reconcileGrant(context.Background(), grant); err == nil || !strings.Contains(err.Error(), "local customer approval") {
		t.Fatalf("got %v", err)
	}
	if _, err := client.CoreV1().ServiceAccounts("among-clusters").Get(context.Background(), "among-clusters-admin", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("service account created without local approval")
	}
}

func TestManagedGrantApprovalIsBoundToGeneration(t *testing.T) {
	approval := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "among-clusters-approval-admin", Namespace: "among-clusters"}, Data: map[string]string{"grantRef": "admin", "generation": "6", "approved": "true"}}
	client := fake.NewSimpleClientset(approval)
	a := &agent{client: client, namespace: "among-clusters"}
	grant := model.GrantInstruction{Name: "admin", Generation: 7, Scope: "Cluster", ExpiresAt: time.Now().Add(time.Hour)}
	if err := a.reconcileGrant(context.Background(), grant); err == nil || !strings.Contains(err.Error(), "local customer approval invalid") {
		t.Fatalf("got %v", err)
	}
	if _, err := client.CoreV1().ServiceAccounts("among-clusters").Get(context.Background(), "among-clusters-admin", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("service account created from approval for a stale grant generation")
	}
}

func TestLoadIdentityPreservesLegacySecretType(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "identity", Namespace: "among-clusters"},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"private-key": privateKey,
			"public-key":  publicKey,
		},
	}
	client := fake.NewSimpleClientset(legacy)
	a := &agent{client: client, clusterID: "qwen", trustDomain: "qwen.byoc", namespace: "among-clusters", secretName: "identity"}
	if err := a.loadIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, err := client.CoreV1().Secrets("among-clusters").Get(context.Background(), "identity", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret type changed: got %q", updated.Type)
	}
	for _, key := range []string{"tls.crt", "tls.key", "bundle.pem"} {
		if len(updated.Data[key]) == 0 {
			t.Fatalf("missing migrated key %q", key)
		}
	}
}

func TestSyncIntervalIsBounded(t *testing.T) {
	t.Setenv("SYNC_INTERVAL", "5m")
	if got := envDuration("SYNC_INTERVAL", time.Minute); got != 5*time.Minute {
		t.Fatalf("interval = %s", got)
	}
	t.Setenv("SYNC_INTERVAL", "1s")
	if got := envDuration("SYNC_INTERVAL", time.Minute); got != time.Minute {
		t.Fatalf("unsafe interval = %s", got)
	}
}

func TestPublicIngressCopiedOnlyFromReviewedServiceAnnotation(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "tenant", Labels: map[string]string{"peering.re8ch.com/advertise": "true"}, Annotations: map[string]string{"peering.re8ch.com/protocol": "https", "peering.re8ch.com/service-class": "web", "peering.re8ch.com/policy-ref": "reviewed-web", "peering.re8ch.com/target-peers": "provider-consumer", "peering.re8ch.com/public-ingress": `{"protocol":"https","address":"192.0.2.10","port":443,"hostname":"canary.example.com","path":"/health","revision":"reviewed-1"}`}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8443}}}}
	a := &agent{client: fake.NewSimpleClientset(svc)}
	services, err := a.advertisedServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].PublicIngress == nil || services[0].PublicIngress.Address != "192.0.2.10" || services[0].Port != 8443 {
		t.Fatalf("unexpected contract %#v", services)
	}
	svc.Annotations["peering.re8ch.com/public-ingress"] = `{"protocol":"quic"}`
	a.client = fake.NewSimpleClientset(svc)
	if _, err = a.advertisedServices(context.Background()); err == nil {
		t.Fatal("invalid reviewed ingress accepted")
	}
}
