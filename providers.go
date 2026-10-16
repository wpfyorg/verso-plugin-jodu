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
	transport := &http.Transport{Proxy: nil, MaxResponseHeaderBytes: oduResponseLimit}
	return &statusProvider{client: &http.Client{Timeout: oduRequestTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, dialContext: dialer.DialContext}
}

func (p *statusProvider) Reboot(ctx context.Context, cfg oduConfig) error {
	conn, cleanup, err := p.openTelnet(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err = io.WriteString(conn, "reboot\r\n"); err != nil {
		return errODUUnavailable
	}
	return errRebootUnconfirmed
}

func (p *statusProvider) openTelnet(ctx context.Context, cfg oduConfig) (net.Conn, func(), error) {
	if !validHost(cfg.Host) || cfg.Username == "" || cfg.Password == "" || len(cfg.Username) > 64 || len(cfg.Password) > 256 || strings.ContainsAny(cfg.Username+cfg.Password, "\r\n") {
		return nil, nil, errors.New("invalid ODU configuration")
	}
	dial := p.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: oduRequestTimeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(cfg.Host, "23"))
	if err != nil {
		return nil, nil, sanitizedReadError(ctx, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() {
		stop()
		_ = conn.Close()
	}
	deadline := time.Now().Add(8 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		cleanup()
		return nil, nil, errODUUnavailable
	}
	if _, err = readTelnetPrompt(conn, "login:", "username:"); err != nil {
		cleanup()
		return nil, nil, errODUUnavailable
	}
	if _, err = io.WriteString(conn, cfg.Username+"\r\n"); err != nil {
		cleanup()
		return nil, nil, errODUUnavailable
	}
	if _, err = readTelnetPrompt(conn, "password:"); err != nil {
		cleanup()
		return nil, nil, errODUUnavailable
	}
	if _, err = io.WriteString(conn, cfg.Password+"\r\n"); err != nil {
		cleanup()
		return nil, nil, errODUUnavailable
	}
	if _, err = readTelnetPrompt(conn, "#", "$", "> "); err != nil {
		cleanup()
		return nil, nil, errODUUnavailable
	}
	return conn, cleanup, nil
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
			if strings.HasSuffix(strings.TrimSpace(text), strings.TrimSpace(strings.ToLower(prompt))) {
				return out.String(), nil
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
	if !cfg.Enabled {
		return reading{}, errMonitoringDisabled
	}
	endpoint, err := p.endpoint(cfg)
	if err != nil {
		return reading{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return reading{}, err
	}
	resp, err := p.client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, oduResponseLimit+1))
			if readErr == nil && len(raw) <= oduResponseLimit {
				netData, data := parse52140Status(raw)
				if len(netData) > 0 {
					return reading{Net: netData, Data: data}, nil
				}
			}
		}
	}
	return p.readTelnetStatus(ctx, cfg)
}

var jodu52140ReadCommands = [...]string{
	`ubus call luxslam 5g '{"cmd":"get","class":"serving_cell"}'`,
	`ubus call luxslam 5g '{"cmd":"get","class":"secondary_cell"}'`,
	`ubus call luxslam 5g '{"cmd":"get","class":"network"}'`,
}

func (p *statusProvider) readTelnetStatus(ctx context.Context, cfg oduConfig) (reading, error) {
	conn, cleanup, err := p.openTelnet(ctx, cfg)
	if err != nil {
		return reading{}, errODUUnavailable
	}
	defer cleanup()
	objects := make([]map[string]string, 0, len(jodu52140ReadCommands))
	for _, command := range jodu52140ReadCommands {
		output, err := run52140ReadCommand(conn, command)
		if err != nil {
			return reading{}, errODUUnavailable
		}
		object, err := parse52140UbusObject(output)
		if err != nil {
			return reading{}, errODUUnavailable
		}
		objects = append(objects, object)
	}
	netData, data := normalize52140Objects(objects[0], objects[1], objects[2])
	if len(netData) == 0 || (netData["rsrp"] == nil && netData["rsrq"] == nil && netData["sinr"] == nil) {
		return reading{}, errODUUnavailable
	}
	return reading{Net: netData, Data: data}, nil
}

