package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type providerSet struct {
	legacy *oduClient
	p5164  *sessionProvider
	p52140 *statusProvider
}

func newProviderSet() *providerSet {
	return &providerSet{legacy: newODUClient(), p5164: newSessionProvider(), p52140: newStatusProvider()}
}

func (p *providerSet) Read(ctx context.Context, cfg oduConfig) (reading, error) {
	switch cfg.Model {
	case model5174x:
		return p.legacy.Read(ctx, cfg)
	case model5164x:
		if !cfg.Enabled {
			return reading{}, errMonitoringDisabled
		}
		return p.p5164.Read(ctx, cfg)
	case model52140:
		if !cfg.Enabled {
			return reading{}, errMonitoringDisabled
		}
		return p.p52140.Read(ctx, cfg)
	default:
		return reading{}, errors.New("unsupported ODU model")
	}
}

func supportsReboot(model string) bool { return model == model5174x || model == model52140 }

func (p *providerSet) Reboot(ctx context.Context, cfg oduConfig) error {
	switch cfg.Model {
	case model5174x:
		return p.legacy.Reboot(ctx, cfg)
	case model52140:
		return p.p52140.Reboot(ctx, cfg)
	default:
		return errors.New("reboot is unavailable for this ODU model")
	}
}

const sessionKey = "$1$SERCOMM$"

// jodu5164xPassword mirrors the device's two HMAC rounds: each round hashes
// the lowercase hex text from the previous round, then Base64 encodes the
// final lowercase hex text. It does not reuse code from the reference package.
func jodu5164xPassword(password string) string {
	mac := func(message string) string {
		h := hmac.New(sha256.New, []byte(sessionKey))
		_, _ = h.Write([]byte(message))
		return hex.EncodeToString(h.Sum(nil))
	}
	return base64.StdEncoding.EncodeToString([]byte(mac(mac(password))))
}

type sessionProvider struct {
	mu     sync.Mutex
	config oduConfig
	client *http.Client
	base   string
}

func newSessionProvider() *sessionProvider {
	jar, _ := cookiejar.New(nil)
	return &sessionProvider{client: &http.Client{Timeout: oduRequestTimeout, Jar: jar, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, MaxResponseHeaderBytes: oduResponseLimit,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *sessionProvider) endpoint(cfg oduConfig, path string) (string, error) {
	if p.base != "" {
		return strings.TrimRight(p.base, "/") + path, nil
	}
	if !validHost(cfg.Host) || strings.ContainsAny(cfg.Host, "/?#@") {
		return "", errors.New("invalid ODU host")
	}
	return "https://" + net.JoinHostPort(cfg.Host, "443") + path, nil
}

func (p *sessionProvider) reset(cfg oduConfig) {
	p.config = cfg
	p.client.Jar, _ = cookiejar.New(nil)
}

func (p *sessionProvider) request(ctx context.Context, cfg oduConfig, method, path string, form url.Values) ([]byte, int, error) {
	endpoint, err := p.endpoint(cfg, path)
	if err != nil {
		return nil, 0, err
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, sanitizedReadError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, oduResponseLimit+1))
	if err != nil || len(raw) > oduResponseLimit {
		return nil, resp.StatusCode, errors.New("invalid ODU response")
	}
	return raw, resp.StatusCode, nil
}

func (p *sessionProvider) login(ctx context.Context, cfg oduConfig) error {
	form := url.Values{"LoginName": {cfg.Username}, "LoginPWD": {jodu5164xPassword(cfg.Password)}, "remember_pwd": {"0"}}
	_, status, err := p.request(ctx, cfg, http.MethodPost, "/data/login.json", form)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return errODULogin
	}
	u, err := url.Parse(mustEndpoint(p, cfg, "/data/login.json"))
	if err != nil {
		return errODULogin
	}
	for _, cookie := range p.client.Jar.Cookies(u) {
		if cookie.Name == "QSESSIONID" && cookie.Value != "" {
			return nil
		}
	}
	return errODULogin
}

