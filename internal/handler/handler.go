package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kborup-redhat/ai-toolbox/internal/config"
	"github.com/kborup-redhat/ai-toolbox/internal/k8s"
	"github.com/kborup-redhat/ai-toolbox/internal/monitoring"
	"github.com/kborup-redhat/ai-toolbox/web"
)

var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

type Handler struct {
	mux            *http.ServeMux
	cfg            *config.Config
	k8sClient      *k8s.Client
	monClient      *monitoring.Client
	version        string
	templates      *template.Template
	runnerPodIP    string
	runnerPodName  string
	runnerMu           sync.Mutex
	loadTestUser       string
	loadTestModel      string
	loadTestTotal      int
	loadTestRunning    bool
}

type pageVars struct {
	Username string
	Version  string
}

func New(cfg *config.Config, version string) (*Handler, error) {
	h := &Handler{
		mux:     http.NewServeMux(),
		cfg:     cfg,
		version: version,
	}

	if cfg.OpenShift.APIURL != "" && cfg.OpenShift.Token != "" {
		h.k8sClient = k8s.NewClient(cfg.OpenShift.APIURL, cfg.OpenShift.Token, cfg.OpenShift.InsecureSkipVerify)
	}

	if cfg.OpenShift.ClusterDomain != "" && cfg.OpenShift.Token != "" {
		h.monClient = monitoring.NewClient(cfg.OpenShift.ClusterDomain, cfg.OpenShift.Token, cfg.OpenShift.InsecureSkipVerify)
	}

	tmplFS, err := fs.Sub(web.FS, "templates")
	if err != nil {
		return nil, err
	}
	h.templates, err = template.ParseFS(tmplFS, "*.html")
	if err != nil {
		return nil, err
	}

	staticFS, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}

	// Clean up any stale runner pod from previous deployment
	if h.k8sClient != nil {
		ns := h.getAppNamespace()
		_ = h.k8sClient.DeletePod(ns, loadTestRunnerPodName)
	}

	h.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	h.mux.HandleFunc("GET /", h.handleDashboard)
	h.mux.HandleFunc("GET /api/overview", h.handleOverview)
	h.mux.HandleFunc("GET /api/network-policies", h.handleNetworkPolicies)
	h.mux.HandleFunc("GET /api/models/list", h.handleModelList)
	h.mux.HandleFunc("GET /api/metrics", h.handleMetrics)
	h.mux.HandleFunc("GET /api/analysis", h.handleAnalysis)
	h.mux.HandleFunc("GET /api/live", h.handleLive)
	h.mux.HandleFunc("POST /api/loadtest/start", h.handleLoadTestStart)
	h.mux.HandleFunc("POST /api/loadtest/stop", h.handleLoadTestStop)
	h.mux.HandleFunc("GET /api/loadtest/status", h.handleLoadTestStatus)
	h.mux.HandleFunc("GET /api/status", h.handleStatus)

	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	vars := pageVars{
		Username: r.Header.Get("X-Forwarded-User"),
		Version:  h.version,
	}
	if err := h.templates.ExecuteTemplate(w, "dashboard.html", vars); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// --- API response types ---

type OverviewResponse struct {
	Nodes        []NodeInfo `json:"nodes"`
	Models       []ModelInfo `json:"models"`
	ConsoleURL   string      `json:"consoleURL,omitempty"`
	DashboardURL string      `json:"dashboardURL,omitempty"`
}

type NodeInfo struct {
	Name             string          `json:"name"`
	Status           string          `json:"status"`
	CPUCapacity      float64         `json:"cpuCapacity"`
	CPUAllocatable   float64         `json:"cpuAllocatable"`
	MemCapacity      int64           `json:"memCapacity"`
	MemAllocatable   int64           `json:"memAllocatable"`
	GPUCapacity      int64           `json:"gpuCapacity"`
	GPUAllocatable   int64           `json:"gpuAllocatable"`
	GPUsUsed         int64           `json:"gpusUsed"`
	GPUProduct       string          `json:"gpuProduct,omitempty"`
	GPUMemoryMB      int64           `json:"gpuMemoryMB,omitempty"`
	GPUSharing       string          `json:"gpuSharing,omitempty"`
	GPUPhysicalCount int64           `json:"gpuPhysicalCount,omitempty"`
	GPUPartitions    []GPUPartition  `json:"gpuPartitions,omitempty"`
	ModelsRunning    int             `json:"modelsRunning"`
	InternalIP       string          `json:"internalIP"`
}

// GPUPartition represents a partitioned GPU slice (MIG, SR-IOV, etc.)
type GPUPartition struct {
	Resource    string `json:"resource"`
	Capacity    int64  `json:"capacity"`
	Allocatable int64  `json:"allocatable"`
}

type ModelInfo struct {
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	Runtime       string            `json:"runtime"`
	Status        string            `json:"status"`
	URL           string            `json:"url"`
	NodeName      string            `json:"nodeName"`
	GPUsRequested int64             `json:"gpusRequested"`
	CPUsRequested float64           `json:"cpusRequested"`
	CPULimits     float64           `json:"cpuLimits"`
	MemRequested  int64             `json:"memRequested"`
	MemLimits     int64             `json:"memLimits"`
	CreatedAt     string            `json:"createdAt"`
	ModelPods     []ModelPodInfo    `json:"modelPods"`
	Labels        map[string]string `json:"labels"`
	// Model runtime metadata (from vLLM/TGI API)
	EngineVersion string `json:"engineVersion,omitempty"`
	MaxModelLen   int64  `json:"maxModelLen,omitempty"`
	ModelRoot     string `json:"modelRoot,omitempty"`
	GPUProduct    string `json:"gpuProduct,omitempty"`
}

type ModelPodInfo struct {
	Name          string  `json:"name"`
	NodeName      string  `json:"nodeName"`
	Phase         string  `json:"phase"`
	GPUsRequested int64   `json:"gpusRequested"`
	CPUsRequested float64 `json:"cpusRequested"`
	CPULimits     float64 `json:"cpuLimits"`
	MemRequested  int64   `json:"memRequested"`
	MemLimits     int64   `json:"memLimits"`
}

type NetworkPolicyReadable struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Target    string   `json:"target"`
	Types     []string `json:"types"`
	Rules     []string `json:"rules"`
}

type ModelListItem struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Runtime   string `json:"runtime"`
	Status    string `json:"status"`
}

type MetricsResponse struct {
	Model     string         `json:"model"`
	Namespace string         `json:"namespace"`
	Summary   MetricsSummary `json:"summary"`
	Series    MetricsSeries  `json:"series"`
}

type MetricsSummary struct {
	RequestsTotal   string `json:"requestsTotal"`
	RequestsPerSec  string `json:"requestsPerSec"`
	AvgLatencyMs    string `json:"avgLatencyMs"`
	P99LatencyMs    string `json:"p99LatencyMs"`
	ErrorRate       string `json:"errorRate"`
	TokensPerSec    string `json:"tokensPerSec"`
	ActiveRequests  string `json:"activeRequests"`
	GPUUtilization  string `json:"gpuUtilization"`
	KVCacheUsage    string `json:"kvCacheUsage"`
	TotalTokens     string `json:"totalTokens"`
	GPUMemoryUsed   string `json:"gpuMemoryUsed"`
	GPUMemoryTotal  string `json:"gpuMemoryTotal"`
	CPUUsage        string `json:"cpuUsage"`
	MemoryUsage     string `json:"memoryUsage"`
}

type MetricsSeries struct {
	RequestRate []SeriesPoint `json:"requestRate"`
	Latency     []SeriesPoint `json:"latency"`
	GPUUtil     []SeriesPoint `json:"gpuUtil"`
	TokenRate   []SeriesPoint `json:"tokenRate"`
}

type SeriesPoint struct {
	Timestamp int64  `json:"t"`
	Value     string `json:"v"`
}

// --- Analysis types ---

type AnalysisResponse struct {
	Model       string              `json:"model"`
	Namespace   string              `json:"namespace"`
	Performance PerformanceAnalysis `json:"performance"`
	Efficiency  EfficiencyAnalysis  `json:"efficiency"`
	Resource    ResourceAnalysis    `json:"resource"`
	Config      ModelConfig         `json:"config"`
	Scheduling  SchedulingAnalysis  `json:"scheduling"`
	Scaling     ScalingAnalysis     `json:"scaling"`
	ConfigAudit ConfigAuditAnalysis `json:"configAudit"`
	Network     NetworkAnalysis     `json:"network"`
	Health      HealthAnalysis      `json:"health"`
	Cost        CostAnalysis        `json:"cost"`
}

type PerformanceAnalysis struct {
	TTFTp50Ms        string `json:"ttftP50Ms"`
	TTFTp99Ms        string `json:"ttftP99Ms"`
	ITLp50Ms         string `json:"itlP50Ms"`
	ITLp99Ms         string `json:"itlP99Ms"`
	PrefillTimeMs    string `json:"prefillTimeMs"`
	DecodeTimeMs     string `json:"decodeTimeMs"`
	QueueWaitMs      string `json:"queueWaitMs"`
	E2ELatencyP50Ms  string `json:"e2eLatencyP50Ms"`
	E2ELatencyP99Ms  string `json:"e2eLatencyP99Ms"`
	RequestsPerSec   string `json:"requestsPerSec"`
	TokensPerSec     string `json:"tokensPerSec"`
	AvgPromptTokens  string `json:"avgPromptTokens"`
	AvgOutputTokens  string `json:"avgOutputTokens"`
}

type EfficiencyAnalysis struct {
	PrefixCacheHitRate string `json:"prefixCacheHitRate"`
	PrefixCacheHits    string `json:"prefixCacheHits"`
	PrefixCacheQueries string `json:"prefixCacheQueries"`
	KVCacheUsage       string `json:"kvCacheUsage"`
	PreemptionCount    string `json:"preemptionCount"`
	TokensPerGPU       string `json:"tokensPerGPU"`
	RequestsRunning    string `json:"requestsRunning"`
	RequestsWaiting    string `json:"requestsWaiting"`
	AvgBatchTokens     string `json:"avgBatchTokens"`
}

type ResourceAnalysis struct {
	GPUUtilization string `json:"gpuUtilization"`
	GPUMemUsed     string `json:"gpuMemUsed"`
	GPUMemTotal    string `json:"gpuMemTotal"`
	CPURequested   string `json:"cpuRequested"`
	CPULimit       string `json:"cpuLimit"`
	CPUActual      string `json:"cpuActual"`
	MemRequested   string `json:"memRequested"`
	MemLimit       string `json:"memLimit"`
	MemActual      string `json:"memActual"`
	GPUsRequested  string `json:"gpusRequested"`
}

type ModelConfig struct {
	Runtime         string            `json:"runtime"`
	ModelName       string            `json:"modelName"`
	Status          string            `json:"status"`
	URL             string            `json:"url"`
	CreatedAt       string            `json:"createdAt"`
	MaxModelLen     string            `json:"maxModelLen"`
	GPUMemUtil      string            `json:"gpuMemUtil"`
	BlockSize       string            `json:"blockSize"`
	PrefixCaching   string            `json:"prefixCaching"`
	Labels          map[string]string `json:"labels,omitempty"`
	NodeName        string            `json:"nodeName"`
	GPUProduct      string            `json:"gpuProduct"`
	Replicas        int               `json:"replicas"`
}

// --- Scheduling & Placement ---

type SchedulingAnalysis struct {
	NodeAffinities   []string `json:"nodeAffinities"`
	NodeSelectors    []string `json:"nodeSelectors"`
	Tolerations      []string `json:"tolerations"`
	PodSpread        string   `json:"podSpread"`
	PodSpreadDetail  string   `json:"podSpreadDetail"`
	GPUFragmentation []GPUFragInfo `json:"gpuFragmentation"`
}

type GPUFragInfo struct {
	NodeName      string `json:"nodeName"`
	GPUTotal      int64  `json:"gpuTotal"`
	GPUUsed       int64  `json:"gpuUsed"`
	GPUFree       int64  `json:"gpuFree"`
	GPUProduct    string `json:"gpuProduct"`
	FragmentScore string `json:"fragmentScore"`
}

// --- Scaling & Capacity ---

