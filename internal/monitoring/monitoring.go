package monitoring

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(clusterDomain, token string, insecureSkipVerify bool) *Client {
	baseURL := fmt.Sprintf("https://thanos-querier-openshift-monitoring.%s", clusterDomain)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify,
	}
	if !insecureSkipVerify {
		if caData, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(caData)
			tlsConfig.RootCAs = pool
		}
	}

	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		},
	}
}

// PromResponse is the Prometheus HTTP API response envelope.
type PromResponse struct {
	Status string   `json:"status"`
	Data   PromData `json:"data"`
}

type PromData struct {
	ResultType string       `json:"resultType"`
	Result     []PromResult `json:"result"`
}

type PromResult struct {
	Metric map[string]string `json:"metric"`
	Value  [2]interface{}    `json:"value"`  // [timestamp, "value"]
	Values [][2]interface{}  `json:"values"` // for range queries
}

// Query runs an instant PromQL query.
func (c *Client) Query(promql string) ([]PromResult, error) {
	u := c.baseURL + "/api/v1/query?query=" + url.QueryEscape(promql)
	data, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var resp PromResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: status=%s", resp.Status)
	}
	return resp.Data.Result, nil
}

// QueryRange runs a range PromQL query over a time window.
func (c *Client) QueryRange(promql string, start, end time.Time, step time.Duration) ([]PromResult, error) {
	params := url.Values{}
	params.Set("query", promql)
	params.Set("start", fmt.Sprintf("%d", start.Unix()))
	params.Set("end", fmt.Sprintf("%d", end.Unix()))
	params.Set("step", fmt.Sprintf("%ds", int(step.Seconds())))

	u := c.baseURL + "/api/v1/query_range?" + params.Encode()
	data, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var resp PromResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus range query failed: status=%s", resp.Status)
	}
	return resp.Data.Result, nil
}

func (c *Client) get(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 10 << 20 // 10 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("authentication failed (HTTP %d) – SA may lack monitoring access", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// Ping checks if Thanos/Prometheus is reachable by running a simple query.
func (c *Client) Ping() error {
	_, err := c.Query("up{job=\"apiserver\"}")
	return err
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
