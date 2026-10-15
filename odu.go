package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"
)

var (
	errMonitoringDisabled = errors.New("ODU monitoring is disabled")
	errPasswordMissing    = errors.New("ODU password is missing")
	errODULogin           = errors.New("ODU login failed")
	errODUUnavailable     = errors.New("ODU is unavailable")
	errRebootUnconfirmed  = errors.New("ODU reboot result is unconfirmed")
)

const (
	oduRequestTimeout = 8 * time.Second
	oduResponseLimit  = 64 << 10
)

type oduConfig struct {
	Host, Username, Password string
	Enabled                  bool
}

// oduClient keeps authentication state only in memory. The gate serializes
// token/cookie updates while allowing queued calls to honor context cancellation.
type oduClient struct {
	gate    chan struct{}
	config  oduConfig
	haveCfg bool
	baseURL string // Empty in production; set only by tests.
	http    *http.Client
	token   string
	authed  bool
}

func newODUCookieJar() *cookiejar.Jar {
	jar, _ := cookiejar.New(nil)
	return jar
}

func newODUClient() *oduClient {
	transport := &http.Transport{
		Proxy:                  nil,
		MaxResponseHeaderBytes: oduResponseLimit,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // ODU commonly uses a self-signed management certificate.
		},
	}
	return &oduClient{gate: make(chan struct{}, 1), http: &http.Client{
		Transport: transport,
		Jar:       newODUCookieJar(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (c *oduClient) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *oduClient) release() { <-c.gate }

func (c *oduClient) resetAuth() {
	c.token = ""
	c.authed = false
	if c.http != nil {
		c.http.Jar = newODUCookieJar()
	}
}

func (c *oduClient) reset(cfg oduConfig) {
	c.resetAuth()
	c.config = cfg
	c.haveCfg = true
}

func (c *oduClient) prepareConfig(cfg oduConfig) error {
	if cfg.Password == "" {
		return errPasswordMissing
	}
	if !validHost(cfg.Host) || cfg.Username == "" || len(cfg.Username) > 64 || len(cfg.Password) > 256 {
		return errors.New("invalid ODU configuration")
	}
	if !c.haveCfg || c.config != cfg {
		c.reset(cfg)
	}
	return nil
}

func (c *oduClient) endpoint(cfg oduConfig, path string) (string, error) {
	if c.baseURL != "" {
		return strings.TrimRight(c.baseURL, "/") + path, nil
	}
	// Construct the authority from a validated host, never from a user URL.
	if strings.ContainsAny(cfg.Host, "/?#@") {
		return "", errors.New("invalid ODU host")
	}
	return "https://" + net.JoinHostPort(cfg.Host, "443") + path, nil
}

type protocolError struct{ expired bool }

func (e protocolError) Error() string { return "ODU rejected the request" }

func (c *oduClient) post(ctx context.Context, cfg oduConfig, path string, payload any) (map[string]any, error) {
	obj, _, err := c.postAttempt(ctx, cfg, path, payload)
	return obj, err
}

func (c *oduClient) postAttempt(ctx context.Context, cfg oduConfig, path string, payload any) (map[string]any, bool, error) {
	if c.http == nil {
		return nil, false, errors.New("ODU client unavailable")
	}
	endpoint, err := c.endpoint(cfg, path)
	if err != nil {
		return nil, false, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.New("could not encode ODU request")
	}
	reqCtx, cancel := context.WithTimeout(ctx, oduRequestTimeout)
	defer cancel()
	var sent atomic.Bool
	reqCtx = httptrace.WithClientTrace(reqCtx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { sent.Store(true) },
	})
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, errors.New("could not create ODU request")
	}
	req.Header.Set("Content-Type", "application/json")
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, sent.Load(), err // Caller maps this without exposing transport details.
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, true, protocolError{expired: true}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, true, protocolError{expired: true}
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil, true, protocolError{expired: true}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, oduResponseLimit+1))
	if err != nil {
		return nil, true, errors.New("invalid ODU response size")
	}
	if isHTMLResponse(resp.Header.Get("Content-Type"), raw) {
		return nil, true, protocolError{expired: true}
	}
	if len(raw) > oduResponseLimit {
		return nil, true, errors.New("invalid ODU response size")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, true, errors.New("ODU returned an HTTP error")
	}
	obj, err := decodeODUObject(raw)
	if err != nil {
		return nil, true, err
	}
	if apiError(obj) {
		return nil, true, protocolError{expired: true}
	}
	if token, ok := obj["RequestVerifyToken"].(string); ok && token != "" {
		c.token = token
	}
	return obj, true, nil
}