type ScalingAnalysis struct {
	SaturationScore    string `json:"saturationScore"`
	SaturationDetail   string `json:"saturationDetail"`
	ScalingRecommendation string `json:"scalingRecommendation"`
	ScalingDetail      string `json:"scalingDetail"`
	HeadroomKVCache    string `json:"headroomKVCache"`
	HeadroomGPU        string `json:"headroomGPU"`
	HeadroomQueue      string `json:"headroomQueue"`
	HeadroomOverall    string `json:"headroomOverall"`
}

// --- Model Configuration Audit ---

type ConfigAuditAnalysis struct {
	TensorParallelism   string `json:"tensorParallelism"`
	TPDetail            string `json:"tpDetail"`
	ContextLenConfig    string `json:"contextLenConfig"`
	ContextLenActual    string `json:"contextLenActual"`
	ContextLenWaste     string `json:"contextLenWaste"`
	ContextLenDetail    string `json:"contextLenDetail"`
	Quantization        string `json:"quantization"`
	QuantizationDetail  string `json:"quantizationDetail"`
	RuntimeVersion      string `json:"runtimeVersion"`
	RuntimeVersionDetail string `json:"runtimeVersionDetail"`
}

// --- Network & Connectivity ---

type NetworkAnalysis struct {
	PoliciesAffecting  int      `json:"policiesAffecting"`
	PolicyDetails      []string `json:"policyDetails"`
	InferenceURL       string   `json:"inferenceURL"`
	InferenceReachable string   `json:"inferenceReachable"`
}

// --- Health & Reliability ---

type HealthAnalysis struct {
	ErrorRateTotal   string `json:"errorRateTotal"`
	ErrorRate5m      string `json:"errorRate5m"`
	ErrorCategories  []ErrorCategory `json:"errorCategories"`
	UptimePercent    string `json:"uptimePercent"`
	UptimeDetail     string `json:"uptimeDetail"`
	ReadySince       string `json:"readySince"`
}

type ErrorCategory struct {
	Category string `json:"category"`
	Count    string `json:"count"`
	Detail   string `json:"detail"`
}

// --- Cost & Efficiency ---

type CostAnalysis struct {
	TokensPerGPUHour     string `json:"tokensPerGPUHour"`
	CostEfficiencyScore  string `json:"costEfficiencyScore"`
	OverProvisionCPU     string `json:"overProvisionCPU"`
	OverProvisionMem     string `json:"overProvisionMem"`
	OverProvisionScore   string `json:"overProvisionScore"`
	OverProvisionDetail  string `json:"overProvisionDetail"`
	RightsizeCPU         string `json:"rightsizeCPU"`
	RightsizeMem         string `json:"rightsizeMem"`
	RightsizeDetail      string `json:"rightsizeDetail"`
}

// --- API handlers ---

func (h *Handler) handleModelList(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		jsonError(w, "Cluster connection not configured", http.StatusServiceUnavailable)
		return
	}

	isvcList, err := h.k8sClient.GetInferenceServices()
	if err != nil {
		log.Printf("Failed to get InferenceServices: %v", err)
		jsonError(w, "Failed to query models", http.StatusBadGateway)
		return
	}

	items := make([]ModelListItem, 0, len(isvcList))
	for _, isvc := range isvcList {
		item := ModelListItem{
			Name:      isvc.Metadata.Name,
			Namespace: isvc.Metadata.Namespace,
			Status:    inferenceServiceStatus(isvc.Status),
		}
		if rt, ok := isvc.Metadata.Labels["serving.kserve.io/servingruntime"]; ok {
			item.Runtime = rt
		}
		items = append(items, item)
	}

	jsonResponse(w, items)
}

