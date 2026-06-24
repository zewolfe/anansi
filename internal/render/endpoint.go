package render

import (
	"fmt"
	"net/url"
	"strings"
)

type EndpointMode string

const (
	ModeInCluster EndpointMode = "in-cluster"
	ModeExternal  EndpointMode = "external"
)

type EndpointConfig struct {
	Mode          EndpointMode
	BaseURL       string
	InferencePath string
}

type Target struct {
	URL  string
	Host string
}

type Endpoint struct {
	URL  string
	Host string
}

func Resolve(cfg EndpointConfig, clusterLocalURL, externalURL string) (Target, error) {
	if cfg.InferencePath == "" {
		return Target{}, fmt.Errorf("endpoint: InferencePath is empty; set it to the runtime's inference path (e.g. /v1/chat/completions)")
	}

	switch cfg.Mode {
	case ModeInCluster:
		if clusterLocalURL == "" {
			return Target{}, fmt.Errorf("endpoint: in-cluster mode but ISVC has no cluster-local address (Status.Address.URL); the service may not be admitted yet")
		}
		u, err := joinURL(clusterLocalURL, cfg.InferencePath)
		if err != nil {
			return Target{}, err
		}
		return Target{URL: u, Host: ""}, nil

	case ModeExternal:
		if cfg.BaseURL == "" {
			return Target{}, fmt.Errorf("endpoint: external mode requires BaseURL (NodePort or port-forward address)")
		}
		if externalURL == "" {
			return Target{}, fmt.Errorf("endpoint: external mode requires the ISVC external URL (Status.URL) to derive the Host header")
		}
		ext, err := url.Parse(externalURL)
		if err != nil {
			return Target{}, fmt.Errorf("endpoint: parsing external URL %q: %w", externalURL, err)
		}
		u, err := joinURL(cfg.BaseURL, cfg.InferencePath)
		if err != nil {
			return Target{}, err
		}

		return Target{URL: u, Host: ext.Host}, nil

	default:
		return Target{}, fmt.Errorf("endpoint: unknown mode %q (want %q or %q)", cfg.Mode, ModeInCluster, ModeExternal)
	}
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("endpoint: parsing base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return u.String(), nil
}

func InferenceEndpoint(cfg EndpointConfig, isvcName, namespace string) Endpoint {
	path := cfg.InferencePath
	mode := cfg.Mode
	baseURL := cfg.BaseURL

	if cfg.InferencePath == "" {
		path = "/v1/completions"
	}
	host := fmt.Sprintf("%s.%s.svc.cluster.local", isvcName, namespace)

	if mode == ModeExternal {
		return Endpoint{
			URL:  strings.TrimRight(baseURL, "/") + path,
			Host: host,
		}
	}
	return Endpoint{
		URL:  "http://" + host + path,
		Host: "",
	}
}
