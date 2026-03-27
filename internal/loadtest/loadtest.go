package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Endpoint    string `json:"endpoint"`
	ModelName   string `json:"modelName"`
	TokenSize   int    `json:"tokenSize"`
	Concurrency int    `json:"concurrency"`
}

type Stats struct {
	Running        bool    `json:"running"`
	TotalRequests  int64   `json:"totalRequests"`
	Successful     int64   `json:"successful"`
	Failed         int64   `json:"failed"`
	AvgLatencyMs   float64 `json:"avgLatencyMs"`
	MinLatencyMs   float64 `json:"minLatencyMs"`
	MaxLatencyMs   float64 `json:"maxLatencyMs"`
	P50LatencyMs   float64 `json:"p50LatencyMs"`
	P99LatencyMs   float64 `json:"p99LatencyMs"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	TokensPerSec   float64 `json:"tokensPerSec"`
	ElapsedSec     float64 `json:"elapsedSec"`
	Concurrency    int     `json:"concurrency"`
	TokenSize      int     `json:"tokenSize"`
	LastError      string  `json:"lastError,omitempty"`
}

type Runner struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	startedAt time.Time
	latencies []float64

	totalReqs   atomic.Int64
	successReqs atomic.Int64
	failedReqs  atomic.Int64
	totalTokens atomic.Int64
	latencySum  atomic.Int64 // microseconds
	minLatency  atomic.Int64 // microseconds
	maxLatency  atomic.Int64 // microseconds
	lastError   atomic.Value // string

	concurrency int
	tokenSize   int
}

func NewRunner() *Runner {
	return &Runner{}
}

// validateEndpoint ensures the endpoint URL targets a cluster-internal pod IP only.
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("endpoint must use an IP address, not hostname %q", host)
	}
	if !ip.IsPrivate() && !ip.IsLoopback() {
		return fmt.Errorf("endpoint IP %s is not a private/internal address", host)
	}
	return nil
}

func (r *Runner) Start(cfg Config) error {
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return fmt.Errorf("load test already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.running = true
	r.latencies = nil
	r.startedAt = time.Now()
	r.concurrency = cfg.Concurrency
	r.tokenSize = cfg.TokenSize
	r.totalReqs.Store(0)
	r.successReqs.Store(0)
	r.failedReqs.Store(0)
	r.totalTokens.Store(0)
	r.latencySum.Store(0)
	r.minLatency.Store(math.MaxInt64)
	r.maxLatency.Store(0)
	r.lastError.Store("")

	client := &http.Client{Timeout: 120 * time.Second}

	for i := 0; i < cfg.Concurrency; i++ {
		go r.worker(ctx, client, cfg)
	}

	return nil
}

func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.running = false
}

func (r *Runner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *Runner) GetStats() Stats {
	r.mu.Lock()
	startedAt := r.startedAt
	concurrency := r.concurrency
	tokenSize := r.tokenSize
	running := r.running
	lats := make([]float64, len(r.latencies))
	copy(lats, r.latencies)
	r.mu.Unlock()

	total := r.totalReqs.Load()
	success := r.successReqs.Load()
	failed := r.failedReqs.Load()
	tokens := r.totalTokens.Load()
	latSumUs := r.latencySum.Load()
	minUs := r.minLatency.Load()
	maxUs := r.maxLatency.Load()

	elapsed := time.Since(startedAt).Seconds()

	s := Stats{
		Running:       running,
		TotalRequests: total,
		Successful:    success,
		Failed:        failed,
		ElapsedSec:    elapsed,
		Concurrency:   concurrency,
		TokenSize:     tokenSize,
	}

	if total > 0 {
		s.AvgLatencyMs = float64(latSumUs) / float64(total) / 1000.0
	}
	if minUs < math.MaxInt64 {
		s.MinLatencyMs = float64(minUs) / 1000.0
	}
	if maxUs > 0 {
		s.MaxLatencyMs = float64(maxUs) / 1000.0
	}
	if elapsed > 0 {
		s.RequestsPerSec = float64(success) / elapsed
		s.TokensPerSec = float64(tokens) / elapsed
	}

	if v := r.lastError.Load(); v != nil {
		if errStr, ok := v.(string); ok {
			s.LastError = errStr
		}
	}

	if len(lats) > 0 {
		sort.Float64s(lats)
		s.P50LatencyMs = lats[len(lats)*50/100]
		idx99 := len(lats)*99/100
		if idx99 >= len(lats) {
			idx99 = len(lats) - 1
		}
		s.P99LatencyMs = lats[idx99]
	}

	return s
}

func (r *Runner) worker(ctx context.Context, client *http.Client, cfg Config) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		r.sendRequest(ctx, client, cfg)
	}
}

type completionRequest struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	MaxTokens int    `json:"max_tokens"`
}

type completionResponse struct {
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (r *Runner) sendRequest(ctx context.Context, client *http.Client, cfg Config) {
	reqBody := completionRequest{
		Model:     cfg.ModelName,
		Prompt:    "Write a detailed explanation about artificial intelligence and its applications in modern technology. Cover machine learning, deep learning, natural language processing, and computer vision.",
		MaxTokens: cfg.TokenSize,
	}

	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.totalReqs.Add(1)
		r.failedReqs.Add(1)
		r.lastError.Store(err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	latUs := time.Since(start).Microseconds()

	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.totalReqs.Add(1)
		r.failedReqs.Add(1)
		r.lastError.Store(err.Error())
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	r.totalReqs.Add(1)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.successReqs.Add(1)
		var cr completionResponse
		if json.Unmarshal(body, &cr) == nil && cr.Usage.CompletionTokens > 0 {
			r.totalTokens.Add(int64(cr.Usage.CompletionTokens))
		} else {
			r.totalTokens.Add(int64(cfg.TokenSize))
		}
	} else {
		r.failedReqs.Add(1)
		errMsg := string(body)
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		r.lastError.Store(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, errMsg))
		return
	}

	// Record latency
	r.latencySum.Add(latUs)
	for {
		old := r.minLatency.Load()
		if latUs >= old || r.minLatency.CompareAndSwap(old, latUs) {
			break
		}
	}
	for {
		old := r.maxLatency.Load()
		if latUs <= old || r.maxLatency.CompareAndSwap(old, latUs) {
			break
		}
	}

	latMs := float64(latUs) / 1000.0
	r.mu.Lock()
	r.latencies = append(r.latencies, latMs)
	r.mu.Unlock()
}
