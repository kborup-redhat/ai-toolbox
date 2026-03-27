package main

import (
	"log"
	"net/http"

	"github.com/kborup-redhat/ai-toolbox/internal/auth"
	"github.com/kborup-redhat/ai-toolbox/internal/config"
	"github.com/kborup-redhat/ai-toolbox/internal/handler"
	"github.com/kborup-redhat/ai-toolbox/internal/k8s"
	"github.com/kborup-redhat/ai-toolbox/internal/metrics"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	h, err := handler.New(cfg, version)
	if err != nil {
		log.Fatalf("Failed to create handler: %v", err)
	}

	// Setup group-based access control
	var k8sClient *k8s.Client
	if cfg.OpenShift.APIURL != "" && cfg.OpenShift.Token != "" {
		k8sClient = k8s.NewClient(cfg.OpenShift.APIURL, cfg.OpenShift.Token, cfg.OpenShift.InsecureSkipVerify)
	}
	groupChecker := auth.NewGroupChecker(k8sClient, cfg.AllowedGroupsFile)

	// Chain: metrics instrumentation -> group auth -> handler
	wrapped := metrics.InstrumentHandler(groupChecker.Middleware(h))

	// Start metrics server on port 8081
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		log.Printf("Metrics server starting on :8081")
		if err := http.ListenAndServe(":8081", mux); err != nil {
			log.Printf("Metrics server failed: %v", err)
		}
	}()

	log.Printf("AI Toolbox %s starting on %s", version, cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, wrapped); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
