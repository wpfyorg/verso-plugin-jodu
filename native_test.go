package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeConfigLoadsWithoutRenderingCredentials(t *testing.T) {
	command := filepath.Join(t.TempDir(), "uci")
	script := `#!/bin/sh
for last do :; done
case "$last" in
  jodu5174x.main) printf 'jodu5174x\n';;
  jodu5174x.main.host) printf '192.168.225.1\n';;
  jodu5174x.main.username) printf 'Admin\n';;
  jodu5174x.main.password) printf 'test-secret\n';;
  jodu5174x.main.enabled) printf '1\n';;
  *) exit 1;;
esac
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadODUConfig(context.Background(), command, t.TempDir(), t.TempDir())
	if err != nil || cfg.Host != "192.168.225.1" || cfg.Username != "Admin" || cfg.Password != "test-secret" || !cfg.Enabled {
		t.Fatal("native config not loaded correctly")
	}
	_, out := request(t, &monitor{}, "GET", "/settings", "", configHeader(t))
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "test-secret") || strings.Contains(string(raw), "never-return-this") {
		t.Fatal("saved credential appeared in settings")
	}
	if _, err := loadODUConfig(context.Background(), "/nonexistent-uci", t.TempDir(), t.TempDir()); err != errConfigUnavailable {
		t.Fatal("config failure must be sanitized")
	}
}

func TestEthernetRouteUsesObservedInterface(t *testing.T) {
	sysfs := t.TempDir()
	iface := filepath.Join(sysfs, "wan")
	if err := os.Mkdir(iface, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"carrier": "1\n", "speed": "1000\n", "duplex": "full\n"} {
		if err := os.WriteFile(filepath.Join(iface, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := ethernetRouteLink([]byte(`[{"dev":"wan"}]`), sysfs); got != "1000 Mbps (full duplex)" {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(iface, "carrier"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ethernetRouteLink([]byte(`[{"dev":"wan"}]`), sysfs); got != "Down" {
		t.Fatal(got)
	}
	for _, route := range []string{`[]`, `{}`, `[{"dev":"../../etc"}]`, `[{"dev":".."}]`, `invalid`} {
		if got := ethernetRouteLink([]byte(route), sysfs); got != "Unknown" {
			t.Fatalf("invalid route: %q", got)
		}
	}
}

func TestMonitoringToggleOnlyStagesEnabled(t *testing.T) {
	m := &monitor{}
	code, out := request(t, m, "POST", "/monitor", url.Values{"enabled": {"0"}}.Encode(), configHeader(t))
	if code != http.StatusOK {
		t.Fatal(code)
	}
	commits := out["commit"].([]any)
	values := commits[0].(map[string]any)["values"].(map[string]any)
	if len(values) != 1 || values["enabled"] != "0" {
		t.Fatal(values)
	}
	code, _ = request(t, m, "POST", "/monitor", "enabled=0", "")
	if code != http.StatusForbidden {
		t.Fatal(code)
	}
	code, _ = request(t, m, "POST", "/monitor", "enabled=unexpected", configHeader(t))
	if code != http.StatusBadRequest {
		t.Fatal(code)
	}
	code, out = request(t, m, "POST", "/", "_action=monitor", configHeader(t))
	if code != http.StatusOK || out["commit"] == nil {
		t.Fatal("same-page monitor form did not stage")
	}
}

func TestEmptyUCIPasswordIsValidDisabledConfiguration(t *testing.T) {
	command := filepath.Join(t.TempDir(), "uci")
	script := `#!/bin/sh
for last do :; done
case "$last" in
jodu5174x.main) echo jodu5174x;;
jodu5174x.main.host) echo 192.168.225.1;;
jodu5174x.main.username) echo Admin;;
jodu5174x.main.enabled) echo 0;;
jodu5174x.main.password) exit 1;;
*) exit 1;;
esac
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadODUConfig(context.Background(), command, t.TempDir(), t.TempDir())
	if err != nil || cfg.Enabled || cfg.Password != "" {
		t.Fatal("empty UCI option rejected disabled config")
	}
}

