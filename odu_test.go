package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testODU(t *testing.T, handler http.Handler) (*oduClient, *httptest.Server, oduConfig) {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	c := newODUClient()
	c.baseURL = s.URL
	c.http.Transport = s.Client().Transport
	return c, s, oduConfig{Host: "192.168.225.1", Username: "Admin", Password: "secret-pass", Enabled: true}
}

func decodeRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func jsonResponse(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, value)
}

func TestODUReadAuthCookiesAndTokenRotation(t *testing.T) {
	var step int
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "cookie-1"})
			jsonResponse(w, `{"RequestVerifyToken":"token-1"}`)
		case "/cgi-bin/cgi/login_req.cgi":
			body := decodeRequest(t, r)
			if body["RequestVerifyToken"] != "token-1" || body["usr"] != "Admin" || body["pwd"] != "secret-pass" {
				t.Errorf("bad login request: %#v", body)
			}
			if cookie, err := r.Cookie("sid"); err != nil || cookie.Value != "cookie-1" {
				t.Errorf("login cookie missing: %v", err)
			}
			jsonResponse(w, `{"result":true,"RequestVerifyToken":"token-2"}`)
		case "/cgi-bin/cgi/net_status_retrieve.cgi":
			body := decodeRequest(t, r)
			if body["RequestVerifyToken"] != "token-2" {
				t.Errorf("stale net token: %#v", body)
			}
			jsonResponse(w, `{"rsrp":-81,"RequestVerifyToken":"token-3","nested":{"token":"secret"}}`)
		case "/cgi-bin/cgi/data_status.cgi":
			body := decodeRequest(t, r)
			if body["RequestVerifyToken"] != "token-3" || body["operation"] != "get" {
				t.Errorf("stale data token or operation: %#v", body)
			}
			jsonResponse(w, `{"downData":123,"RequestVerifyToken":"token-4"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	got, err := c.Read(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if step != 4 || got.Net["rsrp"] != float64(-81) || got.Data["downData"] != float64(123) {
		t.Fatalf("wrong reading or requests: %#v steps=%d", got, step)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "cookie-1") {
		t.Fatalf("auth material leaked: %s", encoded)
	}
}

func TestODUReadRetriesWholeReadOnceAfterExpiredAuth(t *testing.T) {
	var netCalls, tokenCalls int
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			tokenCalls++
			jsonResponse(w, fmt.Sprintf(`{"RequestVerifyToken":"t%d"}`, tokenCalls))
		case "/cgi-bin/cgi/login_req.cgi":
			jsonResponse(w, fmt.Sprintf(`{"result":true,"RequestVerifyToken":"l%d"}`, tokenCalls))
		case "/cgi-bin/cgi/net_status_retrieve.cgi":
			netCalls++
			if netCalls == 1 {
				jsonResponse(w, `{"error":"expired"}`)
				return
			}
			jsonResponse(w, `{"rsrp":"-77","RequestVerifyToken":"n2"}`)
		case "/cgi-bin/cgi/data_status.cgi":
			jsonResponse(w, `{"downData":"5"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	got, err := c.Read(context.Background(), cfg)
	if err != nil || tokenCalls != 2 || netCalls != 2 || got.Net["rsrp"] != "-77" {
		t.Fatalf("read=%#v err=%v token=%d net=%d", got, err, tokenCalls, netCalls)
	}
}

func TestODUReadRetriesHTMLAndUnauthorizedResponses(t *testing.T) {
	for _, expiredResponse := range []string{"html", "401", "403"} {
		t.Run(expiredResponse, func(t *testing.T) {
			var netCalls, tokenCalls atomic.Int32
			c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cgi-bin/cgi/token_query.cgi":
					jsonResponse(w, fmt.Sprintf(`{"RequestVerifyToken":"t%d"}`, tokenCalls.Add(1)))
				case "/cgi-bin/cgi/login_req.cgi":
					jsonResponse(w, `{"result":true}`)
				case "/cgi-bin/cgi/net_status_retrieve.cgi":
					if netCalls.Add(1) == 1 {
						switch expiredResponse {
						case "html":
							w.Header().Set("Content-Type", "text/html")
							_, _ = io.WriteString(w, "<html>login</html>")
						case "401":
							w.WriteHeader(http.StatusUnauthorized)
						case "403":
							w.WriteHeader(http.StatusForbidden)
						}
						return
					}
					jsonResponse(w, `{"rsrp":"-78"}`)
				case "/cgi-bin/cgi/data_status.cgi":
					jsonResponse(w, `{"downData":"10"}`)
				}
			}))
			defer s.Close()
			got, err := c.Read(context.Background(), cfg)
			if err != nil || tokenCalls.Load() != 2 || netCalls.Load() != 2 || got.Net["rsrp"] != "-78" {
				t.Fatalf("read=%#v err=%v token=%d net=%d", got, err, tokenCalls.Load(), netCalls.Load())
			}
		})
	}
}

