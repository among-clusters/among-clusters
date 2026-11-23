package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/re8ch/among-clusters/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestV2AgentDoesNotInstallGrantFromAnUnversionedOrLegacyResponse(t *testing.T) {
	for _, version := range []int{0, 1} {
		_, key, _ := ed25519.GenerateKey(rand.Reader)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request model.ControlMessage
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Type != "grant.poll.v2" {
				t.Error("agent did not negotiate named-rule support")
			}
			_ = json.NewEncoder(w).Encode(model.GrantSnapshot{ProtocolVersion: version,
				Grants: []model.GrantInstruction{{Name: "must-not-install", Scope: "Cluster", ExpiresAt: time.Now().Add(time.Hour)}}})
		}))
		client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "identity", Namespace: "agent"}})
		a := &agent{client: client, namespace: "agent", secretName: "identity", key: key,
			clusterID: "owner", trustDomain: "owner.example", tenant: "tenant", endpoint: server.URL, http: server.Client()}
		if err := a.pollGrants(context.Background()); err == nil || !strings.Contains(err.Error(), "v2 protocol") {
			t.Fatalf("legacy response was not rejected: %v", err)
		}
		for _, action := range client.Actions() {
			if action.GetVerb() == "create" && action.GetResource().Resource != "secrets" {
				t.Fatalf("legacy response attempted mutation: %v", action)
			}
		}
		server.Close()
	}
}

func TestApprovedScopedGrantMaterializesExactNativeRBACBeforeTokenIssuance(t *testing.T) {
	for _, scope := range []string{"Cluster", "Namespaced"} {
		approval := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "among-clusters-approval-release", Namespace: "agent"},
			Data: map[string]string{"grantRef": "release", "generation": "7", "approved": "true"}}
		client := fake.NewSimpleClientset(approval)
		client.PrependReactor("create", "serviceaccounts", func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() == "token" {
				return true, nil, errors.New("fixture: stop before credential issuance")
			}
			return false, nil, nil
		})
		a := &agent{client: client, namespace: "agent"}
		grant := model.GrantInstruction{Name: "release", Generation: 7, Scope: scope, Namespaces: []string{"default"},
			ExpiresAt: time.Now().Add(time.Hour), Rules: []model.AccessRule{{APIGroups: []string{""},
				Resources: []string{"configmaps"}, Verbs: []string{"get", "patch"}, ResourceNames: []string{"release-canary"}}}}
		if err := a.reconcileGrant(context.Background(), grant); err == nil || !strings.Contains(err.Error(), "stop before credential") {
			t.Fatalf("unexpected reconciliation outcome: %v", err)
		}
		var names []string
		if scope == "Cluster" {
			role, err := client.RbacV1().ClusterRoles().Get(context.Background(), "among-clusters-release", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			names = role.Rules[0].ResourceNames
		} else {
			role, err := client.RbacV1().Roles("default").Get(context.Background(), "among-clusters-release", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			names = role.Rules[0].ResourceNames
		}
		if len(names) != 1 || names[0] != "release-canary" {
			t.Fatal("native RBAC lost exact object names")
		}
	}
}
