package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	ListenAddr        string
	AllowedGroupsFile string
	OpenShift         OpenShiftConfig
}

type OpenShiftConfig struct {
	APIURL             string
	Token              string
	ClusterDomain      string
	InsecureSkipVerify bool
}

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:        envOr("LISTEN_ADDR", ":8080"),
		AllowedGroupsFile: envOr("ALLOWED_GROUPS_FILE", "/etc/ai-toolbox/allowed-groups"),
	}

	cfg.OpenShift.APIURL = os.Getenv("OPENSHIFT_API_URL")
	cfg.OpenShift.Token = os.Getenv("OPENSHIFT_TOKEN")
	cfg.OpenShift.ClusterDomain = os.Getenv("CLUSTER_DOMAIN")
	cfg.OpenShift.InsecureSkipVerify = os.Getenv("INSECURE_SKIP_VERIFY") == "true"

	if cfg.OpenShift.APIURL == "" {
		host := os.Getenv("KUBERNETES_SERVICE_HOST")
		port := os.Getenv("KUBERNETES_SERVICE_PORT")
		if host != "" && port != "" {
			cfg.OpenShift.APIURL = fmt.Sprintf("https://%s:%s", host, port)
		}
	}

	if cfg.OpenShift.Token == "" {
		token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
		if err == nil {
			cfg.OpenShift.Token = strings.TrimSpace(string(token))
		}
	}

	cfg.OpenShift.APIURL = strings.TrimRight(cfg.OpenShift.APIURL, "/")

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