func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	namespace := r.URL.Query().Get("namespace")

	if model == "" || namespace == "" {
		jsonError(w, "model and namespace query parameters are required", http.StatusBadRequest)
		return
	}
	if !safeNameRe.MatchString(model) || !safeNameRe.MatchString(namespace) {
		jsonError(w, "invalid model or namespace name", http.StatusBadRequest)
		return
	}

	resp := MetricsResponse{
		Model:     model,
		Namespace: namespace,
	}

	// If no monitoring client, return empty metrics with a note
	if h.monClient == nil {
		resp.Summary = MetricsSummary{}
		jsonResponse(w, resp)
		return
	}

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "1h"
	}

	now := time.Now()
	end := now
	var start time.Time
	var step time.Duration
	switch rangeStr {
	case "5m":
		start = now.Add(-5 * time.Minute)
		step = 15 * time.Second
	case "15m":
		start = now.Add(-15 * time.Minute)
		step = 30 * time.Second
	case "1h":
		start = now.Add(-1 * time.Hour)
		step = 60 * time.Second
	case "6h":
		start = now.Add(-6 * time.Hour)
		step = 5 * time.Minute
	case "24h":
		start = now.Add(-24 * time.Hour)
		step = 15 * time.Minute
	default:
		start = now.Add(-1 * time.Hour)
		step = 60 * time.Second
	}

	// Build pod name filter: KServe predictor pods usually contain the ISVC name
	podFilter := fmt.Sprintf(`namespace="%s",pod=~"%s-predictor-.*"`, namespace, model)

	// --- Summary metrics (instant queries) ---

	// Total requests (try multiple metric names for different runtimes)
	requestMetrics := []string{
		// vLLM
		fmt.Sprintf(`sum(vllm:request_success_total{%s})`, podFilter),
		// TGI
		fmt.Sprintf(`sum(tgi_request_count{%s})`, podFilter),
		// caikit
		fmt.Sprintf(`sum(caikit_rpcs_completed_total{%s})`, podFilter),
		// Generic KServe / kube metrics
		fmt.Sprintf(`sum(increase(http_requests_total{%s}[1h]))`, podFilter),
	}
	resp.Summary.RequestsTotal = h.firstMetricValue(requestMetrics)

	// Requests per second — average over the selected time range
	rpsMetrics := []string{
		fmt.Sprintf(`sum(rate(vllm:request_success_total{%s}[%s]))`, podFilter, rangeStr),
		fmt.Sprintf(`sum(rate(tgi_request_count{%s}[%s]))`, podFilter, rangeStr),
		fmt.Sprintf(`sum(rate(caikit_rpcs_completed_total{%s}[%s]))`, podFilter, rangeStr),
	}
	resp.Summary.RequestsPerSec = h.firstMetricValue(rpsMetrics)

	// Average latency (p50) — use the selected time range so it doesn't go to 0 during idle
	latencyMetrics := []string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[%s])) by (le))`, podFilter, rangeStr),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(tgi_request_duration_bucket{%s}[%s])) by (le))`, podFilter, rangeStr),
	}
	resp.Summary.AvgLatencyMs = h.firstMetricValueScaled(latencyMetrics, 1000)

	// P99 latency — use the selected time range
	p99Metrics := []string{
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[%s])) by (le))`, podFilter, rangeStr),
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(tgi_request_duration_bucket{%s}[%s])) by (le))`, podFilter, rangeStr),
	}
	resp.Summary.P99LatencyMs = h.firstMetricValueScaled(p99Metrics, 1000)

	// Error rate as percentage within the time range
	// Only compute when there were actual requests; otherwise 0%
	errorMetrics := []string{
		fmt.Sprintf(`100 * (sum(increase(vllm:e2e_request_latency_seconds_count{%s}[%s])) - sum(increase(vllm:request_success_total{%s}[%s]))) / clamp_min(sum(increase(vllm:e2e_request_latency_seconds_count{%s}[%s])), 1)`, podFilter, rangeStr, podFilter, rangeStr, podFilter, rangeStr),
	}
	resp.Summary.ErrorRate = h.firstMetricValue(errorMetrics)

	// Tokens per second (LLM specific) — average over the selected time range
	tokenMetrics := []string{
		fmt.Sprintf(`sum(rate(vllm:generation_tokens_total{%s}[%s]))`, podFilter, rangeStr),
		fmt.Sprintf(`sum(rate(tgi_request_generated_tokens_total{%s}[%s]))`, podFilter, rangeStr),
	}
	resp.Summary.TokensPerSec = h.firstMetricValue(tokenMetrics)

	// Active / running requests
	activeMetrics := []string{
		fmt.Sprintf(`sum(vllm:num_requests_running{%s})`, podFilter),
		fmt.Sprintf(`sum(tgi_queue_size{%s})`, podFilter),
	}
	resp.Summary.ActiveRequests = h.firstMetricValue(activeMetrics)

	// Total tokens generated (cumulative counter)
	totalTokenMetrics := []string{
		fmt.Sprintf(`sum(vllm:generation_tokens_total{%s})`, podFilter),
		fmt.Sprintf(`sum(tgi_request_generated_tokens_total{%s})`, podFilter),
		fmt.Sprintf(`sum(caikit_tokens_generated_total{%s})`, podFilter),
	}
	resp.Summary.TotalTokens = h.firstMetricValue(totalTokenMetrics)

	// GPU utilization (DCGM metrics use exported_pod/exported_namespace labels)
	dcgmFilter := fmt.Sprintf(`exported_namespace="%s",exported_pod=~"%s-predictor-.*"`, namespace, model)
	gpuUtilMetrics := []string{
		fmt.Sprintf(`avg(DCGM_FI_DEV_GPU_UTIL{%s})`, dcgmFilter),
	}
	resp.Summary.GPUUtilization = h.firstMetricValue(gpuUtilMetrics)

	gpuMemUsedMetrics := []string{
		fmt.Sprintf(`sum(DCGM_FI_DEV_FB_USED{%s})`, dcgmFilter),
	}
	resp.Summary.GPUMemoryUsed = h.firstMetricValue(gpuMemUsedMetrics)

	gpuMemTotalMetrics := []string{
		fmt.Sprintf(`sum(DCGM_FI_DEV_FB_FREE{%s} + DCGM_FI_DEV_FB_USED{%s})`, dcgmFilter, dcgmFilter),
	}
	resp.Summary.GPUMemoryTotal = h.firstMetricValue(gpuMemTotalMetrics)

	// KV cache usage (vLLM/TGI — percentage of GPU KV cache in use, good proxy for GPU load)
	kvCacheMetrics := []string{
		fmt.Sprintf(`avg(vllm:kv_cache_usage_perc{%s})`, podFilter),
		fmt.Sprintf(`avg(tgi_batch_current_size{%s}) / avg(tgi_batch_current_max_size{%s})`, podFilter, podFilter),
	}
	resp.Summary.KVCacheUsage = h.firstMetricValue(kvCacheMetrics)

	// Container CPU / Memory
	cpuUsageMetrics := []string{
		fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s,container!=""}[%s]))`, podFilter, rangeStr),
	}
	resp.Summary.CPUUsage = h.firstMetricValue(cpuUsageMetrics)

	memUsageMetrics := []string{
		fmt.Sprintf(`sum(container_memory_working_set_bytes{%s,container!=""})`, podFilter),
	}
	resp.Summary.MemoryUsage = h.firstMetricValue(memUsageMetrics)

	// --- Time series (range queries) ---

	// Use a rate window appropriate for the chart step size (at least 2x step for smooth results)
	rateWindow := "5m"
	switch rangeStr {
	case "5m":
		rateWindow = "1m"
	case "15m":
		rateWindow = "2m"
	case "1h":
		rateWindow = "5m"
	case "6h":
		rateWindow = "15m"
	case "24h":
		rateWindow = "30m"
	}

	// Request rate over time
	rpsRangeQueries := []string{
		fmt.Sprintf(`sum(rate(vllm:request_success_total{%s}[%s]))`, podFilter, rateWindow),
		fmt.Sprintf(`sum(rate(tgi_request_count{%s}[%s]))`, podFilter, rateWindow),
		fmt.Sprintf(`sum(rate(caikit_rpcs_completed_total{%s}[%s]))`, podFilter, rateWindow),
	}
	resp.Series.RequestRate = h.firstRangeSeries(rpsRangeQueries, start, end, step)

	// Latency over time (p50)
	latencyRangeQueries := []string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[%s])) by (le))`, podFilter, rateWindow),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(tgi_request_duration_bucket{%s}[%s])) by (le))`, podFilter, rateWindow),
	}
	resp.Series.Latency = h.firstRangeSeriesScaled(latencyRangeQueries, start, end, step, 1000)

	// GPU utilization over time (try DCGM, fall back to KV cache usage * 100 for percentage)
	gpuRangeQueries := []string{
		fmt.Sprintf(`avg(DCGM_FI_DEV_GPU_UTIL{%s})`, dcgmFilter),
		fmt.Sprintf(`avg(vllm:kv_cache_usage_perc{%s}) * 100`, podFilter),
	}
	resp.Series.GPUUtil = h.firstRangeSeries(gpuRangeQueries, start, end, step)

	// Token throughput over time
	tokenRangeQueries := []string{
		fmt.Sprintf(`sum(rate(vllm:generation_tokens_total{%s}[%s]))`, podFilter, rateWindow),
		fmt.Sprintf(`sum(rate(tgi_request_generated_tokens_total{%s}[%s]))`, podFilter, rateWindow),
	}
	resp.Series.TokenRate = h.firstRangeSeries(tokenRangeQueries, start, end, step)

	jsonResponse(w, resp)
}

func (h *Handler) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	namespace := r.URL.Query().Get("namespace")

	if model == "" || namespace == "" {
		jsonError(w, "model and namespace query parameters are required", http.StatusBadRequest)
		return
	}
	if !safeNameRe.MatchString(model) || !safeNameRe.MatchString(namespace) {
		jsonError(w, "invalid model or namespace name", http.StatusBadRequest)
		return
	}

	resp := AnalysisResponse{
		Model:     model,
		Namespace: namespace,
	}

	podFilter := fmt.Sprintf(`namespace="%s",pod=~"%s-predictor-.*"`, namespace, model)
	dcgmFilter := fmt.Sprintf(`exported_namespace="%s",exported_pod=~"%s-predictor-.*"`, namespace, model)

	// === Performance Analysis ===
	// Use progressively wider windows (1h, then 6h fallback) so values persist during idle periods

	resp.Performance.TTFTp50Ms = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:time_to_first_token_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:time_to_first_token_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.TTFTp99Ms = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:time_to_first_token_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:time_to_first_token_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.ITLp50Ms = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:inter_token_latency_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:inter_token_latency_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.ITLp99Ms = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:inter_token_latency_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:inter_token_latency_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.PrefillTimeMs = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:request_prefill_time_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:request_prefill_time_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.DecodeTimeMs = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:request_decode_time_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:request_decode_time_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.QueueWaitMs = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:request_queue_time_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:request_queue_time_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.E2ELatencyP50Ms = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.50, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.E2ELatencyP99Ms = h.firstMetricValueScaled([]string{
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[1h])) by (le))`, podFilter),
		fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vllm:e2e_request_latency_seconds_bucket{%s}[6h])) by (le))`, podFilter),
	}, 1000)
	resp.Performance.RequestsPerSec = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(rate(vllm:request_success_total{%s}[1h]))`, podFilter),
		fmt.Sprintf(`sum(rate(tgi_request_count{%s}[1h]))`, podFilter),
		fmt.Sprintf(`sum(rate(vllm:request_success_total{%s}[6h]))`, podFilter),
	})
	resp.Performance.TokensPerSec = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(rate(vllm:generation_tokens_total{%s}[1h]))`, podFilter),
		fmt.Sprintf(`sum(rate(tgi_request_generated_tokens_total{%s}[1h]))`, podFilter),
		fmt.Sprintf(`sum(rate(vllm:generation_tokens_total{%s}[6h]))`, podFilter),
	})
	// Avg tokens: use lifetime counters for stability (total sum / total count)
	resp.Performance.AvgPromptTokens = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:request_prompt_tokens_sum{%s}) / clamp_min(sum(vllm:request_prompt_tokens_count{%s}), 1)`, podFilter, podFilter),
	})
	resp.Performance.AvgOutputTokens = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:request_generation_tokens_sum{%s}) / clamp_min(sum(vllm:request_generation_tokens_count{%s}), 1)`, podFilter, podFilter),
	})

	// === Efficiency Analysis ===

	resp.Efficiency.PrefixCacheHits = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:prefix_cache_hits_total{%s})`, podFilter),
	})
	resp.Efficiency.PrefixCacheQueries = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:prefix_cache_queries_total{%s})`, podFilter),
	})
	// Cache hit rate: use lifetime totals for stability
	resp.Efficiency.PrefixCacheHitRate = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:prefix_cache_hits_total{%s}) / clamp_min(sum(vllm:prefix_cache_queries_total{%s}), 1)`, podFilter, podFilter),
	})
	resp.Efficiency.KVCacheUsage = h.firstMetricValue([]string{
		fmt.Sprintf(`avg(vllm:kv_cache_usage_perc{%s})`, podFilter),
	})
	resp.Efficiency.PreemptionCount = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:num_preemptions_total{%s})`, podFilter),
	})
	resp.Efficiency.RequestsRunning = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:num_requests_running{%s})`, podFilter),
		fmt.Sprintf(`sum(tgi_queue_size{%s})`, podFilter),
	})
	resp.Efficiency.RequestsWaiting = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:num_requests_waiting{%s})`, podFilter),
	})
	resp.Efficiency.AvgBatchTokens = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(vllm:iteration_tokens_total_sum{%s}) / clamp_min(sum(vllm:iteration_tokens_total_count{%s}), 1)`, podFilter, podFilter),
	})
	resp.Efficiency.TokensPerGPU = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(rate(vllm:generation_tokens_total{%s}[1h])) / clamp_min(count(count by (pod) (vllm:generation_tokens_total{%s})), 1)`, podFilter, podFilter),
		fmt.Sprintf(`sum(rate(vllm:generation_tokens_total{%s}[6h])) / clamp_min(count(count by (pod) (vllm:generation_tokens_total{%s})), 1)`, podFilter, podFilter),
	})

	// === Resource Analysis ===

	resp.Resource.GPUUtilization = h.firstMetricValue([]string{
		fmt.Sprintf(`avg(DCGM_FI_DEV_GPU_UTIL{%s})`, dcgmFilter),
	})
	resp.Resource.GPUMemUsed = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(DCGM_FI_DEV_FB_USED{%s})`, dcgmFilter),
	})
	resp.Resource.GPUMemTotal = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(DCGM_FI_DEV_FB_FREE{%s} + DCGM_FI_DEV_FB_USED{%s})`, dcgmFilter, dcgmFilter),
	})
	resp.Resource.CPUActual = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s,container!=""}[1h]))`, podFilter),
	})
	resp.Resource.MemActual = h.firstMetricValue([]string{
		fmt.Sprintf(`sum(container_memory_working_set_bytes{%s,container!=""})`, podFilter),
	})

	// === K8s-based analysis (needs cluster access) ===

	var pods []k8s.Pod
	var nodes []k8s.Node
	var cpuReq, cpuLim float64
	var memReq, memLim int64
	var gpuReq int64

	if h.k8sClient != nil {
		pods, _ = h.k8sClient.GetPodsInNamespace(namespace, "serving.kserve.io/inferenceservice="+model)
		nodes, _ = h.k8sClient.GetNodes()

		for _, pod := range pods {
			for _, c := range pod.Spec.Containers {
				cpuReq += parseCPUCores(c.Resources.Requests["cpu"])
				cpuLim += parseCPUCores(c.Resources.Limits["cpu"])
				memReq += parseMemoryBytes(c.Resources.Requests["memory"])
				memLim += parseMemoryBytes(c.Resources.Limits["memory"])
				gpuReq += parseGPUResources(c.Resources.Requests)
				if gpuReq == 0 {
					gpuReq = parseGPUResources(c.Resources.Limits)
				}
			}
		}
		resp.Resource.CPURequested = fmt.Sprintf("%.1f", cpuReq)
		resp.Resource.CPULimit = fmt.Sprintf("%.1f", cpuLim)
		resp.Resource.MemRequested = fmt.Sprintf("%d", memReq)
		resp.Resource.MemLimit = fmt.Sprintf("%d", memLim)
		resp.Resource.GPUsRequested = fmt.Sprintf("%d", gpuReq)

		// === Model Config ===
		isvcList, err := h.k8sClient.GetInferenceServices()
		if err == nil {
			for _, isvc := range isvcList {
				if isvc.Metadata.Name == model && isvc.Metadata.Namespace == namespace {
					resp.Config.ModelName = isvc.Metadata.Name
					resp.Config.CreatedAt = isvc.Metadata.CreationTimestamp
					resp.Config.Labels = isvc.Metadata.Labels
					resp.Config.Status = inferenceServiceStatus(isvc.Status)
					if u, ok := isvc.Status["url"].(string); ok {
						resp.Config.URL = u
					}
					if rt, ok := isvc.Metadata.Labels["serving.kserve.io/servingruntime"]; ok {
						resp.Config.Runtime = rt
					}
					break
				}
			}
		}

		if len(pods) > 0 {
			resp.Config.Replicas = len(pods)
			resp.Config.NodeName = pods[0].Spec.NodeName
			for _, n := range nodes {
				if n.Metadata.Name == pods[0].Spec.NodeName {
					resp.Config.GPUProduct = detectGPUProduct(n.Metadata.Labels)
					break
				}
			}
		}

		// vLLM config from Prometheus labels
		if h.monClient != nil {
			results, err := h.monClient.Query(fmt.Sprintf(`vllm:cache_config_info{%s}`, podFilter))
			if err == nil && len(results) > 0 {
				m := results[0].Metric
				if v, ok := m["gpu_memory_utilization"]; ok {
					resp.Config.GPUMemUtil = v
				}
				if v, ok := m["block_size"]; ok {
					resp.Config.BlockSize = v
				}
				if v, ok := m["enable_prefix_caching"]; ok {
					resp.Config.PrefixCaching = v
				}
				if v, ok := m["max_model_len"]; ok {
					resp.Config.MaxModelLen = v
				}
			}
		}

		// === Scheduling & Placement ===
		h.analyzeScheduling(&resp, pods, nodes, gpuReq)

		// === Scaling & Capacity ===
		h.analyzeScaling(&resp, podFilter)

		// === Config Audit ===
		h.analyzeConfigAudit(&resp, pods, podFilter, gpuReq)

		// === Network ===
		h.analyzeNetwork(&resp, namespace, model)

		// === Health ===
		h.analyzeHealth(&resp, podFilter)

		// === Cost & Efficiency ===
		h.analyzeCost(&resp, cpuReq, cpuLim, memReq, memLim, gpuReq, podFilter)
	}

	jsonResponse(w, resp)
}

// --- Deep analysis helpers ---

func (h *Handler) analyzeScheduling(resp *AnalysisResponse, pods []k8s.Pod, nodes []k8s.Node, gpuReq int64) {
	// Node affinities and selectors
	for _, pod := range pods {
		for k, v := range pod.Spec.NodeSelector {
			resp.Scheduling.NodeSelectors = append(resp.Scheduling.NodeSelectors, k+"="+v)
		}
		if pod.Spec.Affinity != nil && pod.Spec.Affinity.NodeAffinity != nil {
			na := pod.Spec.Affinity.NodeAffinity
			if na.RequiredDuringScheduling != nil {
				for _, term := range na.RequiredDuringScheduling.NodeSelectorTerms {
					for _, expr := range term.MatchExpressions {
						resp.Scheduling.NodeAffinities = append(resp.Scheduling.NodeAffinities,
							fmt.Sprintf("%s %s [%s]", expr.Key, expr.Operator, strings.Join(expr.Values, ", ")))
					}
				}
			}
		}
	}

	// Pod spread analysis
	nodeMap := map[string]int{}
	for _, pod := range pods {
		if pod.Spec.NodeName != "" {
			nodeMap[pod.Spec.NodeName]++
		}
	}
	if len(pods) <= 1 {
		resp.Scheduling.PodSpread = "single"
		resp.Scheduling.PodSpreadDetail = "Single replica — no spread analysis applicable"
	} else if len(nodeMap) == 1 {
		resp.Scheduling.PodSpread = "co-located"
		resp.Scheduling.PodSpreadDetail = fmt.Sprintf("All %d replicas on the same node — consider anti-affinity for resilience", len(pods))
	} else if len(nodeMap) == len(pods) {
		resp.Scheduling.PodSpread = "spread"
		resp.Scheduling.PodSpreadDetail = fmt.Sprintf("All %d replicas on separate nodes — good distribution", len(pods))
	} else {
		resp.Scheduling.PodSpread = "partial"
		var detail []string
		for n, c := range nodeMap {
			detail = append(detail, fmt.Sprintf("%s: %d pods", n, c))
		}
		resp.Scheduling.PodSpreadDetail = fmt.Sprintf("Partial spread across %d nodes: %s", len(nodeMap), strings.Join(detail, ", "))
	}

	// GPU fragmentation
	// Get all serving pods across all models to compute used GPUs per node
	allServingPods, _ := h.k8sClient.GetAllPods("component=predictor")
	nodeGPUUsed := map[string]int64{}
	for _, pod := range allServingPods {
		if pod.Status.Phase != "Running" {
			continue
		}
		for _, c := range pod.Spec.Containers {
			used := parseGPUResources(c.Resources.Requests)
			if used == 0 {
				used = parseGPUResources(c.Resources.Limits)
			}
			nodeGPUUsed[pod.Spec.NodeName] += used
		}
	}

	for _, n := range nodes {
		gpuTotal := parseGPUResources(n.Status.Allocatable)
		if gpuTotal == 0 {
			continue
		}
		used := nodeGPUUsed[n.Metadata.Name]
		free := gpuTotal - used
		fragScore := "none"
		if free > 0 && free < gpuReq {
			fragScore = "fragmented"
		} else if free == 0 {
			fragScore = "full"
		} else {
			fragScore = "available"
		}
		resp.Scheduling.GPUFragmentation = append(resp.Scheduling.GPUFragmentation, GPUFragInfo{
			NodeName:      n.Metadata.Name,
			GPUTotal:      gpuTotal,
			GPUUsed:       used,
			GPUFree:       free,
			GPUProduct:    detectGPUProduct(n.Metadata.Labels),
			FragmentScore: fragScore,
		})
	}
}

