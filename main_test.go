package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func configHeader(t *testing.T) string {
	t.Helper()
	data := map[string]any{"jodu": map[string]any{"main": map[string]any{
		".type": "jodu", "host": "192.168.225.1", "username": "Admin", "enabled": "1", "password": "never-return-this",
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
	m := &monitor{value: reading{Net: map[string]any{"rsrp": "-75", "password": "hidden-password-value"}},
		updated: time.Now(), checked: time.Now()}
	code, out := request(t, m, "GET", "/", "", "")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "-75") || strings.Contains(string(encoded), "hidden-password-value") {
		t.Fatal("unauthorized status leaked ODU values")
	}
	_, out = request(t, m, "GET", "/", "", configHeader(t))
	encoded, _ = json.Marshal(out)
	if !strings.Contains(string(encoded), "-75") || strings.Contains(string(encoded), "hidden-password-value") {
		t.Fatalf("wrong status fields: %s", encoded)
	}
}

func TestSettingsStagesOnlyChangedValues(t *testing.T) {
	m := &monitor{}
	form := url.Values{"model": {model5174x}, "host": {"192.168.225.1"}, "username": {"Admin"}, "enabled": {"1"}}
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
	form := url.Values{"model": {model5174x}, "host": {"example.com/path"}, "username": {"Admin"}, "enabled": {"1"}}
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
		updated: time.Now(), checked: time.Now(), fetch: func(context.Context) (reading, error) { return reading{}, errODUUnavailable }}
	m.poll()
	reading, _, reason := m.current()
	if reason == "" || len(reading.Net) != 0 {
		t.Fatalf("stale data retained: %#v (%s)", reading, reason)
	}
}

func TestOverviewRequiresScopedRead(t *testing.T) {
	m := &monitor{value: reading{Net: map[string]any{"rsrp": "-75", "password": "hidden"}},
		updated: time.Now(), checked: time.Now()}
	for _, header := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte(`{"jodu":{}}`))} {
		code, out := request(t, m, "GET", "/overview", "", header)
		encoded, _ := json.Marshal(out)
		if code != http.StatusOK || !strings.Contains(string(encoded), "Status unavailable") ||
			strings.Contains(string(encoded), "-75") || strings.Contains(string(encoded), "hidden") {
			t.Fatalf("unauthorized overview: status=%d body=%s", code, encoded)
		}
	}
}

func TestOverviewShowsOnlySummaryReadings(t *testing.T) {
	m := &monitor{value: reading{Net: map[string]any{
		"sim_card_state": "Connected", "operation_mode": "5G", "band": "n78",
		"rsrp": "-75", "sinr": "20", "password": "hidden", "pcid": "private-detail",
	}, Data: map[string]any{"downData": "traffic-detail"}, Link: " 1000 Mbps "},
		updated: time.Now(), checked: time.Now()}
	code, out := request(t, m, "GET", "/overview", "", configHeader(t))
	encoded, _ := json.Marshal(out)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	for _, want := range []string{"Connected", "5G", "n78", "-75", "20", "1000 Mbps", "Checked"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %q: %s", want, encoded)
		}
	}
	for _, forbidden := range []string{"hidden", "never-return-this", "private-detail", "traffic-detail", `"commit"`, `"form"`, `"link"`, `"act"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("unexpected %q in summary: %s", forbidden, encoded)
		}
	}
	if out["widget"].(map[string]any)["type"] != "properties" {
		t.Fatalf("summary must use properties: %s", encoded)
	}
}

func TestOverviewHidesStaleAndFailedReadings(t *testing.T) {
	for _, state := range []struct {
		name string
		m    *monitor
		want string
	}{
		{"initial", &monitor{}, "Checking ODU"},
		{"failed", &monitor{checked: time.Now(), err: "Monitoring is disabled."}, "Monitoring is disabled."},
		{"stale", &monitor{checked: time.Now(), updated: time.Now().Add(-61 * time.Second), value: reading{Net: map[string]any{"rsrp": "-75"}}}, "ODU reading is stale."},
	} {
		t.Run(state.name, func(t *testing.T) {
			_, out := request(t, state.m, "GET", "/overview", "", configHeader(t))
			encoded, _ := json.Marshal(out)
			if !strings.Contains(string(encoded), state.want) || strings.Contains(string(encoded), "-75") {
				t.Fatalf("wrong unavailable state: %s", encoded)
			}
		})
	}
}

func TestOverviewRejectsWrites(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		code, _ := request(t, &monitor{}, method, "/overview", "", configHeader(t))
		if code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: got %d", method, code)
		}
	}
}

func TestManifestRegistersOverview(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Overview struct{ Path, Icon string }
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Overview.Path != "/overview" || manifest.Overview.Icon != "radio" {
		t.Fatalf("wrong overview registration: %#v", manifest.Overview)
	}
}