func TestODUDisabledOrMissingPasswordMakesNoRequests(t *testing.T) {
	var calls atomic.Int32
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	for _, tc := range []struct {
		cfg  oduConfig
		want error
	}{{func() oduConfig { x := cfg; x.Enabled = false; return x }(), errMonitoringDisabled},
		{func() oduConfig { x := cfg; x.Password = ""; return x }(), errPasswordMissing}} {
		if _, err := c.Read(context.Background(), tc.cfg); !errors.Is(err, tc.want) {
			t.Fatalf("got %v, want %v", err, tc.want)
		}
		if tc.want == errPasswordMissing {
			if err := c.Reboot(context.Background(), tc.cfg); !errors.Is(err, tc.want) {
				t.Fatalf("reboot got %v, want %v", err, tc.want)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("made %d requests", calls.Load())
	}
}

func TestODULoginFailureIsStableAndSanitized(t *testing.T) {
	for _, reply := range []string{`{"result":false,"error":"private-password-detail"}`, `{}`, `{"result":"true"}`} {
		t.Run(reply, func(t *testing.T) {
			c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cgi-bin/cgi/token_query.cgi" {
					jsonResponse(w, `{"RequestVerifyToken":"private-token"}`)
					return
				}
				jsonResponse(w, reply)
			}))
			defer s.Close()
			_, err := c.Read(context.Background(), cfg)
			if !errors.Is(err, errODULogin) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), cfg.Password) {
				t.Fatalf("unsanitized login error: %v", err)
			}
		})
	}
}

func TestODUConfigChangeResetsAuthentication(t *testing.T) {
	var tokens atomic.Int32
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			jsonResponse(w, fmt.Sprintf(`{"RequestVerifyToken":"t%d"}`, tokens.Add(1)))
		case "/cgi-bin/cgi/login_req.cgi":
			jsonResponse(w, `{"result":true}`)
		case "/cgi-bin/cgi/net_status_retrieve.cgi":
			jsonResponse(w, `{"rsrp":"-70"}`)
		case "/cgi-bin/cgi/data_status.cgi":
			jsonResponse(w, `{}`)
		}
	}))
	defer s.Close()
	if _, err := c.Read(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Password = "changed"
	if _, err := c.Read(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatalf("config change reused old authentication; token queries=%d", tokens.Load())
	}
}

func TestODURejectsRedirectWithoutFollowing(t *testing.T) {
	var calls atomic.Int32
	var redirected atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer foreign.Close()
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, foreign.URL+"/steal", http.StatusFound)
	}))
	defer s.Close()
	_, err := c.Read(context.Background(), cfg)
	if err == nil || calls.Load() != 2 || redirected.Load() != 0 {
		t.Fatalf("redirect not blocked: err=%v source=%d target=%d", err, calls.Load(), redirected.Load())
	}
}

func TestODUStrictObjectAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`[]`, `null`, `{"rsrp":1} {"extra":2}`} {
		if _, err := decodeODUObject([]byte(body)); err == nil {
			t.Errorf("accepted invalid response %q", body)
		}
	}
}

