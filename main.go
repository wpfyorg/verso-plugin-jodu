// A native, local-only Verso view for the separately installed JODU5174x package.
// No code or assets from that package are included here.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dataCommand = "/usr/libexec/jodu5174x-data.sh"
const defaultSocket = "/var/run/verso-plugin-jodu5174x/jodu5174x.sock"

type reading struct {
	Net   map[string]any `json:"netstatus"`
	Data  map[string]any `json:"datastatus"`
	Link  string         `json:"eth_link"`
	Error string         `json:"error"`
}

type monitor struct {
	mu      sync.RWMutex
	value   reading
	updated time.Time
	checked time.Time
	err     string
	command string
}

func (m *monitor) poll() {
	ctx, cancel := context.WithTimeout(context.Background(), 38*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, m.command).Output()
	var result reading
	if err == nil {
		if len(output) > 65536 {
			err = errors.New("oversized response")
		} else {
			err = json.Unmarshal(output, &result)
		}
	}
	reason := ""
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			reason = "Install the JODU LuCI package to enable monitoring."
		} else {
			reason = "Unable to read ODU status."
		}
	} else if result.Error != "" {
		switch result.Error {
		case "disabled":
			reason = "Monitoring is disabled."
		case "password not configured":
			reason = "Set the ODU password in Settings."
		case "login failed":
			reason = "ODU sign-in failed. Check Settings."
		case "curl not found":
			reason = "Install curl on the router."
		default:
			reason = "Unable to reach the ODU. Check its address and connection."
		}
	} else if len(result.Net) == 0 {
		reason = "No ODU status received."
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checked = time.Now()
	m.err = reason
	if reason == "" {
		m.value = result
		m.updated = m.checked
	} else {
		m.value = reading{} // Never pass old signal levels off as current data.
	}
}

func (m *monitor) current() (reading, time.Time, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.checked.IsZero() {
		return reading{}, time.Time{}, "Checking ODU…"
	}
	if !m.updated.IsZero() && time.Since(m.updated) > 60*time.Second {
		return reading{}, time.Time{}, "ODU reading is stale."
	}
	return m.value, m.updated, m.err
}

func prop(label, value string) map[string]any { return map[string]any{"label": label, "value": value} }
func box(title string, items []any) any {
	return map[string]any{"type": "card", "title": title, "children": []any{map[string]any{"type": "properties", "items": items}}}
}
func section(v map[string]any, pairs ...string) []any {
	result := make([]any, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		// Only the named scalar fields become visible; discard all other ODU fields.
		if raw, ok := v[pairs[i+1]]; ok {
			value := ""
			switch x := raw.(type) {
			case string:
				value = x
			case json.Number:
				value = string(x)
			case float64:
				value = strconv.FormatFloat(x, 'f', -1, 64)
			}
			value = strings.TrimSpace(value)
			if value != "" && value != "NA" {
				if len(value) > 96 {
					value = value[:96]
				}
				result = append(result, prop(pairs[i], value))
			}
		}
	}
	return result
}
func metrics(v map[string]any, secondary bool) []any {
	suffix := ""
	if secondary {
		suffix = "_sec"
	}
	x := section(v, "Band", "band"+suffix, "Bandwidth (MHz)", "band_width"+suffix,
		"ARFCN", "arfcn"+suffix, "Cell ID", "pcid"+suffix,
		"Modulation", "modulation"+suffix, "MIMO", "mimo"+suffix,
		"RSRP (dBm)", "rsrp"+suffix, "RSRQ (dB)", "rsrq"+suffix)
	if !secondary {
		x = append(x, section(v, "SINR (dB)", "sinr")...)
	}
	bler := "bler_dl" + suffix
	if raw, ok := v[bler]; ok {
		if f, err := strconv.ParseFloat(fmt.Sprint(raw), 64); err == nil && f >= 0 && f <= 1 {
			x = append(x, prop("Downlink BLER", fmt.Sprintf("%.2f%%", f*100)))
		}
	}
	return x
}
func envelope(title string, widget any) map[string]any {
	return map[string]any{"schema_version": 1, "title": title, "width": "normal", "widget": widget}
}
func warn(title, body string) any {
	return map[string]any{"type": "callout", "variant": "warning", "title": title, "body": body}
}
func (m *monitor) status() map[string]any {
	v, updated, reason := m.current()
	children := []any{}
	if reason != "" {
		children = append(children, warn("ODU status", reason))
	}
	if reason == "" {
		summary := section(v.Net, "Connection", "sim_card_state", "Radio", "operation_mode",
			"RRC", "rrc_state", "Connected for", "connection_time", "Ethernet", "__unused")
		if v.Link != "" {
			summary = append(summary, prop("Ethernet", strings.TrimSpace(v.Link)))
		}
		if !updated.IsZero() {
			summary = append(summary, prop("Checked", updated.Format("15:04:05")))
		}
		children = append(children, box("Connection", summary))
		primary := metrics(v.Net, false)
		if len(primary) > 0 {
			children = append(children, box("Primary 5G carrier", primary))
		}
		secondary := metrics(v.Net, true)
		if len(secondary) > 0 {
			children = append(children, box("Secondary carrier", secondary))
		}
		usage := section(v.Data, "Downloaded", "downData", "Uploaded", "upData")
		if len(usage) > 0 {
			children = append(children, box("ODU traffic counters", usage))
		}
	}
	children = append(children, map[string]any{"type": "link", "label": "Settings", "href": "/plugins/jodu5174x/settings", "icon": "settings", "style": "button"})
	out := envelope("5G ODU", map[string]any{"type": "stack", "children": children})
	out["act"] = map[string]any{"label": "Refresh", "href": "/plugins/jodu5174x/", "icon": "refresh-cw"}
	return out
}

