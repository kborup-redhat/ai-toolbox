package auth

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kborup-redhat/ai-toolbox/internal/k8s"
)

// GroupChecker validates that a user belongs to at least one allowed group.
type GroupChecker struct {
	k8sClient     *k8s.Client
	configPath    string
	mu            sync.RWMutex
	allowedGroups []string
	lastLoad      time.Time
}

// NewGroupChecker creates a checker that reads allowed groups from a file.
// The file is re-read every 30 seconds to pick up ConfigMap changes.
func NewGroupChecker(k8sClient *k8s.Client, configPath string) *GroupChecker {
	gc := &GroupChecker{
		k8sClient:  k8sClient,
		configPath: configPath,
	}
	gc.loadGroups()
	return gc
}

func (gc *GroupChecker) loadGroups() {
	data, err := os.ReadFile(gc.configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: could not read allowed-groups file %s: %v", gc.configPath, err)
		}
		gc.mu.Lock()
		gc.allowedGroups = nil
		gc.lastLoad = time.Now()
		gc.mu.Unlock()
		return
	}

	raw := strings.TrimSpace(string(data))
	var groups []string
	if raw != "" {
		for _, g := range strings.Split(raw, ",") {
			g = strings.TrimSpace(g)
			if g != "" {
				groups = append(groups, g)
			}
		}
	}

	gc.mu.Lock()
	gc.allowedGroups = groups
	gc.lastLoad = time.Now()
	gc.mu.Unlock()

	if len(groups) > 0 {
		log.Printf("Allowed groups loaded: %v", groups)
	} else {
		log.Printf("No group restrictions configured - all authenticated users allowed")
	}
}

func (gc *GroupChecker) getGroups() []string {
	gc.mu.RLock()
	age := time.Since(gc.lastLoad)
	gc.mu.RUnlock()

	if age > 30*time.Second {
		gc.loadGroups()
	}

	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.allowedGroups
}

// Middleware returns an HTTP middleware that checks group membership.
// If no groups are configured, all authenticated users are allowed.
func (gc *GroupChecker) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always allow static assets
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		allowed := gc.getGroups()
		if len(allowed) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		username := r.Header.Get("X-Forwarded-User")
		if username == "" {
			http.Error(w, "Unauthorized: no user identity", http.StatusUnauthorized)
			return
		}

		// Users with cluster-admin ClusterRoleBinding are always allowed
		if gc.k8sClient != nil && gc.k8sClient.IsClusterAdmin(username) {
			next.ServeHTTP(w, r)
			return
		}

		userGroups, err := gc.getUserGroups(username)
		if err != nil {
			log.Printf("Failed to look up groups for user %s: %v", username, err)
			gc.denyAccess(w, username)
			return
		}

		for _, ug := range userGroups {
			for _, ag := range allowed {
				if ug == ag {
					next.ServeHTTP(w, r)
					return
				}
			}
		}

		log.Printf("Access denied for user %s (groups: %v, allowed: %v)", username, userGroups, allowed)
		gc.denyAccess(w, username)
	})
}

func (gc *GroupChecker) denyAccess(w http.ResponseWriter, username string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Access Denied</title>
<link rel="stylesheet" href="https://unpkg.com/@patternfly/patternfly@5/patternfly.min.css">
</head><body>
<div style="display:flex;align-items:center;justify-content:center;min-height:100vh;">
<div class="pf-v5-c-empty-state"><div class="pf-v5-c-empty-state__content">
<h2 class="pf-v5-c-title pf-m-lg">Access Denied</h2>
<div class="pf-v5-c-empty-state__body">
Your account <strong>%s</strong> is not a member of any authorized group.<br>
Contact your cluster administrator to request access.
</div></div></div></div></body></html>`, html.EscapeString(username))
}

func (gc *GroupChecker) getUserGroups(username string) ([]string, error) {
	if gc.k8sClient == nil {
		return nil, fmt.Errorf("no cluster connection")
	}
	return gc.k8sClient.GetUserGroups(username)
}

// AddGetUserGroups adds the method to the k8s client via this package
// to avoid circular imports. The actual implementation is on the k8s.Client.
func init() {
	// registration happens via k8s package directly
}

// GroupList is the OpenShift API response for groups.
type GroupList struct {
	Items []Group `json:"items"`
}

type Group struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Users []string `json:"users"`
}

// GetUserGroupsDirect queries the OpenShift API for groups containing a user.
// This is a standalone function for use when the k8s client method isn't available.
func GetUserGroupsDirect(apiURL, token string, insecureSkipVerify bool, username string) ([]string, error) {
	client := k8s.NewClient(apiURL, token, insecureSkipVerify)
	return client.GetUserGroups(username)
}

// ParseGroupList parses the OpenShift group list API response.
func ParseGroupList(data []byte, username string) ([]string, error) {
	var list GroupList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing groups: %w", err)
	}
	var groups []string
	for _, g := range list.Items {
		for _, u := range g.Users {
			if u == username {
				groups = append(groups, g.Metadata.Name)
				break
			}
		}
	}
	return groups, nil
}