func run52140ReadCommand(conn net.Conn, command string) (string, error) {
	allowed := false
	for _, candidate := range jodu52140ReadCommands {
		if command == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", errors.New("unsupported status command")
	}
	if _, err := io.WriteString(conn, command+"\r\n"); err != nil {
		return "", err
	}
	output, err := readTelnetPrompt(conn, "#", "$", "> ")
	if err != nil {
		return "", err
	}
	if echo := strings.Index(output, command); echo >= 0 {
		end := echo + len(command)
		for end < len(output) && (output[end] == '\r' || output[end] == '\n') {
			end++
		}
		output = output[end:]
	}
	return output, nil
}

func parse52140UbusObject(output string) (map[string]string, error) {
	start := strings.IndexByte(output, '{')
	if start < 0 {
		return nil, errors.New("missing status JSON")
	}
	depth, end := 0, -1
	inString, escaped := false, false
	for i := start; i < len(output); i++ {
		c := output[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				i = len(output)
			}
		}
	}
	if end < 0 {
		return nil, errors.New("incomplete status JSON")
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(output[start:end]), &values); err != nil || values == nil {
		return nil, errors.New("invalid status JSON")
	}
	for _, key := range []string{"data", "result", "values"} {
		if nested, ok := values[key].(map[string]any); ok {
			values = nested
			break
		}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if value != nil {
			out[strings.ToLower(key)] = fmt.Sprint(value)
		}
	}
	return out, nil
}

func normalize52140Objects(serving, secondary, network map[string]string) (map[string]any, map[string]any) {
	netData := map[string]any{}
	putAlias(netData, serving, map[string]string{
		"status": "operation_mode", "operating_mode": "operation_mode", "rrc_state": "rrc_state",
		"nr5g": "band", "band": "band", "bandwidth": "band_width", "band_width": "band_width",
		"nr_arfcn": "arfcn", "nr_earcn": "arfcn", "arfcn": "arfcn", "pci": "pcid", "physical_cell_id": "pcid",
		"plmn": "plmn", "bler": "bler_dl", "dl_bler": "bler_dl", "mimo": "mimo", "modulation": "modulation",
		"cqi": "cqi", "rsrp": "rsrp", "ss_rsrp": "rsrp", "rsrq": "rsrq", "ss_rsrq": "rsrq", "sinr": "sinr", "ss_sinr": "sinr",
	})
	putAlias(netData, secondary, map[string]string{
		"nr5g": "band_sec", "band": "band_sec", "bandwidth": "band_width_sec", "band_width": "band_width_sec",
		"nr_arfcn": "arfcn_sec", "nr_earcn": "arfcn_sec", "arfcn": "arfcn_sec", "pci": "pcid_sec", "physical_cell_id": "pcid_sec",
		"plmn": "plmn_sec", "bler": "bler_dl_sec", "dl_bler": "bler_dl_sec", "mimo": "mimo_sec", "modulation": "modulation_sec",
		"cqi": "cqi_sec", "rsrp": "rsrp_sec", "ss_rsrp": "rsrp_sec", "rsrq": "rsrq_sec", "ss_rsrq": "rsrq_sec", "sinr": "sinr_sec", "ss_sinr": "sinr_sec",
	})
	putAlias(netData, network, map[string]string{"status": "sim_card_state", "connection": "sim_card_state"})
	data := map[string]any{}
	putAlias(data, network, map[string]string{"rx_bytes": "downData", "tx_bytes": "upData"})
	return netData, data
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
	serving, secondary, network := decode("SERVING"), decode("SECONDARY"), decode("NETWORK")
	netData, data := normalize52140Objects(serving, secondary, network)
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