func (h *Handler) analyzeScaling(resp *AnalysisResponse, podFilter string) {
	// Saturation score: combine KV cache, GPU util, queue depth
	kvStr := resp.Efficiency.KVCacheUsage
	gpuStr := resp.Resource.GPUUtilization
	waitStr := resp.Efficiency.RequestsWaiting

	var kvPct, gpuPct, waitCount float64
	if kvStr != "" {
		kvPct, _ = strconv.ParseFloat(kvStr, 64)
		kvPct *= 100
	}
	if gpuStr != "" {
		gpuPct, _ = strconv.ParseFloat(gpuStr, 64)
	}
	if waitStr != "" {
		waitCount, _ = strconv.ParseFloat(waitStr, 64)
	}

	// Weighted saturation: KV cache 40%, GPU 40%, queue 20%
	queueScore := waitCount * 5 // normalize: 20 waiting = 100%
	if queueScore > 100 {
		queueScore = 100
	}
	saturation := kvPct*0.4 + gpuPct*0.4 + queueScore*0.2

	resp.Scaling.SaturationScore = fmt.Sprintf("%.0f%%", saturation)
	if saturation < 30 {
		resp.Scaling.SaturationDetail = "Low utilization — model has significant spare capacity"
	} else if saturation < 60 {
		resp.Scaling.SaturationDetail = "Moderate utilization — healthy operating range"
	} else if saturation < 80 {
		resp.Scaling.SaturationDetail = "High utilization — approaching capacity limits"
	} else {
		resp.Scaling.SaturationDetail = "Critical saturation — model is at or near capacity, consider scaling"
	}

	// Scaling recommendation
	if saturation > 80 || waitCount > 10 {
		resp.Scaling.ScalingRecommendation = "Scale up"
		reasons := []string{}
		if kvPct > 80 {
			reasons = append(reasons, fmt.Sprintf("KV cache at %.0f%%", kvPct))
		}
		if gpuPct > 80 {
			reasons = append(reasons, fmt.Sprintf("GPU at %.0f%%", gpuPct))
		}
		if waitCount > 10 {
			reasons = append(reasons, fmt.Sprintf("%.0f requests queued", waitCount))
		}
		resp.Scaling.ScalingDetail = "Add replicas or increase GPU memory. " + strings.Join(reasons, "; ")
	} else if saturation < 20 && gpuPct < 15 {
		resp.Scaling.ScalingRecommendation = "Consider scaling down"
		resp.Scaling.ScalingDetail = "Very low utilization — resources may be over-provisioned"
	} else {
		resp.Scaling.ScalingRecommendation = "No action needed"
		resp.Scaling.ScalingDetail = "Current capacity matches workload"
	}

	// Headroom
	if kvPct > 0 {
		resp.Scaling.HeadroomKVCache = fmt.Sprintf("%.0f%% free", 100-kvPct)
	}
	if gpuPct > 0 {
		resp.Scaling.HeadroomGPU = fmt.Sprintf("%.0f%% free", 100-gpuPct)
	}
	resp.Scaling.HeadroomQueue = fmt.Sprintf("%.0f waiting", waitCount)

	overallHeadroom := 100 - saturation
	if overallHeadroom < 0 {
		overallHeadroom = 0
	}
	resp.Scaling.HeadroomOverall = fmt.Sprintf("%.0f%%", overallHeadroom)
}

func (h *Handler) analyzeConfigAudit(resp *AnalysisResponse, pods []k8s.Pod, podFilter string, gpuReq int64) {
	// Tensor parallelism
	if gpuReq > 1 {
		resp.ConfigAudit.TensorParallelism = fmt.Sprintf("%d GPUs", gpuReq)
		if gpuReq <= 8 {
			resp.ConfigAudit.TPDetail = fmt.Sprintf("Model uses %d GPUs — likely tensor parallelism", gpuReq)
		} else {
			resp.ConfigAudit.TPDetail = fmt.Sprintf("Model uses %d GPUs — may use pipeline parallelism for >8 GPUs", gpuReq)
		}
	} else {
		resp.ConfigAudit.TensorParallelism = "Single GPU"
		resp.ConfigAudit.TPDetail = "No parallelism — model fits on one GPU"
	}

	// max_model_len vs actual usage
	if resp.Config.MaxModelLen != "" {
		resp.ConfigAudit.ContextLenConfig = resp.Config.MaxModelLen + " tokens"
		if resp.Performance.AvgPromptTokens != "" && resp.Performance.AvgOutputTokens != "" {
			avgPrompt, _ := strconv.ParseFloat(resp.Performance.AvgPromptTokens, 64)
			avgOutput, _ := strconv.ParseFloat(resp.Performance.AvgOutputTokens, 64)
			maxLen, _ := strconv.ParseFloat(resp.Config.MaxModelLen, 64)
			if avgPrompt > 0 && maxLen > 0 {
				avgTotal := avgPrompt + avgOutput
				resp.ConfigAudit.ContextLenActual = fmt.Sprintf("%.0f tokens avg (%.0f prompt + %.0f output)", avgTotal, avgPrompt, avgOutput)
				utilPct := (avgTotal / maxLen) * 100
				resp.ConfigAudit.ContextLenWaste = fmt.Sprintf("%.0f%% utilized", utilPct)
				if utilPct < 10 {
					resp.ConfigAudit.ContextLenDetail = "Very low context utilization — reducing max_model_len could free significant KV cache memory"
				} else if utilPct < 30 {
					resp.ConfigAudit.ContextLenDetail = "Low context utilization — consider reducing max_model_len to improve throughput"
				} else {
					resp.ConfigAudit.ContextLenDetail = "Context length is reasonably sized for the workload"
				}
			}
		}
	}

	// Quantization detection and max_model_len from vLLM /v1/models API
	modelRoot := ""
	for _, pod := range pods {
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			client := &http.Client{Timeout: 3 * time.Second}
			if apiResp, err := client.Get(fmt.Sprintf("http://%s:8080/v1/models", pod.Status.PodIP)); err == nil {
				defer apiResp.Body.Close()
				var modelsResp struct {
					Data []struct {
						Root        string `json:"root"`
						MaxModelLen int64  `json:"max_model_len"`
					} `json:"data"`
				}
				if json.NewDecoder(apiResp.Body).Decode(&modelsResp) == nil && len(modelsResp.Data) > 0 {
					modelRoot = modelsResp.Data[0].Root
					if modelsResp.Data[0].MaxModelLen > 0 {
						if resp.Config.MaxModelLen == "" {
							resp.Config.MaxModelLen = fmt.Sprintf("%d", modelsResp.Data[0].MaxModelLen)
							resp.ConfigAudit.ContextLenConfig = fmt.Sprintf("%d tokens", modelsResp.Data[0].MaxModelLen)
						}
					}
				}
			}
			break
		}
	}
	// Build a search string from model root, storageUri, and container args
	searchParts := []string{strings.ToLower(modelRoot)}

	// Get storageUri from ISVC
	if h.k8sClient != nil {
		if isvcs, err := h.k8sClient.GetInferenceServices(); err == nil {
			for _, isvc := range isvcs {
				if isvc.Metadata.Name == resp.Model && isvc.Metadata.Namespace == resp.Namespace {
					if pred, ok := isvc.Spec["predictor"].(map[string]interface{}); ok {
						if model, ok := pred["model"].(map[string]interface{}); ok {
							if uri, ok := model["storageUri"].(string); ok {
								searchParts = append(searchParts, strings.ToLower(uri))
							}
						}
					}
					break
				}
			}
		}
	}

	// Check container args for --dtype and --quantization
	dtype := ""
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			if c.Name != "kserve-container" {
				continue
			}
			allArgs := append(c.Command, c.Args...)
			for _, arg := range allArgs {
				lower := strings.ToLower(arg)
				if strings.HasPrefix(lower, "--dtype=") {
					dtype = strings.TrimPrefix(lower, "--dtype=")
				}
				if strings.HasPrefix(lower, "--quantization=") {
					q := strings.TrimPrefix(lower, "--quantization=")
					searchParts = append(searchParts, q)
				}
			}
		}
		if dtype != "" {
			break
		}
	}

	searchStr := strings.Join(searchParts, " ")
	if strings.Contains(searchStr, "awq") {
		resp.ConfigAudit.Quantization = "AWQ"
		resp.ConfigAudit.QuantizationDetail = "4-bit AWQ quantization — good balance of speed and quality"
	} else if strings.Contains(searchStr, "gptq") {
		resp.ConfigAudit.Quantization = "GPTQ"
		resp.ConfigAudit.QuantizationDetail = "GPTQ quantization — effective for reducing memory usage"
	} else if strings.Contains(searchStr, "fp8") || strings.Contains(searchStr, "fp-8") {
		resp.ConfigAudit.Quantization = "FP8"
		resp.ConfigAudit.QuantizationDetail = "FP8 quantization — minimal quality loss with good performance"
	} else if strings.Contains(searchStr, "gguf") {
		resp.ConfigAudit.Quantization = "GGUF"
		resp.ConfigAudit.QuantizationDetail = "GGUF quantized model"
	} else if dtype != "" {
		resp.ConfigAudit.Quantization = "None (dtype: " + dtype + ")"
		resp.ConfigAudit.QuantizationDetail = "No quantization — running with --dtype=" + dtype
	} else if modelRoot != "" {
		resp.ConfigAudit.Quantization = "None detected"
		resp.ConfigAudit.QuantizationDetail = "Full precision or quantization not detectable"
	} else {
		resp.ConfigAudit.Quantization = "Unknown"
		resp.ConfigAudit.QuantizationDetail = "Could not determine model configuration"
	}

	// Runtime version
	for _, pod := range pods {
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			client := &http.Client{Timeout: 3 * time.Second}
			if vresp, err := client.Get(fmt.Sprintf("http://%s:8080/version", pod.Status.PodIP)); err == nil {
				defer vresp.Body.Close()
				var ver struct {
					Version string `json:"version"`
				}
				if json.NewDecoder(vresp.Body).Decode(&ver) == nil && ver.Version != "" {
					resp.ConfigAudit.RuntimeVersion = ver.Version
					resp.ConfigAudit.RuntimeVersionDetail = "Runtime version detected from pod API"
				}
			}
			break
		}
	}
	if resp.ConfigAudit.RuntimeVersion == "" {
		resp.ConfigAudit.RuntimeVersion = resp.Config.Runtime
		resp.ConfigAudit.RuntimeVersionDetail = "Version from serving runtime label"
	}
}