func mustEndpoint(p *sessionProvider, cfg oduConfig, path string) string {
	endpoint, _ := p.endpoint(cfg, path)
	return endpoint
}

func (p *sessionProvider) getJSON(ctx context.Context, cfg oduConfig, endpoint string) (map[string]any, error) {
	raw, status, err := p.request(ctx, cfg, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, protocolError{expired: true}
	}
	obj, err := decodeODUObject(raw)
	if err != nil {
		return nil, protocolError{expired: true}
	}
	return obj, nil
}

func (p *sessionProvider) readOnce(ctx context.Context, cfg oduConfig) (reading, error) {
	primary, err := p.getJSON(ctx, cfg, "/data/network_status_cell_parameters.json")
	if err != nil || !hasSignal(primary) {
		return reading{}, protocolError{expired: true}
	}
	secondary, _ := p.getJSON(ctx, cfg, "/data/network_status_secondary_cell_parameters.json")
	lan, _ := p.getJSON(ctx, cfg, "/data/network_status_lan.json")
	device, _ := p.getJSON(ctx, cfg, "/data/network_status_device_data.json")
	return normalize5164(primary, secondary, lan, device), nil
}

func hasSignal(v map[string]any) bool {
	if v == nil {
		return false
	}
	_, ok := v["signal_strength"]
	return ok
}

func (p *sessionProvider) Read(ctx context.Context, cfg oduConfig) (reading, error) {
	if cfg.Password == "" {
		return reading{}, errPasswordMissing
	}
	if !validHost(cfg.Host) || cfg.Username == "" || len(cfg.Username) > 64 || len(cfg.Password) > 256 {
		return reading{}, errors.New("invalid ODU configuration")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		p.client = newSessionProvider().client
	}
	if p.config != cfg {
		p.reset(cfg)
	}
	if len(p.client.Jar.Cookies(mustURL(p, cfg))) == 0 {
		if err := p.login(ctx, cfg); err != nil {
			return reading{}, err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := p.readOnce(ctx, cfg)
		var expired protocolError
		if errors.As(err, &expired) && attempt == 0 {
			p.reset(cfg)
			if loginErr := p.login(ctx, cfg); loginErr != nil {
				return reading{}, loginErr
			}
			continue
		}
		if err != nil {
			return reading{}, errODUUnavailable
		}
		return result, nil
	}
	return reading{}, errODUUnavailable
}

func mustURL(p *sessionProvider, cfg oduConfig) *url.URL {
	u, _ := url.Parse(mustEndpoint(p, cfg, "/data/network_status_cell_parameters.json"))
	return u
}

func normalize5164(primary, secondary, lan, device map[string]any) reading {
	cell := func(src map[string]any, suffix string) map[string]any {
		if src == nil {
			return nil
		}
		out := make(map[string]any, len(src)+2)
		for k, v := range src {
			out[k] = v
		}
		for from, to := range map[string]string{"bandwidth": "band_width", "nr_earcn": "arfcn", "physical_cell_id": "pcid", "dl_bler": "bler_dl", "ss_rsrp": "rsrp", "ss_rsrq": "rsrq", "ss_sinr": "sinr"} {
			if v, ok := src[from]; ok {
				out[to+suffix] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(fmt.Sprint(v), "dBm"), "dB"))
			}
		}
		for from, to := range map[string]string{"band": "band", "mimo": "mimo", "modulation": "modulation", "cqi": "cqi"} {
			if v, ok := src[from]; ok {
				out[to+suffix] = v
			}
		}
		return out
	}
	netData := cell(primary, "")
	if netData == nil {
		netData = map[string]any{}
	}
	for from, to := range map[string]string{"operating_mode": "operation_mode", "rrc_state": "rrc_state", "plmn": "plmn"} {
		if v, ok := primary[from]; ok {
			netData[to] = v
		}
	}
	if sec := cell(secondary, "_sec"); sec != nil {
		for k, v := range sec {
			netData[k] = v
		}
	}
	data := map[string]any{}
	for from, to := range map[string]string{"data_sent": "upData", "data_received": "downData", "packet_loss": "packet_loss"} {
		if v, ok := device[from]; ok {
			data[to] = v
		}
	}
	for from, label := range map[string]string{"link_status": "link_status", "speed": "eth_speed", "duplex_mode": "eth_duplex", "connection_uptime": "eth_uptime"} {
		if v, ok := lan[from]; ok {
			netData[label] = v
		}
	}
	return reading{Net: netData, Data: data}
}