func TestODURejectsOversizedResponse(t *testing.T) {
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"RequestVerifyToken":"`+strings.Repeat("x", oduResponseLimit)+`"}`)
	}))
	defer s.Close()
	_, err := c.Read(context.Background(), cfg)
	if !errors.Is(err, errODUUnavailable) {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestODURejectsURLAsHostBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	cfg.Host = "https://example.invalid/path"
	if _, err := c.Read(context.Background(), cfg); err == nil {
		t.Fatal("accepted URL as ODU host")
	}
	if calls.Load() != 0 {
		t.Fatalf("made %d requests", calls.Load())
	}
}

func TestODUDataAuthFailureIsNotHealthyEmptyReading(t *testing.T) {
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			jsonResponse(w, `{"RequestVerifyToken":"t1"}`)
		case "/cgi-bin/cgi/login_req.cgi":
			jsonResponse(w, `{"result":true}`)
		case "/cgi-bin/cgi/net_status_retrieve.cgi":
			jsonResponse(w, `{"rsrp":-70}`)
		case "/cgi-bin/cgi/data_status.cgi":
			jsonResponse(w, `{"result":false}`)
		}
	}))
	defer s.Close()
	got, err := c.Read(context.Background(), cfg)
	if err == nil || !errors.Is(err, errODUUnavailable) || len(got.Net) != 0 || got.Data != nil {
		t.Fatalf("auth error reported as healthy data: %#v %v", got, err)
	}
}

func TestODURebootConnectionCutIsUnconfirmedAndNotRepeated(t *testing.T) {
	var reboots atomic.Int32
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			jsonResponse(w, `{"RequestVerifyToken":"t1"}`)
		case "/cgi-bin/cgi/login_req.cgi":
			jsonResponse(w, `{"result":true}`)
		case "/cgi-bin/cgi/reboot.cgi":
			reboots.Add(1)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server cannot hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer s.Close()
	err := c.Reboot(context.Background(), cfg)
	if !errors.Is(err, errRebootUnconfirmed) || reboots.Load() != 1 {
		t.Fatalf("reboot result=%v calls=%d", err, reboots.Load())
	}
}

func TestODUQueuedOperationHonorsContextAndDoesNotAttemptReboot(t *testing.T) {
	started := make(chan struct{})
	resume := make(chan struct{})
	var releaseOnce sync.Once
	releaseRead := func() { releaseOnce.Do(func() { close(resume) }) }
	var rebootCalls atomic.Int32
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			jsonResponse(w, `{"RequestVerifyToken":"t1"}`)
		case "/cgi-bin/cgi/login_req.cgi":
			jsonResponse(w, `{"result":true}`)
		case "/cgi-bin/cgi/net_status_retrieve.cgi":
			close(started)
			<-resume
			jsonResponse(w, `{"rsrp":"-70"}`)
		case "/cgi-bin/cgi/data_status.cgi":
			jsonResponse(w, `{}`)
		case "/cgi-bin/cgi/reboot.cgi":
			rebootCalls.Add(1)
			jsonResponse(w, `{"result":true}`)
		}
	}))
	defer func() {
		releaseRead()
		s.Close()
	}()
	readDone := make(chan error, 1)
	go func() {
		_, err := c.Read(context.Background(), cfg)
		readDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not reach in-flight request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := c.Reboot(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errRebootUnconfirmed) {
		t.Fatalf("queued reboot returned %v", err)
	}
	if rebootCalls.Load() != 0 {
		t.Fatalf("queued reboot reached ODU %d times", rebootCalls.Load())
	}
	releaseRead()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
}

func TestODURebootAuthCancellationIsNotUnconfirmed(t *testing.T) {
	started := make(chan struct{})
	resume := make(chan struct{})
	var releaseOnce sync.Once
	releaseAuth := func() { releaseOnce.Do(func() { close(resume) }) }
	var rebootCalls atomic.Int32
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			close(started)
			<-resume
			jsonResponse(w, `{"RequestVerifyToken":"t1"}`)
		case "/cgi-bin/cgi/reboot.cgi":
			rebootCalls.Add(1)
			jsonResponse(w, `{"result":true}`)
		}
	}))
	defer func() {
		releaseAuth()
		s.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Reboot(ctx, cfg) }()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("authentication request did not start")
	}
	err := <-done
	if !errors.Is(err, context.Canceled) || errors.Is(err, errRebootUnconfirmed) {
		t.Fatalf("pre-reboot auth cancellation returned %v", err)
	}
	if rebootCalls.Load() != 0 {
		t.Fatalf("reboot request was sent %d times", rebootCalls.Load())
	}
}

func TestODURebootNeedsExplicitSuccess(t *testing.T) {
	c, s, cfg := testODU(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			jsonResponse(w, `{"RequestVerifyToken":"t1"}`)
		case "/cgi-bin/cgi/login_req.cgi":
			jsonResponse(w, `{"result":true}`)
		case "/cgi-bin/cgi/reboot.cgi":
			jsonResponse(w, `{"result":true}`)
		}
	}))
	defer s.Close()
	cfg.Enabled = false
	if err := c.Reboot(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}
