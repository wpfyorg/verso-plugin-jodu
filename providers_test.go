package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJODU5164xPasswordTransform(t *testing.T) {
	const want = "NjRiZmI5NDQ3ZmZmZjViMDJhMGRhMTI2YjAwMmZkNjU1NzdjZDE3ODIyMjc2ZWI0MDkwMDkyYzdkOTYxZmY1Yg=="
	if got := jodu5164xPassword("admin"); got != want {
		t.Fatalf("auth transform mismatch: %s", got)
	}
}

func TestJODU5164xSessionAuthAndSignalExpiryRetry(t *testing.T) {
	var logins, primaryCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data/login.json":
			logins.Add(1)
			if err := r.ParseForm(); err != nil || r.Form.Get("LoginName") != "Admin" || r.Form.Get("LoginPWD") != jodu5164xPassword("admin") || r.Form.Get("remember_pwd") != "0" {
				t.Errorf("bad login form: %v", r.Form)
			}
			http.SetCookie(w, &http.Cookie{Name: "QSESSIONID", Value: fmt.Sprint(logins.Load())})
			w.WriteHeader(http.StatusOK)
		case "/data/network_status_cell_parameters.json":
			if _, err := r.Cookie("QSESSIONID"); err != nil {
				t.Errorf("missing session cookie: %v", err)
			}
			if primaryCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"operating_mode":"NA"}`))
				return
			}
			_, _ = w.Write([]byte(`{"signal_strength":"-79 dBm","operating_mode":"5G","band":"n78","bandwidth":"100 MHz","nr_earcn":634080,"physical_cell_id":263,"plmn":"00101","rrc_state":"CONNECTED","dl_bler":"0.1","mimo":"4x4","modulation":"256QAM","cqi":12,"ss_rsrp":"-79 dBm","ss_rsrq":"-9 dB","ss_sinr":"18 dB"}`))
		case "/data/network_status_secondary_cell_parameters.json":
			_, _ = w.Write([]byte(`{"band":"n78","bandwidth":"40 MHz","nr_earcn":634100,"physical_cell_id":264,"ss_rsrp":"-82 dBm","ss_rsrq":"-10 dB","ss_sinr":"16 dB"}`))
		case "/data/network_status_lan.json":
			_, _ = w.Write([]byte(`{"link_status":"Up","speed":"1000 Mbps","duplex_mode":"Full","connection_uptime":"2 days"}`))
		case "/data/network_status_device_data.json":
			_, _ = w.Write([]byte(`{"data_sent":"1 GB","data_received":"9 GB","packet_loss":"0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := newSessionProvider()
	p.base = server.URL
	p.client.Transport = server.Client().Transport
	cfg := oduConfig{Model: model5164x, Host: "192.168.225.1", Username: "Admin", Password: "admin", Enabled: true}
	got, err := p.Read(context.Background(), cfg)
	if err != nil {
		t.Fatalf("read failed after %d logins/%d primary reads: %v", logins.Load(), primaryCalls.Load(), err)
	}
	if logins.Load() != 2 || primaryCalls.Load() != 2 {
		t.Fatalf("session retry counts: logins=%d primary=%d", logins.Load(), primaryCalls.Load())
	}
	for key, want := range map[string]any{"band": "n78", "arfcn": "634080", "pcid": "263", "rsrp": "-79", "plmn": "00101", "band_sec": "n78", "eth_speed": "1000 Mbps"} {
		if got.Net[key] != want {
			t.Errorf("net %s=%v, want %v", key, got.Net[key], want)
		}
	}
	if got.Data["upData"] != "1 GB" || got.Data["downData"] != "9 GB" {
		t.Fatalf("device counters not normalized: %#v", got.Data)
	}
}

func TestJODU52140StatusParserSections(t *testing.T) {
	fixture := `---SERVING---
{"status":"CONNECTED","nr5g":"n78","bandwidth":100,"pci":263,"nr_arfcn":634080,"rsrp":-80,"rsrq":-10,"sinr":18,"bler":2,"modulation":"256QAM","mimo":"4x4","plmn":"00101"}
---SECONDARY---
{"band":"n41","bandwidth":40,"pci":264,"nr_arfcn":640000,"ss_rsrp":-83,"ss_rsrq":-11,"ss_sinr":15}
---NETWORK---
{"rx_bytes":123456,"tx_bytes":654321}
---CPU---
12
---MEM---
40
---ETH---
1000Mb/s|Full|yes
---QTEMP---
cpuss-0-usr, 48
---UPTIME---
1234.5 6789.0
---VERSION---
1.2.1
`
	net, data := parse52140Status([]byte(fixture))
	for key, want := range map[string]any{"operation_mode": "CONNECTED", "band": "n78", "band_width": "100", "pcid": "263", "arfcn": "634080", "rsrp": "-80", "plmn": "00101", "band_sec": "n41", "rsrp_sec": "-83", "eth_speed": "1000Mb/s", "eth_duplex": "Full", "link_status": "yes", "softversion": "1.2.1", "odu_uptime": "20m34.5s", "temperature": "48 °C"} {
		if net[key] != want {
			t.Errorf("net %s=%v, want %v", key, net[key], want)
		}
	}
	if data["downData"] != "123456" || data["upData"] != "654321" {
		t.Fatalf("traffic counters not parsed: %#v", data)
	}
	if missing, _ := parse52140Status([]byte("---SERVING---\n{}\n---UPTIME---\n1\n---VERSION---\n0.9\n")); missing != nil {
		t.Fatal("accepted unsupported helper version")
	}
}