func TestRebootRequiresConfirmedSingleUseRequest(t *testing.T) {
	var calls atomic.Int32
	m := &monitor{reboot: func(context.Context, string) error { calls.Add(1); return nil }, value: reading{Net: map[string]any{"rsrp": "-75"}}, checked: time.Now(), updated: time.Now()}
	code, _ := request(t, m, "GET", "/reboot", "", configHeader(t))
	if code != http.StatusOK || calls.Load() != 0 {
		t.Fatal("GET must never reboot")
	}
	code, _ = request(t, m, "POST", "/reboot", "confirm=1", configHeader(t))
	if code != http.StatusUnprocessableEntity || calls.Load() != 0 {
		t.Fatal("missing confirmation token was accepted")
	}
	code, _ = request(t, m, "POST", "/reboot", "confirm=1", "")
	if code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("missing scoped read was accepted")
	}
	form := url.Values{"confirm": {"1"}, "token": {m.rebootToken("192.168.225.1")}}.Encode()
	code, _ = request(t, m, "POST", "/reboot", form, configHeader(t))
	waitForReboot(t, m)
	_, out := request(t, m, "GET", "/reboot", "", configHeader(t))
	if code != http.StatusOK || calls.Load() != 1 || out["notice"].(map[string]any)["level"] != "success" {
		t.Fatal("confirmed reboot not called once")
	}
	v, _, reason := m.current()
	if len(v.Net) != 0 || !strings.Contains(reason, "reboot") {
		t.Fatal("reboot kept old telemetry")
	}
	code, _ = request(t, m, "POST", "/reboot", form, configHeader(t))
	if code != http.StatusUnprocessableEntity || calls.Load() != 1 {
		t.Fatal("duplicate reboot request was replayed")
	}
}

func TestRebootUnconfirmedDoesNotClaimSuccess(t *testing.T) {
	m := &monitor{reboot: func(context.Context, string) error { return errRebootUnconfirmed }}
	form := url.Values{"confirm": {"1"}, "token": {m.rebootToken("192.168.225.1")}}.Encode()
	_, _ = request(t, m, "POST", "/reboot", form, configHeader(t))
	waitForReboot(t, m)
	_, out := request(t, m, "GET", "/reboot", "", configHeader(t))
	if out["notice"].(map[string]any)["level"] != "warning" {
		t.Fatal("ambiguous reboot reported as success")
	}
}

func waitForReboot(t *testing.T, m *monitor) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		active := m.rebooting
		m.mu.RUnlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background reboot did not complete")
}

func TestRebootReturnsBeforeHardwareOperationCompletes(t *testing.T) {
	finish := make(chan struct{})
	var calls atomic.Int32
	m := &monitor{reboot: func(context.Context, string) error { calls.Add(1); <-finish; return nil }, fetch: func(context.Context) (reading, error) {
		t.Error("poll must pause during reboot")
		return reading{}, nil
	}}
	form := url.Values{"confirm": {"1"}, "token": {m.rebootToken("192.168.225.1")}}.Encode()
	start := time.Now()
	code, out := request(t, m, "POST", "/reboot", form, configHeader(t))
	if code != http.StatusOK || time.Since(start) > time.Second {
		close(finish)
		t.Fatal("reboot blocked the shell gateway")
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "pending") {
		close(finish)
		t.Fatal("pending outcome missing")
	}
	m.poll()
	if m.startReboot("192.168.225.1") {
		close(finish)
		t.Fatal("queued duplicate reboot")
	}
	close(finish)
	waitForReboot(t, m)
	if calls.Load() != 1 {
		t.Fatal("reboot was not issued exactly once")
	}
}