type statusProvider struct {
	client      *http.Client
	base        string
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func newStatusProvider() *statusProvider {
	dialer := &net.Dialer{Timeout: oduRequestTimeout}
	return &statusProvider{client: &http.Client{Timeout: oduRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, dialContext: dialer.DialContext}
}

func (p *statusProvider) Reboot(ctx context.Context, cfg oduConfig) error {
	if !validHost(cfg.Host) || cfg.Username == "" || cfg.Password == "" || len(cfg.Username) > 64 || len(cfg.Password) > 256 || strings.ContainsAny(cfg.Username+cfg.Password, "\r\n") {
		return errors.New("invalid ODU configuration")
	}
	dial := p.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: oduRequestTimeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(cfg.Host, "23"))
	if err != nil {
		return sanitizedReadError(ctx, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(8 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if _, err = readTelnetPrompt(conn, "login:", "username:"); err != nil {
		return errODUUnavailable
	}
	if _, err = io.WriteString(conn, cfg.Username+"\r\n"); err != nil {
		return errODUUnavailable
	}
	if _, err = readTelnetPrompt(conn, "password:"); err != nil {
		return errODUUnavailable
	}
	if _, err = io.WriteString(conn, cfg.Password+"\r\n"); err != nil {
		return errODUUnavailable
	}
	if _, err = readTelnetPrompt(conn, "#", "$", "> "); err != nil {
		return errODUUnavailable
	}
	if _, err = io.WriteString(conn, "reboot\r\n"); err != nil {
		return errODUUnavailable
	}
	return errRebootUnconfirmed
}

func readTelnetPrompt(conn net.Conn, prompts ...string) (string, error) {
	var out strings.Builder
	buf := make([]byte, 1)
	for out.Len() < 8192 {
		if _, err := io.ReadFull(conn, buf); err != nil {
			return out.String(), err
		}
		if buf[0] == 255 { // Skip the three-byte Telnet option negotiation.
			negotiation := make([]byte, 2)
			if _, err := io.ReadFull(conn, negotiation); err != nil {
				return out.String(), err
			}
			continue
		}
		out.WriteByte(buf[0])
		text := strings.ToLower(out.String())
		for _, prompt := range prompts {
			if strings.Contains(text, strings.ToLower(prompt)) {
				return text, nil
			}
		}
	}
	return out.String(), errors.New("Telnet prompt too large")
}

func (p *statusProvider) endpoint(cfg oduConfig) (string, error) {
	if p.base != "" {
		return strings.TrimRight(p.base, "/") + "/status.txt", nil
	}
	if !validHost(cfg.Host) || strings.ContainsAny(cfg.Host, "/?#@") {
		return "", errors.New("invalid ODU host")
	}
	return "http://" + net.JoinHostPort(cfg.Host, "8080") + "/status.txt", nil
}

func (p *statusProvider) Read(ctx context.Context, cfg oduConfig) (reading, error) {
	endpoint, err := p.endpoint(cfg)
	if err != nil {
		return reading{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return reading{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return reading{}, sanitizedReadError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return reading{}, errSetupRequired
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, oduResponseLimit+1))
	if err != nil || len(raw) > oduResponseLimit {
		return reading{}, errODUUnavailable
	}
	netData, data := parse52140Status(raw)
	if len(netData) == 0 {
		return reading{}, errSetupRequired
	}
	return reading{Net: netData, Data: data}, nil
}

func parse52140Status(raw []byte) (map[string]any, map[string]any) {
	sections := map[string][]string{}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), oduResponseLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if strings.HasPrefix(line, "---") && strings.HasSuffix(line, "---") {
			section = strings.ToUpper(strings.Trim(line, "-"))
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sections[section] = append(sections[section], line)
	}
	decode := func(name string) map[string]string {
		lines := sections[name]
		joined := strings.Join(lines, "\n")
		var values map[string]any
		if json.Unmarshal([]byte(joined), &values) == nil && values != nil {
			out := make(map[string]string, len(values))
			for k, value := range values {
				out[strings.ToLower(k)] = fmt.Sprint(value)
			}
			return out
		}
		out := map[string]string{}
		if name == "ETH" && len(lines) > 0 {
			parts := strings.Split(lines[0], "|")
			if len(parts) > 0 {
				out["speed"] = strings.TrimSpace(parts[0])
			}
			if len(parts) > 1 {
				out["duplex"] = strings.TrimSpace(parts[1])
			}
			if len(parts) > 2 {
				out["link_status"] = strings.TrimSpace(parts[2])
			}
		} else if len(lines) > 0 {
			out[strings.ToLower(name)] = strings.Join(lines, " ")
		}
		return out
	}
	serving, secondary := decode("SERVING"), decode("SECONDARY")
	netData := map[string]any{}
	putAlias(netData, serving, map[string]string{"status": "operation_mode", "nr5g": "band", "bandwidth": "band_width", "nr_arfcn": "arfcn", "pci": "pcid", "plmn": "plmn", "bler": "bler_dl", "mimo": "mimo", "modulation": "modulation", "rsrp": "rsrp", "rsrq": "rsrq", "sinr": "sinr"})
	putAlias(netData, secondary, map[string]string{"band": "band_sec", "bandwidth": "band_width_sec", "nr_arfcn": "arfcn_sec", "pci": "pcid_sec", "bler": "bler_dl_sec", "mimo": "mimo_sec", "modulation": "modulation_sec", "ss_rsrp": "rsrp_sec", "ss_rsrq": "rsrq_sec", "ss_sinr": "sinr_sec"})
	network := decode("NETWORK")
	putAlias(netData, network, map[string]string{"status": "sim_card_state", "connection": "sim_card_state"})
	eth := decode("ETH")
	putAlias(netData, eth, map[string]string{"link_status": "link_status", "speed": "eth_speed", "duplex": "eth_duplex", "duplex_mode": "eth_duplex"})
	if len(sections["CPU"]) > 0 {
		netData["odu_cpu_pct"] = strings.TrimSpace(strings.Join(sections["CPU"], " ")) + "%"
	}
	if len(sections["MEM"]) > 0 {
		netData["odu_mem_pct"] = strings.TrimSpace(strings.Join(sections["MEM"], " ")) + "%"
	}
	if len(sections["QTEMP"]) > 0 {
		temp := strings.TrimSpace(strings.Join(sections["QTEMP"], " "))
		if _, after, ok := strings.Cut(temp, ","); ok {
			netData["temperature"] = strings.TrimSpace(after) + " °C"
		}
	}
	if len(sections["UPTIME"]) > 0 {
		if fields := strings.Fields(strings.Join(sections["UPTIME"], " ")); len(fields) > 0 {
			if seconds, err := strconv.ParseFloat(fields[0], 64); err == nil && seconds >= 0 {
				netData["odu_uptime"] = (time.Duration(seconds * float64(time.Second))).String()
			}
		}
	}
	if len(sections["VERSION"]) > 0 {
		netData["softversion"] = strings.TrimSpace(strings.Join(sections["VERSION"], " "))
	}
	data := map[string]any{}
	putAlias(data, network, map[string]string{"rx_bytes": "downData", "tx_bytes": "upData"})
	if serving == nil || len(serving) == 0 || len(sections["UPTIME"]) == 0 || !strings.Contains(strings.Join(sections["VERSION"], " "), "1.2.1") {
		return nil, nil
	}
	return netData, data
}

func putAlias(dest map[string]any, src map[string]string, aliases map[string]string) {
	for from, to := range aliases {
		if value := strings.TrimSpace(src[from]); value != "" {
			dest[to] = value
		}
	}
}

var errSetupRequired = errors.New("ODU status helper setup required")