func TestJODU52140MissingHelperVersionRequiresSetup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status.txt" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("---SERVING---\n{}\n"))
	}))
	defer server.Close()
	p := newStatusProvider()
	p.base = server.URL
	_, err := p.Read(context.Background(), oduConfig{Model: model52140, Host: "192.168.225.1", Enabled: true})
	if err != errSetupRequired {
		t.Fatalf("missing helper version returned %v", err)
	}
}

func TestModelSelectionValidationAndRebootCapabilityUI(t *testing.T) {
	for _, model := range []string{model5174x, model5164x, model52140} {
		if !validModel(model) {
			t.Errorf("valid model rejected: %s", model)
		}
	}
	if validModel("auto") || validModel("") {
		t.Fatal("unsupported model accepted")
	}
	for _, test := range []struct {
		model string
		want  bool
	}{{model5174x, true}, {model5164x, false}, {model52140, true}} {
		current := map[string]string{"model": test.model, "enabled": "1"}
		m := &monitor{value: reading{Net: map[string]any{"rsrp": "-70"}}, updated: time.Now(), checked: time.Now()}
		out := m.status(current)
		raw, _ := json.Marshal(out)
		got := strings.Contains(string(raw), "/plugins/jodu/reboot")
		if got != test.want {
			t.Errorf("model %s reboot link=%v, want %v", test.model, got, test.want)
		}
	}
	values := url.Values{"model": {"invalid"}, "host": {"192.168.225.1"}, "username": {"Admin"}}
	if code, _ := request(t, &monitor{}, http.MethodPost, "/settings", values.Encode(), configHeader(t)); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid model accepted with HTTP %d", code)
	}
}

func TestSettingsOffersAndStagesExplicitModel(t *testing.T) {
	code, settingsPage := request(t, &monitor{}, http.MethodGet, "/settings", "", configHeader(t))
	if code != http.StatusOK {
		t.Fatalf("settings GET status %d", code)
	}
	raw, _ := json.Marshal(settingsPage)
	for _, value := range []string{`"kind":"select"`, model5174x, model5164x, model52140} {
		if !strings.Contains(string(raw), value) {
			t.Errorf("model selector missing %q", value)
		}
	}
	values := url.Values{"model": {model5164x}, "host": {"192.168.225.1"}, "username": {"Admin"}, "enabled": {"1"}}
	code, out := request(t, &monitor{}, http.MethodPost, "/settings", values.Encode(), configHeader(t))
	if code != http.StatusOK {
		t.Fatalf("settings POST status %d", code)
	}
	commits, ok := out["commit"].([]any)
	if !ok || len(commits) != 1 {
		t.Fatalf("model change was not staged: %#v", out)
	}
	change := commits[0].(map[string]any)
	vals := change["values"].(map[string]any)
	if change["config"] != "jodu" || vals["model"] != model5164x {
		t.Fatalf("unexpected staged model change: %#v", change)
	}
}

func TestJODU52140RebootUsesAuthenticatedTelnetCommand(t *testing.T) {
	clientSide, deviceSide := net.Pipe()
	p := newStatusProvider()
	p.dialContext = func(context.Context, string, string) (net.Conn, error) { return clientSide, nil }
	gotCommand := make(chan string, 1)
	go func() {
		defer deviceSide.Close()
		reader := bufio.NewReader(deviceSide)
		_, _ = io.WriteString(deviceSide, "login:")
		user, _ := reader.ReadString('\n')
		_, _ = io.WriteString(deviceSide, "Password:")
		password, _ := reader.ReadString('\n')
		_, _ = io.WriteString(deviceSide, "#")
		command, _ := reader.ReadString('\n')
		gotCommand <- strings.TrimSpace(user) + ":" + strings.TrimSpace(password) + ":" + strings.TrimSpace(command)
	}()
	cfg := oduConfig{Model: model52140, Host: "192.168.225.1", Username: "root", Password: "secret"}
	err := p.Reboot(context.Background(), cfg)
	if err != errRebootUnconfirmed {
		t.Fatalf("reboot result = %v, want unconfirmed", err)
	}
	if command := <-gotCommand; command != "root:secret:reboot" {
		t.Fatalf("Telnet command sequence %q", command)
	}
}
