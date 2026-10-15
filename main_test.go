package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func configHeader(t *testing.T) string {
	t.Helper()
	data := map[string]any{"jodu5174x": map[string]any{"main": map[string]any{
		".type": "jodu5174x", "host": "192.168.225.1", "username": "Admin", "enabled": "1", "password": "never-return-this",
	}}}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func request(t *testing.T, m *monitor, method, path, data, header string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(data))
	if data != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if header != "" {
		r.Header.Set("X-Verso-UCI", header)
	}
	w := httptest.NewRecorder()
	m.handler(w, r)
	var out map[string]any
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, out
}

func TestStatusRequiresScopedRead(t *testing.T) {
	m := &monitor{value: reading{Net: map[string]any{"rsrp": "-75", "password": "hidden"}},
		updated: time.Now(), checked: time.Now()}
	code, out := request(t, m, "GET", "/", "", "")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "-75") || strings.Contains(string(encoded), "hidden") {
		t.Fatal("unauthorized status leaked ODU values")
	}
	_, out = request(t, m, "GET", "/", "", configHeader(t))
	encoded, _ = json.Marshal(out)
	if !strings.Contains(string(encoded), "-75") || strings.Contains(string(encoded), "hidden") {
		t.Fatalf("wrong status fields: %s", encoded)
	}
}

func TestSettingsStagesOnlyChangedValues(t *testing.T) {
	m := &monitor{}
	form := url.Values{"host": {"192.168.225.1"}, "username": {"Admin"}, "enabled": {"1"}}
	code, out := request(t, m, "POST", "/settings", form.Encode(), configHeader(t))
	if code != 200 {
		t.Fatal(code)
	}
	if _, present := out["commit"]; present {
		t.Fatal("unchanged settings were staged")
	}

	form.Set("enabled", "0")
	code, out = request(t, m, "POST", "/settings", form.Encode(), configHeader(t))
	if code != 200 {
		t.Fatal(code)
	}
	commits, ok := out["commit"].([]any)
	if !ok || len(commits) != 1 {
		t.Fatalf("commits: %#v", out["commit"])
	}
	change := commits[0].(map[string]any)["values"].(map[string]any)
	if len(change) != 1 || change["enabled"] != "0" {
		t.Fatalf("unexpected change: %#v", change)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "never-return-this") {
		t.Fatal("saved secret was echoed")
	}
}

func TestSettingsRejectsHostInjection(t *testing.T) {
	m := &monitor{}
	form := url.Values{"host": {"example.com/path"}, "username": {"Admin"}, "enabled": {"1"}}
	code, _ := request(t, m, "POST", "/settings", form.Encode(), configHeader(t))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("wanted 422, got %d", code)
	}
	code, _ = request(t, m, "POST", "/settings", form.Encode(), "")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("unauthorized POST: %d", code)
	}
}

func TestPollClearsOldReadingsOnFailure(t *testing.T) {
	m := &monitor{value: reading{Net: map[string]any{"rsrp": "-60"}},
		updated: time.Now(), checked: time.Now(), command: "/nonexistent-jodu-test-command"}
	m.poll()
	reading, _, reason := m.current()
	if reason == "" || len(reading.Net) != 0 {
		t.Fatalf("stale data retained: %#v (%s)", reading, reason)
	}
}
