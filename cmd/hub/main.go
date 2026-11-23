package main

import (
	"context"
	"github.com/re8ch/among-clusters/internal/telemetry"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/re8ch/among-clusters/internal/api"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	diagnostics := telemetry.NewFromEnv("among-hub")
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	cfg.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return telemetry.Transport(base, "local_kubernetes", "")
	})
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	address := os.Getenv("LISTEN_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	server := &api.SovereignServer{Store: &api.KubernetesSovereignStore{Dynamic: client, Core: core}, AdminToken: os.Getenv("ADMIN_TOKEN")}
	interval := 60 * time.Second
	if value := os.Getenv("RECONCILE_INTERVAL"); value != "" {
		if parsed, parseErr := time.ParseDuration(value); parseErr == nil && parsed >= 15*time.Second {
			interval = parsed
		}
	}
	go (&api.SovereignReconciler{Client: client, Core: core, ManagedAccessEnabled: api.ManagedAccessFromEnv(), Interval: interval}).Run(context.Background())
	log.Printf("AmongClusters hub listening on %s", address)
	log.Fatal(http.ListenAndServe(address, diagnostics.Handler(server.Handler())))
}