func isHTMLResponse(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(strings.ToLower(trimmed), "<!doctype html") || strings.HasPrefix(strings.ToLower(trimmed), "<html")
}

func decodeODUObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]any
	if err := dec.Decode(&value); err != nil || value == nil {
		return nil, errors.New("invalid ODU response")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid ODU response")
	}
	return value, nil
}

func apiError(obj map[string]any) bool {
	if result, exists := obj["result"]; exists {
		if ok, valid := result.(bool); valid && !ok {
			return true
		}
	}
	if value, exists := obj["error"]; exists && value != nil {
		if text, ok := value.(string); !ok || text != "" {
			return true
		}
	}
	return false
}

func (c *oduClient) authenticate(ctx context.Context, cfg oduConfig) error {
	obj, err := c.post(ctx, cfg, "/cgi-bin/cgi/token_query.cgi", map[string]any{})
	if err != nil {
		return err
	}
	token, ok := obj["RequestVerifyToken"].(string)
	if !ok || token == "" {
		return errors.New("ODU token unavailable")
	}
	c.token = token
	obj, err = c.post(ctx, cfg, "/cgi-bin/cgi/login_req.cgi", map[string]any{
		"RequestVerifyToken": c.token,
		"usr":                cfg.Username,
		"pwd":                cfg.Password,
	})
	if err != nil {
		var pe protocolError
		if errors.As(err, &pe) && pe.expired {
			return errODULogin
		}
		return err
	}
	ok, valid := obj["result"].(bool)
	if !valid || !ok {
		return errODULogin
	}
	c.authed = true
	return nil
}

func (c *oduClient) ensureAuth(ctx context.Context, cfg oduConfig) error {
	if c.authed && c.token != "" {
		return nil
	}
	return c.authenticate(ctx, cfg)
}

func (c *oduClient) readOnce(ctx context.Context, cfg oduConfig) (reading, error) {
	if err := c.ensureAuth(ctx, cfg); err != nil {
		return reading{}, err
	}
	netObj, err := c.post(ctx, cfg, "/cgi-bin/cgi/net_status_retrieve.cgi", map[string]any{"RequestVerifyToken": c.token})
	if err != nil {
		return reading{}, err
	}
	dataObj, err := c.post(ctx, cfg, "/cgi-bin/cgi/data_status.cgi", map[string]any{"RequestVerifyToken": c.token, "operation": "get"})
	if err != nil {
		return reading{}, err
	}
	return reading{Net: stripToken(netObj), Data: stripToken(dataObj)}, nil
}

func stripToken(obj map[string]any) map[string]any {
	result := make(map[string]any, len(obj))
	for key, value := range obj {
		switch strings.ToLower(key) {
		case "requestverifytoken", "token", "password", "pwd", "username", "usr":
			continue
		}
		switch value.(type) {
		case nil, string, bool, float64, json.Number:
			result[key] = value
		}
	}
	return result
}

func (c *oduClient) Read(ctx context.Context, cfg oduConfig) (reading, error) {
	if err := c.acquire(ctx); err != nil {
		return reading{}, err
	}
	defer c.release()
	if !cfg.Enabled {
		if !c.haveCfg || c.config != cfg {
			c.reset(cfg)
		}
		return reading{}, errMonitoringDisabled
	}
	if err := c.prepareConfig(cfg); err != nil {
		return reading{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := c.readOnce(ctx, cfg)
		var expired protocolError
		if errors.As(err, &expired) && expired.expired && attempt == 0 {
			c.resetAuth()
			continue
		}
		if err != nil {
			if errors.Is(err, errODULogin) {
				return reading{}, errODULogin
			}
			return reading{}, sanitizedReadError(ctx, err)
		}
		return result, nil
	}
	return reading{}, errODUUnavailable
}

func sanitizedReadError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return errODUUnavailable
}

func (c *oduClient) Reboot(ctx context.Context, cfg oduConfig) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	if err := c.prepareConfig(cfg); err != nil {
		return err
	}
	if err := c.ensureAuth(ctx, cfg); err != nil {
		if errors.Is(err, errODULogin) {
			return errODULogin
		}
		return sanitizedReadError(ctx, err)
	}
	obj, attempted, err := c.postAttempt(ctx, cfg, "/cgi-bin/cgi/reboot.cgi", map[string]any{"RequestVerifyToken": c.token})
	if err != nil {
		if attempted {
			return errRebootUnconfirmed
		}
		return sanitizedReadError(ctx, err)
	}
	ok, valid := obj["result"].(bool)
	if !valid || !ok {
		return errors.New("ODU did not confirm reboot")
	}
	return nil
}
