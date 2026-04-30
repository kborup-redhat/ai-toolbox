package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/kborup-redhat/ai-toolbox/internal/loadtest"
)

func main() {
	runner := loadtest.NewRunner()

	mux := http.NewServeMux()

	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		var cfg loadtest.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := runner.Start(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "started"})
	})

	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, _ *http.Request) {
		runner.Stop()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
	})

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(runner.GetStats())
	})

	log.Println("Load test runner starting on :8090")
	if err := http.ListenAndServe(":8090", mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