func (h *Handler) analyzeNetwork(resp *AnalysisResponse, namespace, model string) {
	// Check network policies affecting this namespace
	policies, err := h.k8sClient.GetNetworkPolicies(namespace)
	if err == nil {
		resp.Network.PoliciesAffecting = len(policies)
		for _, np := range policies {
			detail := fmt.Sprintf("%s: ", np.Metadata.Name)
			types := strings.Join(np.Spec.PolicyTypes, ", ")
			if types == "" {
				types = "Ingress"
			}
			detail += types
			if len(np.Spec.Ingress) == 0 && containsStr(np.Spec.PolicyTypes, "Ingress") {
				detail += " [DENIES all inbound]"
			}
			if len(np.Spec.Egress) == 0 && containsStr(np.Spec.PolicyTypes, "Egress") {
				detail += " [DENIES all outbound]"
			}
			resp.Network.PolicyDetails = append(resp.Network.PolicyDetails, detail)
		}
	}

	// Check inference URL reachability
	isvcList, err := h.k8sClient.GetInferenceServices()
	if err == nil {
		for _, isvc := range isvcList {
			if isvc.Metadata.Name == model && isvc.Metadata.Namespace == namespace {
				if u, ok := isvc.Status["url"].(string); ok && u != "" {
					resp.Network.InferenceURL = u
					// Check reachability by looking at Ready condition
					status := inferenceServiceStatus(isvc.Status)
					if status == "Ready" {
						resp.Network.InferenceReachable = "Ready"
					} else {
						resp.Network.InferenceReachable = status
					}
				}
				break
			}
		}
	}
}

func (h *Handler) analyzeHealth(resp *AnalysisResponse, podFilter string) {
	if h.monClient == nil {
		return
	}

	// Error rate (recent — 15m window, falls back to 1h)
	errRate5m := h.firstMetricValue([]string{
		fmt.Sprintf(`100 * (sum(increase(vllm:e2e_request_latency_seconds_count{%s}[15m])) - sum(increase(vllm:request_success_total{%s}[15m]))) / clamp_min(sum(increase(vllm:e2e_request_latency_seconds_count{%s}[15m])), 1)`, podFilter, podFilter, podFilter),
		fmt.Sprintf(`100 * (sum(increase(vllm:e2e_request_latency_seconds_count{%s}[1h])) - sum(increase(vllm:request_success_total{%s}[1h]))) / clamp_min(sum(increase(vllm:e2e_request_latency_seconds_count{%s}[1h])), 1)`, podFilter, podFilter, podFilter),
	})
	if errRate5m != "" {
		v, _ := strconv.ParseFloat(errRate5m, 64)
		resp.Health.ErrorRate5m = fmt.Sprintf("%.2f%%", v)
	}

	// Error rate (total over 6h)
	errRateTotal := h.firstMetricValue([]string{
		fmt.Sprintf(`100 * (sum(increase(vllm:e2e_request_latency_seconds_count{%s}[6h])) - sum(increase(vllm:request_success_total{%s}[6h]))) / clamp_min(sum(increase(vllm:e2e_request_latency_seconds_count{%s}[6h])), 1)`, podFilter, podFilter, podFilter),
	})
	if errRateTotal != "" {
		v, _ := strconv.ParseFloat(errRateTotal, 64)
		resp.Health.ErrorRateTotal = fmt.Sprintf("%.2f%%", v)
	}

	// Error categories from HTTP status codes (if available)
	// 429 rate limiting
	rateLimited := h.firstMetricValue([]string{
		fmt.Sprintf(`sum(increase(vllm:request_success_total{%s,status="429"}[1h]))`, podFilter),
	})
	if rateLimited != "" {
		v, _ := strconv.ParseFloat(rateLimited, 64)
		if v > 0 {
			resp.Health.ErrorCategories = append(resp.Health.ErrorCategories, ErrorCategory{
				Category: "Rate Limited (429)",
				Count:    fmt.Sprintf("%.0f", v),
				Detail:   "Requests rejected due to rate limiting",
			})
		}
	}

	// Timeouts (from high latency)
	p99Str := resp.Performance.E2ELatencyP99Ms
	if p99Str != "" {
		p99, _ := strconv.ParseFloat(p99Str, 64)
		if p99 > 30000 {
			resp.Health.ErrorCategories = append(resp.Health.ErrorCategories, ErrorCategory{
				Category: "Potential Timeouts",
				Count:    "-",
				Detail:   fmt.Sprintf("P99 latency is %.1fs — clients with shorter timeouts may be failing", p99/1000),
			})
		}
	}

	// Preemptions as an error category
	preemptions := resp.Efficiency.PreemptionCount
	if preemptions != "" && preemptions != "0" {
		resp.Health.ErrorCategories = append(resp.Health.ErrorCategories, ErrorCategory{
			Category: "GPU Preemptions",
			Count:    preemptions,
			Detail:   "Requests evicted from GPU due to memory pressure — may cause retries",
		})
	}

	// Uptime — check if model has been Ready continuously
	// Use the model creation timestamp and current status
	if resp.Config.Status == "Ready" {
		if resp.Config.CreatedAt != "" {
			created, err := time.Parse(time.RFC3339, resp.Config.CreatedAt)
			if err == nil {
				uptime := time.Since(created)
				resp.Health.ReadySince = resp.Config.CreatedAt
				if uptime.Hours() > 24 {
					resp.Health.UptimeDetail = fmt.Sprintf("Running for %.0f days", uptime.Hours()/24)
				} else {
					resp.Health.UptimeDetail = fmt.Sprintf("Running for %.1f hours", uptime.Hours())
				}
			}
		}
		resp.Health.UptimePercent = "Healthy"
	} else {
		resp.Health.UptimePercent = "Degraded"
		resp.Health.UptimeDetail = "Model is not in Ready state: " + resp.Config.Status
	}
}

func (h *Handler) analyzeCost(resp *AnalysisResponse, cpuReq, cpuLim float64, memReq, memLim int64, gpuReq int64, podFilter string) {
	// Tokens per GPU-hour
	if resp.Performance.TokensPerSec != "" && gpuReq > 0 {
		tps, _ := strconv.ParseFloat(resp.Performance.TokensPerSec, 64)
		if tps > 0 {
			tokPerGPUHour := (tps / float64(gpuReq)) * 3600
			resp.Cost.TokensPerGPUHour = fmt.Sprintf("%.0f", tokPerGPUHour)
			if tokPerGPUHour > 100000 {
				resp.Cost.CostEfficiencyScore = "Excellent"
			} else if tokPerGPUHour > 50000 {
				resp.Cost.CostEfficiencyScore = "Good"
			} else if tokPerGPUHour > 10000 {
				resp.Cost.CostEfficiencyScore = "Moderate"
			} else if tokPerGPUHour > 0 {
				resp.Cost.CostEfficiencyScore = "Low"
			}
		}
	}

	// Over-provisioning
	if resp.Resource.CPUActual != "" && cpuReq > 0 {
		actual, _ := strconv.ParseFloat(resp.Resource.CPUActual, 64)
		if actual > 0 {
			ratio := actual / cpuReq
			resp.Cost.OverProvisionCPU = fmt.Sprintf("%.0f%% used of requested", ratio*100)
		}
	}
	if resp.Resource.MemActual != "" && memReq > 0 {
		actual, _ := strconv.ParseFloat(resp.Resource.MemActual, 64)
		if actual > 0 {
			ratio := actual / float64(memReq)
			resp.Cost.OverProvisionMem = fmt.Sprintf("%.0f%% used of requested", ratio*100)
		}
	}

	// Overall over-provisioning score
	cpuActual, _ := strconv.ParseFloat(resp.Resource.CPUActual, 64)
	memActual, _ := strconv.ParseFloat(resp.Resource.MemActual, 64)
	if cpuReq > 0 && memReq > 0 && cpuActual > 0 && memActual > 0 {
		cpuRatio := cpuActual / cpuReq
		memRatio := memActual / float64(memReq)
		avgRatio := (cpuRatio + memRatio) / 2
		if avgRatio < 0.2 {
			resp.Cost.OverProvisionScore = "Heavily over-provisioned"
			resp.Cost.OverProvisionDetail = "Resource usage is far below requests — significant cost savings possible"
		} else if avgRatio < 0.5 {
			resp.Cost.OverProvisionScore = "Over-provisioned"
			resp.Cost.OverProvisionDetail = "Resources are under-utilized — consider reducing requests"
		} else if avgRatio < 0.8 {
			resp.Cost.OverProvisionScore = "Well-sized"
			resp.Cost.OverProvisionDetail = "Resource requests match actual usage reasonably well"
		} else {
			resp.Cost.OverProvisionScore = "Tightly provisioned"
			resp.Cost.OverProvisionDetail = "Usage is close to requests — ensure limits have headroom for spikes"
		}
	}

	// Right-sizing recommendations
	if cpuActual > 0 && cpuReq > 0 {
		recommended := cpuActual * 1.3 // 30% headroom
		if recommended < 0.1 {
			recommended = 0.1
		}
		if recommended < cpuReq*0.7 || recommended > cpuReq*1.3 {
			resp.Cost.RightsizeCPU = fmt.Sprintf("%.1f cores (currently %.1f)", recommended, cpuReq)
		} else {
			resp.Cost.RightsizeCPU = fmt.Sprintf("%.1f cores — current request is appropriate", cpuReq)
		}
	}
	if memActual > 0 && memReq > 0 {
		recommended := memActual * 1.3
		if recommended < float64(memReq)*0.7 || recommended > float64(memReq)*1.3 {
			resp.Cost.RightsizeMem = fmt.Sprintf("%s (currently %s)", fmtBytesGo(int64(recommended)), fmtBytesGo(memReq))
		} else {
			resp.Cost.RightsizeMem = fmt.Sprintf("%s — current request is appropriate", fmtBytesGo(memReq))
		}
	}
	if resp.Cost.RightsizeCPU != "" || resp.Cost.RightsizeMem != "" {
		resp.Cost.RightsizeDetail = "Recommendations based on actual usage + 30% headroom"
	}
}

func fmtBytesGo(b int64) string {
	if b >= 1024*1024*1024 {
		return fmt.Sprintf("%.1f GiB", float64(b)/(1024*1024*1024))
	}
	if b >= 1024*1024 {
		return fmt.Sprintf("%.0f MiB", float64(b)/(1024*1024))
	}
	return fmt.Sprintf("%d B", b)
}

// --- Live metrics types ---

type LiveResponse struct {
	Model     string            `json:"model"`
	Namespace string            `json:"namespace"`
	Timestamp int64             `json:"timestamp"`
	Gauges    map[string]float64 `json:"gauges"`
	Counters  map[string]float64 `json:"counters"`
}

func (h *Handler) handleLive(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	namespace := r.URL.Query().Get("namespace")

	if model == "" || namespace == "" {
		jsonError(w, "model and namespace query parameters are required", http.StatusBadRequest)
		return
	}
	if !safeNameRe.MatchString(model) || !safeNameRe.MatchString(namespace) {
		jsonError(w, "invalid model or namespace name", http.StatusBadRequest)
		return
	}

	if h.k8sClient == nil {
		jsonError(w, "cluster connection not configured", http.StatusServiceUnavailable)
		return
	}

	// Find the predictor pod IP
	pods, err := h.k8sClient.GetPodsInNamespace(namespace, "serving.kserve.io/inferenceservice="+model)
	if err != nil || len(pods) == 0 {
		jsonError(w, "no running pods found for model", http.StatusNotFound)
		return
	}

	var podIP string
	for _, pod := range pods {
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			podIP = pod.Status.PodIP
			break
		}
	}
	if podIP == "" {
		jsonError(w, "no running pod with IP found", http.StatusNotFound)
		return
	}

	// Scrape /metrics from the pod directly
	client := &http.Client{Timeout: 3 * time.Second}
	metricsURL := fmt.Sprintf("http://%s:8080/metrics", podIP)
	resp, err := client.Get(metricsURL)
	if err != nil {
		jsonError(w, "failed to scrape pod metrics: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		jsonError(w, "failed to read metrics response", http.StatusBadGateway)
		return
	}

	gauges, counters := parsePrometheusText(string(bodyBytes))

	liveResp := LiveResponse{
		Model:     model,
		Namespace: namespace,
		Timestamp: time.Now().Unix(),
		Gauges:    gauges,
		Counters:  counters,
	}

	jsonResponse(w, liveResp)
}

// --- Load test handlers ---

const loadTestRunnerPodName = "ai-toolbox-loadtest-runner"

type loadTestStartRequest struct {
	Model       string `json:"model"`
	Namespace   string `json:"namespace"`
	TokenSize   int    `json:"tokenSize"`
	Concurrency int    `json:"concurrency"`
	Force       bool   `json:"force"`
}