func snapshot(r *http.Request) (map[string]string, bool) {
	payload, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Verso-UCI"))
	if err != nil || len(payload) == 0 || len(payload) > 65536 {
		return nil, false
	}
	var configs map[string]map[string]map[string]any
	if json.Unmarshal(payload, &configs) != nil {
		return nil, false
	}
	main, ok := configs["jodu5174x"]["main"]
	if !ok || main[".type"] != "jodu5174x" {
		return nil, false
	}
	fields := map[string]string{}
	for _, key := range []string{"host", "username", "enabled"} {
		fields[key], _ = main[key].(string)
	}
	return fields, true
}
func fields(current map[string]string) []any {
	return []any{
		map[string]any{"type": "field", "kind": "text", "name": "host", "label": "ODU address", "value": current["host"], "placeholder": "192.168.225.1"},
		map[string]any{"type": "field", "kind": "text", "name": "username", "label": "ODU username", "value": current["username"]},
		map[string]any{"type": "field", "kind": "password", "name": "password", "label": "New password", "value": "", "help": "Leave empty to retain the saved password."},
		map[string]any{"type": "switch", "name": "enabled", "label": "Monitor the ODU", "on": current["enabled"] != "0"},
	}
}
func settings(current map[string]string, ok bool, reason string) map[string]any {
	var children []any
	if !ok {
		children = append(children, warn("Settings unavailable", "Verso could not read jodu5174x configuration. Install the LuCI package and sign in with an account allowed to read it."))
	} else {
		children = append(children, map[string]any{"type": "form", "style": "page", "submit": "Stage settings", "fields": fields(current)})
	}
	children = append(children, map[string]any{"type": "link", "label": "Back to ODU", "href": "/plugins/jodu5174x/"})
	out := envelope("5G ODU settings", map[string]any{"type": "stack", "children": children})
	if reason != "" {
		out["notice"] = map[string]any{"level": "warning", "text": reason}
	}
	return out
}

var hostRegex = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func validHost(value string) bool {
	if net.ParseIP(value) != nil {
		return true
	}
	if !hostRegex.MatchString(value) || strings.Contains(value, "..") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}
func (m *monitor) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var output map[string]any
	switch r.URL.Path {
	case "/", "":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Verso can render plugin GETs without authentication. Only expose
		// ODU telemetry after its scoped UCI read has succeeded.
		if _, allowed := snapshot(r); !allowed {
			output = envelope("5G ODU", warn("Status unavailable", "Sign in to Verso with permission to read JODU settings."))
			break
		}
		output = m.status()
	case "/settings":
		values, ok := snapshot(r)
		if r.Method == http.MethodGet {
			output = settings(values, ok, "")
			break
		}
		if !ok {
			http.Error(w, "Configuration read required", http.StatusUnprocessableEntity)
			return
		}
		if r.ParseForm() != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		host := strings.TrimSpace(r.PostForm.Get("host"))
		usr := strings.TrimSpace(r.PostForm.Get("username"))
		pass := r.PostForm.Get("password")
		enable := "0"
		if r.PostForm.Get("enabled") == "1" {
			enable = "1"
		}
		if !validHost(host) || usr == "" || len(usr) > 64 || len(pass) > 256 || strings.ContainsAny(usr, "\r\n") {
			output = settings(values, true, "Check the ODU address, username and password.")
			w.WriteHeader(http.StatusUnprocessableEntity)
			break
		}
		changes := map[string]any{}
		for key, next := range map[string]string{"host": host, "username": usr, "enabled": enable} {
			if values[key] != next {
				changes[key] = next
			}
		}
		if pass != "" {
			changes["password"] = pass
		}
		output = settings(values, true, "")
		if len(changes) > 0 {
			output["commit"] = []any{map[string]any{"config": "jodu5174x", "section": "main", "values": changes}}
			output["notice"] = map[string]any{"level": "info", "text": "Changes staged. Apply them from Verso's top bar."}
		}
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(output)
}
func main() {
	socket := flag.String("socket", defaultSocket, "Unix socket path")
	command := flag.String("command", dataCommand, "local installed read-only data command")
	flag.Parse()
	m := &monitor{command: *command}
	parent := strings.TrimSuffix(*socket, "/"+lastSegment(*socket))
	if err := os.MkdirAll(parent, 0750); err != nil {
		log.Fatal(err)
	}
	gid := -1
	if group, err := user.LookupGroup("verso"); err == nil {
		gid, _ = strconv.Atoi(group.Gid)
	} else {
		log.Fatal("verso group unavailable: ", err)
	}
	if err := os.Chown(parent, -1, gid); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(parent, 0750); err != nil {
		log.Fatal(err)
	}
	if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(*socket)
	defer ln.Close()
	if err := os.Chown(*socket, -1, gid); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(*socket, 0660); err != nil {
		log.Fatal(err)
	}
	go func() {
		m.poll()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			m.poll()
		}
	}()
	log.Fatal(http.Serve(ln, http.HandlerFunc(m.handler)))
}
func lastSegment(p string) string { parts := strings.Split(p, "/"); return parts[len(parts)-1] }
