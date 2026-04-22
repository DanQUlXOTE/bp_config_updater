package bpclient

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	user    string
	pass    string
	http    *http.Client
}

type Options struct {
	BaseURL       string
	APIKey        string
	User          string
	Pass          string
	SkipTLSVerify bool
	Timeout       time.Duration
}

func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("baseURL required")
	}
	if opts.APIKey == "" && opts.User == "" {
		return nil, fmt.Errorf("either API key or user/pass required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	tr := &http.Transport{}
	if opts.SkipTLSVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	return &Client{
		baseURL: base,
		apiKey:  opts.APIKey,
		user:    opts.User,
		pass:    opts.Pass,
		http:    &http.Client{Timeout: opts.Timeout, Transport: tr},
	}, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	if c.apiKey != "" {
		req.Header.Set("X-Bindplane-Api-Key", c.apiKey)
	} else {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const maxResponseBytes = 50 << 20 // 50 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("bindplane %s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, truncate(string(body), 500))
	}
	return body, nil
}

// GetConfigurationRaw fetches the configuration and returns the raw JSON
// body so the caller can round-trip it without dropping unknown fields.
func (c *Client) GetConfigurationRaw(name string) ([]byte, error) {
	u := c.baseURL + "/v1/configurations/" + url.PathEscape(name)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

// Apply posts a list of resources to /v1/apply. Each resource is a
// decoded map (from YAML/JSON) representing a Bindplane resource.
func (c *Client) Apply(resources []map[string]any) ([]byte, error) {
	payload := map[string]any{"resources": resources}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u := c.baseURL + "/v1/apply"
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