func (h *Handler) getRunnerImage() string {
	if img := os.Getenv("LOADTEST_RUNNER_IMAGE"); img != "" {
		return img
	}
	// Default: same image as the main app
	if img := os.Getenv("APP_IMAGE"); img != "" {
		return img
	}
	return "quay.io/kborup-redhat/ai-toolbox:latest"
}

func (h *Handler) getAppNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "ai-toolbox"
}

func (h *Handler) ensureRunnerPod() (string, error) {
	h.runnerMu.Lock()
	defer h.runnerMu.Unlock()

	ns := h.getAppNamespace()

	// Check if runner pod already exists and is running
	if h.runnerPodIP != "" && h.runnerPodName != "" {
		pod, err := h.k8sClient.GetPod(ns, h.runnerPodName)
		if err == nil && pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			h.runnerPodIP = pod.Status.PodIP
			return h.runnerPodIP, nil
		}
		// Pod is gone or not running, reset
		h.runnerPodIP = ""
		h.runnerPodName = ""
	}

	// Delete any stale pod and wait for it to be fully gone
	_ = h.k8sClient.DeletePod(ns, loadTestRunnerPodName)
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		_, err := h.k8sClient.GetPod(ns, loadTestRunnerPodName)
		if err != nil {
			break // pod is gone (404)
		}
	}

	// Create the runner pod
	image := h.getRunnerImage()
	podSpec := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]interface{}{
			"name":      loadTestRunnerPodName,
			"namespace": ns,
			"labels": map[string]string{
				"app":       "ai-toolbox",
				"component": "loadtest-runner",
			},
		},
		"spec": map[string]interface{}{
			"serviceAccountName": "ai-toolbox",
			"restartPolicy":      "Never",
			"containers": []map[string]interface{}{
				{
					"name":    "runner",
					"image":   image,
					"command": []string{"loadtest-runner"},
					"ports": []map[string]interface{}{
						{"containerPort": 8090, "name": "api"},
					},
					"resources": map[string]interface{}{
						"requests": map[string]string{
							"cpu":    "100m",
							"memory": "128Mi",
						},
						"limits": map[string]string{
							"memory": "512Mi",
						},
					},
				},
			},
		},
	}

	podJSON, err := json.Marshal(podSpec)
	if err != nil {
		return "", fmt.Errorf("marshaling pod spec: %w", err)
	}

	if err := h.k8sClient.CreatePod(ns, podJSON); err != nil {
		return "", fmt.Errorf("creating runner pod: %w", err)
	}

	// Wait for pod to be running (up to 60s)
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		pod, err := h.k8sClient.GetPod(ns, loadTestRunnerPodName)
		if err != nil {
			continue
		}
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			h.runnerPodIP = pod.Status.PodIP
			h.runnerPodName = loadTestRunnerPodName
			log.Printf("Load test runner pod ready: %s (IP: %s)", loadTestRunnerPodName, pod.Status.PodIP)
			return h.runnerPodIP, nil
		}
	}

	// Cleanup on timeout
	_ = h.k8sClient.DeletePod(ns, loadTestRunnerPodName)
	return "", fmt.Errorf("runner pod did not become ready within 60s")
}

func (h *Handler) proxyToRunner(method, path string, body io.Reader) (*http.Response, error) {
	h.runnerMu.Lock()
	ip := h.runnerPodIP
	h.runnerMu.Unlock()

	if ip == "" {
		return nil, fmt.Errorf("no runner pod available")
	}

	url := fmt.Sprintf("http://%s:8090%s", ip, path)
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	return client.Do(req)
}

func (h *Handler) handleLoadTestStart(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		jsonError(w, "cluster connection not configured", http.StatusServiceUnavailable)
		return
	}

	var req loadTestStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Model == "" || req.Namespace == "" {
		jsonError(w, "model and namespace are required", http.StatusBadRequest)
		return
	}
	if !safeNameRe.MatchString(req.Model) || !safeNameRe.MatchString(req.Namespace) {
		jsonError(w, "invalid model or namespace name", http.StatusBadRequest)
		return
	}
	if req.TokenSize <= 0 {
		req.TokenSize = 50
	}
	if req.TokenSize > 4096 {
		req.TokenSize = 4096
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 1
	}
	if req.Concurrency > 100 {
		req.Concurrency = 100
	}

	username := r.Header.Get("X-Forwarded-User")
	if username == "" {
		username = "unknown"
	}
	modelKey := req.Namespace + "/" + req.Model

	// Check if there's already an active load test on this model
	h.runnerMu.Lock()
	activeUser := h.loadTestUser
	activeModel := h.loadTestModel
	h.runnerMu.Unlock()

	if activeUser != "" && !req.Force {
		// Check if runner is actually still running
		if statusResp, err := h.proxyToRunner("GET", "/status", nil); err == nil {
			defer statusResp.Body.Close()
			var status struct {
				Running bool `json:"running"`
			}
			if json.NewDecoder(statusResp.Body).Decode(&status) == nil && status.Running {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"conflict":    true,
					"activeUser":  activeUser,
					"activeModel": activeModel,
				})
				return
			}
		}
	}

	// If forcing, stop the existing test and clean up the runner first
	if req.Force && activeUser != "" {
		h.proxyToRunner("POST", "/stop", nil)
		h.deleteRunnerPod()
	}

	// Find the predictor pod IP
	pods, err := h.k8sClient.GetPodsInNamespace(req.Namespace, "serving.kserve.io/inferenceservice="+req.Model)
	if err != nil || len(pods) == 0 {
		jsonError(w, "no running pods found for model", http.StatusNotFound)
		return
	}

	var podIP string
	for _, pod := range pods {
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			podIP = pod.Status.PodIP
			break
		}
	}
	if podIP == "" {
		jsonError(w, "no running pod with IP found", http.StatusNotFound)
		return
	}

	// Ensure runner pod is available
	_, err = h.ensureRunnerPod()
	if err != nil {
		jsonError(w, "failed to start runner pod: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Send start command to the runner
	runnerReq := map[string]interface{}{
		"endpoint":    fmt.Sprintf("http://%s:8080/v1/completions", podIP),
		"modelName":   req.Model,
		"tokenSize":   req.TokenSize,
		"concurrency": req.Concurrency,
	}
	bodyBytes, _ := json.Marshal(runnerReq)

	resp, err := h.proxyToRunner("POST", "/start", bytes.NewReader(bodyBytes))
	if err != nil {
		jsonError(w, "failed to communicate with runner: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))

	// Track active load test owner
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		h.runnerMu.Lock()
		h.loadTestUser = username
		h.loadTestModel = modelKey
		h.loadTestTotal++
		h.loadTestRunning = true
		h.runnerMu.Unlock()
	}

	log.Printf("Load test started by %s: model=%s/%s concurrency=%d tokens=%d", username, req.Namespace, req.Model, req.Concurrency, req.TokenSize)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func (h *Handler) handleLoadTestStop(w http.ResponseWriter, r *http.Request) {
	resp, err := h.proxyToRunner("POST", "/stop", nil)
	if err != nil {
		// If runner is not available, just report stopped
		jsonResponse(w, map[string]string{"status": "stopped"})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	log.Printf("Load test stopped")

	// Clean up the runner pod after stopping
	h.deleteRunnerPod()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func (h *Handler) deleteRunnerPod() {
	h.runnerMu.Lock()
	defer h.runnerMu.Unlock()

	if h.runnerPodName == "" {
		return
	}
	ns := h.getAppNamespace()
	if err := h.k8sClient.DeletePod(ns, h.runnerPodName); err != nil {
		log.Printf("Warning: failed to delete runner pod: %v", err)
	} else {
		log.Printf("Runner pod %s deleted", h.runnerPodName)
	}
	h.runnerPodIP = ""
	h.runnerPodName = ""
	h.loadTestUser = ""
	h.loadTestModel = ""
	h.loadTestRunning = false
}

func (h *Handler) handleLoadTestStatus(w http.ResponseWriter, r *http.Request) {
	h.runnerMu.Lock()
	activeUser := h.loadTestUser
	activeModel := h.loadTestModel
	totalTests := h.loadTestTotal
	isRunning := h.loadTestRunning
	h.runnerMu.Unlock()

	resp, err := h.proxyToRunner("GET", "/status", nil)
	if err != nil {
		jsonResponse(w, map[string]interface{}{
			"running":           false,
			"totalRequests":     0,
			"activeUser":        "",
			"activeModel":       "",
			"totalTestsStarted": totalTests,
			"testRunning":       isRunning,
		})
		return
	}
	defer resp.Body.Close()

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		jsonResponse(w, map[string]interface{}{
			"running":           false,
			"totalRequests":     0,
			"totalTestsStarted": totalTests,
			"testRunning":       isRunning,
		})
		return
	}

	status["activeUser"] = activeUser
	status["activeModel"] = activeModel
	status["totalTestsStarted"] = totalTests
	status["testRunning"] = isRunning
	jsonResponse(w, status)
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	type serviceStatus struct {
		Name    string `json:"name"`
		Status  string `json:"status"` // "connected" or "unavailable"
		Details string `json:"details,omitempty"`
	}

	var services []serviceStatus

	// Kubernetes API
	if h.k8sClient != nil {
		if err := h.k8sClient.Ping(); err != nil {
			services = append(services, serviceStatus{Name: "Kubernetes API", Status: "unavailable", Details: "Cannot reach API server"})
		} else {
			services = append(services, serviceStatus{Name: "Kubernetes API", Status: "connected"})
		}
	} else {
		services = append(services, serviceStatus{Name: "Kubernetes API", Status: "unavailable", Details: "Not configured"})
	}

	// Thanos / Prometheus monitoring
	if h.monClient != nil {
		if err := h.monClient.Ping(); err != nil {
			services = append(services, serviceStatus{Name: "Monitoring (Thanos)", Status: "unavailable", Details: "Cannot reach Thanos querier"})
		} else {
			services = append(services, serviceStatus{Name: "Monitoring (Thanos)", Status: "connected"})
		}
	} else {
		services = append(services, serviceStatus{Name: "Monitoring (Thanos)", Status: "unavailable", Details: "Not configured"})
	}

	// User Workload Monitoring
	uwmEnabled := false
	if h.k8sClient != nil {
		uwmEnabled = h.k8sClient.IsUserWorkloadMonitoringEnabled()
	}
	if uwmEnabled {
		services = append(services, serviceStatus{Name: "User Workload Monitoring", Status: "connected"})
	} else {
		services = append(services, serviceStatus{Name: "User Workload Monitoring", Status: "unavailable", Details: "Not enabled on this cluster"})
	}

	// OpenShift Console
	if h.k8sClient != nil {
		if u := h.k8sClient.GetConsoleURL(); u != "" {
			services = append(services, serviceStatus{Name: "OpenShift Console", Status: "connected"})
		} else {
			services = append(services, serviceStatus{Name: "OpenShift Console", Status: "unavailable", Details: "Console URL not found"})
		}
	} else {
		services = append(services, serviceStatus{Name: "OpenShift Console", Status: "unavailable", Details: "Not configured"})
	}

	// RHOAI Dashboard
	if h.k8sClient != nil {
		if u := h.k8sClient.GetRHOAIDashboardURL(); u != "" {
			services = append(services, serviceStatus{Name: "RHOAI Dashboard", Status: "connected"})
		} else {
			services = append(services, serviceStatus{Name: "RHOAI Dashboard", Status: "unavailable", Details: "Dashboard route not found"})
		}
	} else {
		services = append(services, serviceStatus{Name: "RHOAI Dashboard", Status: "unavailable", Details: "Not configured"})
	}

	// KServe / InferenceServices
	if h.k8sClient != nil {
		if _, err := h.k8sClient.GetInferenceServices(); err != nil {
			services = append(services, serviceStatus{Name: "KServe InferenceServices", Status: "unavailable", Details: "Cannot query InferenceServices API"})
		} else {
			services = append(services, serviceStatus{Name: "KServe InferenceServices", Status: "connected"})
		}
	} else {
		services = append(services, serviceStatus{Name: "KServe InferenceServices", Status: "unavailable", Details: "Not configured"})
	}

	jsonResponse(w, map[string]interface{}{
		"services":                   services,
		"userWorkloadMonitoring":     uwmEnabled,
	})
}

// parsePrometheusText parses Prometheus exposition format and extracts
// vLLM-relevant gauge and counter values (summed across labels).
func parsePrometheusText(text string) (gauges map[string]float64, counters map[string]float64) {
	gauges = make(map[string]float64)
	counters = make(map[string]float64)

	// Track which metrics are which type
	metricTypes := make(map[string]string)

	// Metrics we care about (without label suffixes)
	wantGauges := map[string]bool{
		"vllm:num_requests_running":                true,
		"vllm:num_requests_waiting":                true,
		"vllm:num_requests_swapped":                true,
		"vllm:kv_cache_usage_perc":                 true,
		"vllm:gpu_cache_usage_perc":                true,
		"vllm:cpu_cache_usage_perc":                true,
		"vllm:avg_prompt_throughput_toks_per_s":     true,
		"vllm:avg_generation_throughput_toks_per_s": true,
		"vllm:num_gpu_blocks_used":                 true,
		"vllm:num_gpu_blocks_total":                true,
		"vllm:num_cpu_blocks_used":                 true,
		"vllm:num_cpu_blocks_total":                true,
		"vllm:num_preemptions_total":               true,
		"process_resident_memory_bytes":             true,
		"process_cpu_seconds_total":                 true,
	}

	wantCounters := map[string]bool{
		"vllm:request_success_total":        true,
		"vllm:generation_tokens_total":      true,
		"vllm:prompt_tokens_total":          true,
		"vllm:prefix_cache_hits_total":      true,
		"vllm:prefix_cache_queries_total":   true,
		"vllm:num_preemptions_total":        true,
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse TYPE lines
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				metricTypes[parts[2]] = parts[3]
			}
			continue
		}

		// Skip other comments
		if strings.HasPrefix(line, "#") {
			continue
		}

		// Parse metric line: metric_name{labels} value
		name, value := parseMetricLine(line)
		if name == "" {
			continue
		}

		// Strip _total suffix for matching, but keep original name
		baseName := name
		if strings.HasSuffix(baseName, "_total") {
			baseName = strings.TrimSuffix(baseName, "_total") + "_total"
		}

		if wantGauges[baseName] {
			gauges[baseName] += value
		}
		if wantCounters[baseName] {
			counters[baseName] += value
		}

		// Also handle histogram _sum and _count for live display
		if strings.HasSuffix(name, "_sum") || strings.HasSuffix(name, "_count") {
			stem := name
			if strings.HasSuffix(name, "_sum") {
				stem = strings.TrimSuffix(name, "_sum")
			} else {
				stem = strings.TrimSuffix(name, "_count")
			}
			// Include key histogram data
			switch stem {
			case "vllm:e2e_request_latency_seconds",
				"vllm:time_to_first_token_seconds",
				"vllm:inter_token_latency_seconds",
				"vllm:request_prompt_tokens",
				"vllm:request_generation_tokens":
				counters[name] += value
			}
		}
	}

	return gauges, counters
}

func parseMetricLine(line string) (string, float64) {
	// Handle metric_name{labels} value  or  metric_name value
	var name, valStr string

	braceIdx := strings.IndexByte(line, '{')
	if braceIdx >= 0 {
		name = line[:braceIdx]
		// Find closing brace then the value
		closeIdx := strings.LastIndexByte(line, '}')
		if closeIdx < 0 || closeIdx >= len(line)-1 {
			return "", 0
		}
		valStr = strings.TrimSpace(line[closeIdx+1:])
	} else {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", 0
		}
		name = parts[0]
		valStr = parts[1]
	}

	// Skip histogram bucket lines
	if strings.HasSuffix(name, "_bucket") {
		return "", 0
	}
	// Skip _created lines
	if strings.HasSuffix(name, "_created") {
		return "", 0
	}

	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return "", 0
	}

	return name, v
}