func TestDisabledMonitorKeepsEthernetAndHidesOldSignals(t *testing.T) {
	m := &monitor{updated: time.Now().Add(-2 * time.Minute), checked: time.Now(), value: reading{Net: map[string]any{"rsrp": "-75"}}, fetch: func(context.Context) (reading, error) {
		return reading{Link: "1000 Mbps (full duplex)"}, errMonitoringDisabled
	}}
	m.poll()
	v, _, reason := m.current()
	if reason != "Monitoring is disabled." || len(v.Net) != 0 || v.Link == "" {
		t.Fatal("disabled state lost link health or retained stale signals")
	}
	_, out := request(t, m, "GET", "/overview", "", configHeader(t))
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "-75") || !strings.Contains(string(raw), "1000 Mbps") {
		t.Fatal("wrong disabled overview")
	}
}

func TestNativeODUReadFlowsIntoVersoStatus(t *testing.T) {
	var requests atomic.Int32
	odu := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/cgi-bin/cgi/token_query.cgi":
			_, _ = w.Write([]byte(`{"RequestVerifyToken":"fixture-token"}`))
		case "/cgi-bin/cgi/login_req.cgi":
			_, _ = w.Write([]byte(`{"result":true}`))
		case "/cgi-bin/cgi/net_status_retrieve.cgi":
			_, _ = w.Write([]byte(`{"sim_card_state":"Attached","operation_mode":"5G","band":"n78","rsrp":-86,"sinr":17,"softversion":"fixture-firmware","RequestVerifyToken":"private-token"}`))
		case "/cgi-bin/cgi/data_status.cgi":
			_, _ = w.Write([]byte(`{"downData":"10 GB","upData":"2 GB"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer odu.Close()
	client := newODUClient()
	client.baseURL = odu.URL
	client.http.Transport = odu.Client().Transport
	cfg := oduConfig{Host: "192.168.225.1", Username: "Admin", Password: "fixture-password", Enabled: true}
	m := &monitor{fetch: func(ctx context.Context) (reading, error) {
		value, err := client.Read(ctx, cfg)
		value.Link = "1000 Mbps (full duplex)"
		return value, err
	}}
	m.poll()
	if requests.Load() != 4 {
		t.Fatal("native client did not complete token/login/status/traffic reads")
	}
	_, out := request(t, m, "GET", "/", "", configHeader(t))
	raw, _ := json.Marshal(out)
	for _, want := range []string{"Attached", "n78", "-86", "10 GB", "fixture-firmware", "1000 Mbps"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("native status missing %q", want)
		}
	}
	for _, secret := range []string{"fixture-token", "private-token", "fixture-password", "never-return-this"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("authentication material reached Verso output")
		}
	}
	_, out = request(t, m, "GET", "/overview", "", configHeader(t))
	raw, _ = json.Marshal(out)
	if !strings.Contains(string(raw), "-86") {
		t.Fatal("native summary missing signal")
	}
	_, out = request(t, m, "GET", "/overview", "", "")
	raw, _ = json.Marshal(out)
	if strings.Contains(string(raw), "-86") {
		t.Fatal("native telemetry escaped scoped read gate")
	}
}

func TestInFlightPollCannotRestorePreRebootTelemetry(t *testing.T) {
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m := &monitor{fetch: func(context.Context) (reading, error) {
		close(started)
		<-finish
		return reading{Net: map[string]any{"rsrp": "-75"}}, nil
	}, reboot: func(context.Context, string) error { return nil }}
	go func() { m.poll(); close(done) }()
	<-started
	if !m.startReboot("192.168.225.1") {
		close(finish)
		t.Fatal("reboot job did not start")
	}
	waitForReboot(t, m)
	close(finish)
	<-done
	value, _, reason := m.current()
	if len(value.Net) != 0 || !strings.Contains(reason, "reboot") {
		t.Fatal("old in-flight poll restored pre-reboot metrics")
	}
}