// firstMetricValue tries each PromQL query and returns the first that has a result.
func (h *Handler) firstMetricValue(queries []string) string {
	for _, q := range queries {
		results, err := h.monClient.Query(q)
		if err != nil {
			continue
		}
		if len(results) > 0 {
			if v, ok := results[0].Value[1].(string); ok && v != "NaN" {
				return v
			}
		}
	}
	return ""
}

func (h *Handler) firstMetricValueScaled(queries []string, scale float64) string {
	raw := h.firstMetricValue(queries)
	if raw == "" {
		return ""
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return fmt.Sprintf("%.1f", f*scale)
}

func (h *Handler) firstRangeSeries(queries []string, start, end time.Time, step time.Duration) []SeriesPoint {
	for _, q := range queries {
		results, err := h.monClient.QueryRange(q, start, end, step)
		if err != nil {
			continue
		}
		if len(results) > 0 && len(results[0].Values) > 0 {
			return promValuesToSeries(results[0].Values)
		}
	}
	return nil
}

func (h *Handler) firstRangeSeriesScaled(queries []string, start, end time.Time, step time.Duration, scale float64) []SeriesPoint {
	for _, q := range queries {
		results, err := h.monClient.QueryRange(q, start, end, step)
		if err != nil {
			continue
		}
		if len(results) > 0 && len(results[0].Values) > 0 {
			points := promValuesToSeries(results[0].Values)
			for i, p := range points {
				if f, err := strconv.ParseFloat(p.Value, 64); err == nil {
					points[i].Value = fmt.Sprintf("%.1f", f*scale)
				}
			}
			return points
		}
	}
	return nil
}

func promValuesToSeries(values [][2]interface{}) []SeriesPoint {
	points := make([]SeriesPoint, 0, len(values))
	for _, pair := range values {
		var ts int64
		switch t := pair[0].(type) {
		case float64:
			ts = int64(t)
		case json.Number:
			ts, _ = t.Int64()
		}
		val := ""
		if v, ok := pair[1].(string); ok {
			val = v
		}
		if val != "NaN" {
			points = append(points, SeriesPoint{Timestamp: ts, Value: val})
		}
	}
	return points
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		jsonError(w, "Cluster connection not configured", http.StatusServiceUnavailable)
		return
	}

	nodes, err := h.k8sClient.GetNodes()
	if err != nil {
		log.Printf("Failed to get nodes: %v", err)
		jsonError(w, "Failed to query nodes", http.StatusBadGateway)
		return
	}

	isvcList, err := h.k8sClient.GetInferenceServices()
	if err != nil {
		log.Printf("Failed to get InferenceServices: %v", err)
		isvcList = nil
	}

	servingPods, _ := h.k8sClient.GetAllPods("component=predictor")

	nodeInfos := make([]NodeInfo, 0, len(nodes))
	for _, n := range nodes {
		ni := NodeInfo{
			Name:             n.Metadata.Name,
			Status:           nodeStatus(n),
			CPUCapacity:      parseCPUCores(n.Status.Capacity["cpu"]),
			CPUAllocatable:   parseCPUCores(n.Status.Allocatable["cpu"]),
			MemCapacity:      parseMemoryBytes(n.Status.Capacity["memory"]),
			MemAllocatable:   parseMemoryBytes(n.Status.Allocatable["memory"]),
			GPUCapacity:      parseGPUResources(n.Status.Capacity),
			GPUAllocatable:   parseGPUResources(n.Status.Allocatable),
			GPUProduct:       detectGPUProduct(n.Metadata.Labels),
			GPUMemoryMB:      detectGPUMemoryMB(n.Metadata.Labels),
			GPUPhysicalCount: detectGPUPhysicalCount(n.Metadata.Labels),
			GPUPartitions:    detectGPUPartitions(n.Status.Capacity, n.Status.Allocatable),
		}
		ni.GPUSharing = detectGPUSharing(n.Metadata.Labels, ni.GPUCapacity, ni.GPUAllocatable)
		for _, addr := range n.Status.Addresses {
			if addr.Type == "InternalIP" {
				ni.InternalIP = addr.Address
				break
			}
		}
		nodeInfos = append(nodeInfos, ni)
	}

	models := make([]ModelInfo, 0)
	for _, isvc := range isvcList {
		mi := ModelInfo{
			Name:      isvc.Metadata.Name,
			Namespace: isvc.Metadata.Namespace,
			CreatedAt: isvc.Metadata.CreationTimestamp,
			Labels:    isvc.Metadata.Labels,
		}

		if rt, ok := isvc.Metadata.Annotations["serving.kserve.io/deploymentMode"]; ok {
			mi.Runtime = rt
		}
		if rt, ok := isvc.Metadata.Labels["serving.kserve.io/servingruntime"]; ok {
			mi.Runtime = rt
		}

		mi.Status = inferenceServiceStatus(isvc.Status)

		if urlMap, ok := isvc.Status["url"]; ok {
			if u, ok := urlMap.(string); ok {
				mi.URL = u
			}
		}

		for _, pod := range servingPods {
			if pod.Metadata.Namespace != isvc.Metadata.Namespace {
				continue
			}
			podISVC := pod.Metadata.Labels["serving.kserve.io/inferenceservice"]
			if podISVC == "" {
				podISVC = pod.Metadata.Labels["serving.knative.dev/service"]
			}
			if podISVC != isvc.Metadata.Name {
				continue
			}

			// Skip failed/terminated pods — don't count their resources
			if pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded" {
				continue
			}

			podInfo := ModelPodInfo{
				Name:     pod.Metadata.Name,
				NodeName: pod.Spec.NodeName,
				Phase:    pod.Status.Phase,
			}
			for _, c := range pod.Spec.Containers {
				gpuReq := parseGPUResources(c.Resources.Requests)
				if gpuReq == 0 {
					gpuReq = parseGPUResources(c.Resources.Limits)
				}
				podInfo.GPUsRequested += gpuReq
				if c.Resources.Requests["cpu"] != "" {
					podInfo.CPUsRequested += parseCPUCores(c.Resources.Requests["cpu"])
				}
				if c.Resources.Limits["cpu"] != "" {
					podInfo.CPULimits += parseCPUCores(c.Resources.Limits["cpu"])
				}
				if c.Resources.Requests["memory"] != "" {
					podInfo.MemRequested += parseMemoryBytes(c.Resources.Requests["memory"])
				}
				if c.Resources.Limits["memory"] != "" {
					podInfo.MemLimits += parseMemoryBytes(c.Resources.Limits["memory"])
				}
			}
			mi.ModelPods = append(mi.ModelPods, podInfo)
			mi.GPUsRequested += podInfo.GPUsRequested
			if mi.NodeName == "" {
				mi.NodeName = pod.Spec.NodeName
			}
			mi.CPUsRequested += podInfo.CPUsRequested
			mi.CPULimits += podInfo.CPULimits
			mi.MemRequested += podInfo.MemRequested
			mi.MemLimits += podInfo.MemLimits
		}

		for i := range nodeInfos {
			for _, pod := range mi.ModelPods {
				if pod.NodeName == nodeInfos[i].Name {
					nodeInfos[i].ModelsRunning++
					nodeInfos[i].GPUsUsed += pod.GPUsRequested
				}
			}
		}

		models = append(models, mi)
	}

	// Enrich models with runtime metadata and GPU product info
	for i := range models {
		// GPU product from the node
		for _, n := range nodeInfos {
			if n.Name == models[i].NodeName && n.GPUProduct != "" {
				models[i].GPUProduct = n.GPUProduct
				break
			}
		}
		// Fetch vLLM/TGI metadata from the predictor pod
		if len(models[i].ModelPods) > 0 {
			h.enrichModelMetadata(&models[i], servingPods)
		}
	}

	resp := OverviewResponse{
		Nodes:  nodeInfos,
		Models: models,
	}
	resp.ConsoleURL = h.k8sClient.GetConsoleURL()
	resp.DashboardURL = h.k8sClient.GetRHOAIDashboardURL()

	jsonResponse(w, resp)
}

func (h *Handler) handleNetworkPolicies(w http.ResponseWriter, r *http.Request) {
	if h.k8sClient == nil {
		jsonError(w, "Cluster connection not configured", http.StatusServiceUnavailable)
		return
	}

	ns := r.URL.Query().Get("namespace")

	var policies []k8s.NetworkPolicy
	var err error

	if ns != "" {
		policies, err = h.k8sClient.GetNetworkPolicies(ns)
	} else {
		isvcList, isvcErr := h.k8sClient.GetInferenceServices()
		if isvcErr != nil {
			policies, err = h.k8sClient.GetAllNetworkPolicies()
		} else {
			nsSet := make(map[string]bool)
			for _, isvc := range isvcList {
				nsSet[isvc.Metadata.Namespace] = true
			}
			for namespace := range nsSet {
				nsPolicies, nsErr := h.k8sClient.GetNetworkPolicies(namespace)
				if nsErr != nil {
					log.Printf("Failed to get network policies for %s: %v", namespace, nsErr)
					continue
				}
				policies = append(policies, nsPolicies...)
			}
		}
	}
	if err != nil {
		log.Printf("Failed to get network policies: %v", err)
		jsonError(w, "Failed to query network policies", http.StatusBadGateway)
		return
	}

	readable := make([]NetworkPolicyReadable, 0, len(policies))
	for _, np := range policies {
		readable = append(readable, formatNetworkPolicy(np))
	}

	jsonResponse(w, readable)
}

// --- Helpers ---

func nodeStatus(n k8s.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			if c.Status == "True" {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

func inferenceServiceStatus(status map[string]interface{}) string {
	if status == nil {
		return "Unknown"
	}
	conditions, ok := status["conditions"]
	if !ok {
		return "Unknown"
	}
	condList, ok := conditions.([]interface{})
	if !ok {
		return "Unknown"
	}
	for _, c := range condList {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			if cond["status"] == "True" {
				return "Ready"
			}
			if msg, ok := cond["message"].(string); ok && msg != "" {
				return msg
			}
			return "NotReady"
		}
	}
	return "Pending"
}

func formatNetworkPolicy(np k8s.NetworkPolicy) NetworkPolicyReadable {
	r := NetworkPolicyReadable{
		Name:      np.Metadata.Name,
		Namespace: np.Metadata.Namespace,
		Types:     np.Spec.PolicyTypes,
	}

	if len(np.Spec.PodSelector.MatchLabels) == 0 && len(np.Spec.PodSelector.MatchExpressions) == 0 {
		r.Target = "All pods in namespace"
	} else {
		r.Target = "Pods matching: " + formatSelector(np.Spec.PodSelector)
	}

	for _, rule := range np.Spec.Ingress {
		r.Rules = append(r.Rules, formatIngressRule(rule))
	}
	for _, rule := range np.Spec.Egress {
		r.Rules = append(r.Rules, formatEgressRule(rule))
	}

	if len(np.Spec.Ingress) == 0 && containsStr(np.Spec.PolicyTypes, "Ingress") {
		r.Rules = append(r.Rules, "DENY all inbound traffic")
	}
	if len(np.Spec.Egress) == 0 && containsStr(np.Spec.PolicyTypes, "Egress") {
		r.Rules = append(r.Rules, "DENY all outbound traffic")
	}

	return r
}

func formatIngressRule(rule k8s.NetworkPolicyRule) string {
	parts := []string{"ALLOW inbound"}

	if len(rule.Ports) > 0 {
		portStrs := make([]string, 0, len(rule.Ports))
		for _, p := range rule.Ports {
			proto := "TCP"
			if p.Protocol != "" {
				proto = p.Protocol
			}
			portStr := fmt.Sprintf("%v/%s", p.Port, proto)
			portStrs = append(portStrs, portStr)
		}
		parts = append(parts, "on "+strings.Join(portStrs, ", "))
	}

	if len(rule.From) > 0 {
		fromParts := make([]string, 0)
		for _, peer := range rule.From {
			fromParts = append(fromParts, formatPeer(peer))
		}
		parts = append(parts, "from "+strings.Join(fromParts, " OR "))
	} else {
		parts = append(parts, "from anywhere")
	}

	return strings.Join(parts, " ")
}

func formatEgressRule(rule k8s.NetworkPolicyRule) string {
	parts := []string{"ALLOW outbound"}

	if len(rule.Ports) > 0 {
		portStrs := make([]string, 0, len(rule.Ports))
		for _, p := range rule.Ports {
			proto := "TCP"
			if p.Protocol != "" {
				proto = p.Protocol
			}
			portStr := fmt.Sprintf("%v/%s", p.Port, proto)
			portStrs = append(portStrs, portStr)
		}
		parts = append(parts, "on "+strings.Join(portStrs, ", "))
	}

	if len(rule.To) > 0 {
		toParts := make([]string, 0)
		for _, peer := range rule.To {
			toParts = append(toParts, formatPeer(peer))
		}
		parts = append(parts, "to "+strings.Join(toParts, " OR "))
	} else {
		parts = append(parts, "to anywhere")
	}

	return strings.Join(parts, " ")
}

func formatPeer(peer k8s.NetworkPolicyPeer) string {
	parts := make([]string, 0)

	if peer.IPBlock != nil {
		s := "CIDR " + peer.IPBlock.CIDR
		if len(peer.IPBlock.Except) > 0 {
			s += " (except " + strings.Join(peer.IPBlock.Except, ", ") + ")"
		}
		parts = append(parts, s)
	}
	if peer.NamespaceSelector != nil {
		if len(peer.NamespaceSelector.MatchLabels) == 0 && len(peer.NamespaceSelector.MatchExpressions) == 0 {
			parts = append(parts, "all namespaces")
		} else {
			parts = append(parts, "namespaces matching "+formatSelector(*peer.NamespaceSelector))
		}
	}
	if peer.PodSelector != nil {
		if len(peer.PodSelector.MatchLabels) == 0 && len(peer.PodSelector.MatchExpressions) == 0 {
			parts = append(parts, "all pods")
		} else {
			parts = append(parts, "pods matching "+formatSelector(*peer.PodSelector))
		}
	}

	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, " in ")
}

func formatSelector(sel k8s.LabelSelector) string {
	parts := make([]string, 0)
	for k, v := range sel.MatchLabels {
		parts = append(parts, k+"="+v)
	}
	for _, expr := range sel.MatchExpressions {
		switch expr.Operator {
		case "In":
			parts = append(parts, expr.Key+" in ("+strings.Join(expr.Values, ", ")+")")
		case "NotIn":
			parts = append(parts, expr.Key+" not in ("+strings.Join(expr.Values, ", ")+")")
		case "Exists":
			parts = append(parts, expr.Key+" exists")
		case "DoesNotExist":
			parts = append(parts, expr.Key+" does not exist")
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// gpuResourceKeys lists the known whole-GPU resource names across vendors.
var gpuResourceKeys = []string{
	"nvidia.com/gpu",
	"amd.com/gpu",
	"gpu.intel.com/i915",
	"gpu.intel.com/xe",
}

// gpuPartitionPrefixes lists resource prefixes for partitioned GPU resources.
var gpuPartitionPrefixes = []string{
	"nvidia.com/mig-",     // NVIDIA MIG (e.g., nvidia.com/mig-1g.5gb)
	"amd.com/gpu-partition", // AMD SR-IOV GPU partitioning
}

// parseGPUResources sums GPU counts across all known vendor resource keys.
func parseGPUResources(resources map[string]string) int64 {
	var total int64
	for _, key := range gpuResourceKeys {
		if v := resources[key]; v != "" {
			total += parseResourceInt(v)
		}
	}
	// Also count partitioned GPU resources
	for k, v := range resources {
		for _, prefix := range gpuPartitionPrefixes {
			if strings.HasPrefix(k, prefix) {
				total += parseResourceInt(v)
			}
		}
	}
	return total
}

// detectGPUPartitions finds partitioned GPU resources (MIG, SR-IOV, etc.) in a resource list.
func detectGPUPartitions(capacity, allocatable map[string]string) []GPUPartition {
	var parts []GPUPartition
	for k, v := range capacity {
		isPartition := false
		for _, prefix := range gpuPartitionPrefixes {
			if strings.HasPrefix(k, prefix) {
				isPartition = true
				break
			}
		}
		if isPartition {
			p := GPUPartition{
				Resource: k,
				Capacity: parseResourceInt(v),
			}
			if av, ok := allocatable[k]; ok {
				p.Allocatable = parseResourceInt(av)
			}
			parts = append(parts, p)
		}
	}
	return parts
}

// detectGPUProduct reads GPU vendor/product info from node labels.
func detectGPUProduct(labels map[string]string) string {
	// NVIDIA
	if p := labels["nvidia.com/gpu.product"]; p != "" {
		return p
	}
	// AMD - typically set by AMD GPU operator
	if p := labels["amd.com/gpu.product"]; p != "" {
		return p
	}
	// Intel
	if p := labels["gpu.intel.com/device-id.0300"]; p != "" {
		return "Intel GPU " + p
	}
	return ""
}

// detectGPUSharing reads GPU sharing strategy from node labels.
func detectGPUSharing(labels map[string]string, capacity, allocatable int64) string {
	// NVIDIA sharing strategy
	if s := labels["nvidia.com/gpu.sharing-strategy"]; s != "" && s != "none" {
		return s // "time-slicing", "mps", "mig"
	}
	// If allocatable > capacity, some form of sharing/partitioning is active
	if capacity > 0 && allocatable > capacity {
		return "partitioned"
	}
	return ""
}

// detectGPUPhysicalCount returns the physical GPU count from labels.
func detectGPUPhysicalCount(labels map[string]string) int64 {
	if c := labels["nvidia.com/gpu.count"]; c != "" {
		return parseResourceInt(c)
	}
	return 0
}

// detectGPUMemoryMB returns GPU memory in MB from labels.
func detectGPUMemoryMB(labels map[string]string) int64 {
	if m := labels["nvidia.com/gpu.memory"]; m != "" {
		return parseResourceInt(m)
	}
	return 0
}

func parseResourceInt(s string) int64 {
	if s == "" {
		return 0
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if strings.HasSuffix(s, "m") {
		if v, err := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64); err == nil {
			return v / 1000
		}
	}
	return 0
}

// parseCPUCores parses a Kubernetes CPU resource value and returns cores as float64.
// e.g. "4" -> 4.0, "500m" -> 0.5, "2500m" -> 2.5
func parseCPUCores(s string) float64 {
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "m") {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64); err == nil {
			return v / 1000
		}
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return 0
}

// parseMemoryBytes parses a Kubernetes memory resource value and returns bytes as int64.
// Supports Ki, Mi, Gi, Ti suffixes and plain bytes.
func parseMemoryBytes(s string) int64 {
	if s == "" {
		return 0
	}
	multipliers := []struct {
		suffix string
		mult   int64
	}{
		{"Ti", 1024 * 1024 * 1024 * 1024},
		{"Gi", 1024 * 1024 * 1024},
		{"Mi", 1024 * 1024},
		{"Ki", 1024},
		{"T", 1000000000000},
		{"G", 1000000000},
		{"M", 1000000},
		{"K", 1000},
	}
	for _, m := range multipliers {
		if strings.HasSuffix(s, m.suffix) {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(s, m.suffix), 64); err == nil {
				return int64(v * float64(m.mult))
			}
		}
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return 0
}

// enrichModelMetadata fetches runtime metadata from the model's predictor pod API.
func (h *Handler) enrichModelMetadata(mi *ModelInfo, allPods []k8s.Pod) {
	// Find the pod IP for this model
	var podIP string
	for _, pod := range allPods {
		if pod.Metadata.Namespace != mi.Namespace {
			continue
		}
		podISVC := pod.Metadata.Labels["serving.kserve.io/inferenceservice"]
		if podISVC == "" {
			podISVC = pod.Metadata.Labels["serving.knative.dev/service"]
		}
		if podISVC == mi.Name && pod.Status.Phase == "Running" {
			podIP = pod.Status.PodIP
			break
		}
	}
	if podIP == "" {
		return
	}

	client := &http.Client{Timeout: 3 * time.Second}

	// Fetch /version
	if resp, err := client.Get(fmt.Sprintf("http://%s:8080/version", podIP)); err == nil {
		defer resp.Body.Close()
		var ver struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(resp.Body).Decode(&ver) == nil && ver.Version != "" {
			mi.EngineVersion = ver.Version
		}
	}

	// Fetch /v1/models
	if resp, err := client.Get(fmt.Sprintf("http://%s:8080/v1/models", podIP)); err == nil {
		defer resp.Body.Close()
		var modelsResp struct {
			Data []struct {
				ID          string `json:"id"`
				Root        string `json:"root"`
				MaxModelLen int64  `json:"max_model_len"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&modelsResp) == nil && len(modelsResp.Data) > 0 {
			mi.MaxModelLen = modelsResp.Data[0].MaxModelLen
			mi.ModelRoot = modelsResp.Data[0].Root
		}
	}
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("JSON encode error: %v", err)
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
