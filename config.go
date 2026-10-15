package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var errConfigUnavailable = errors.New("ODU configuration unavailable")

// The monitor reads committed configuration through its own UCI save directory.
// Verso's per-session pending edits remain in the shell's Apply workflow.
func loadODUConfig(ctx context.Context, command, configDir, saveDir string) (oduConfig, error) {
	get := func(key string) (string, error) {
		out, err := exec.CommandContext(ctx, command, "-q", "-c", configDir, "-t", saveDir, "-C", saveDir, "get", "jodu5174x.main"+key).Output()
		if err != nil || len(out) > 4096 {
			return "", errConfigUnavailable
		}
		return strings.TrimSuffix(string(out), "\n"), nil
	}
	typ, err := get("")
	if err != nil || typ != "jodu5174x" {
		return oduConfig{}, errConfigUnavailable
	}
	values := map[string]string{}
	for _, key := range []string{"host", "username", "password", "enabled"} {
		value, err := get("." + key)
		if err != nil {
			return oduConfig{}, errConfigUnavailable
		}
		values[key] = value
	}
	if !validHost(values["host"]) || values["username"] == "" || len(values["username"]) > 64 || len(values["password"]) > 256 ||
		strings.ContainsAny(values["username"], "\r\n") || (values["enabled"] != "0" && values["enabled"] != "1") {
		return oduConfig{}, errConfigUnavailable
	}
	return oduConfig{Host: values["host"], Username: values["username"], Password: values["password"], Enabled: values["enabled"] == "1"}, nil
}

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,64}$`)

// Ethernet health remains useful when the ODU's HTTPS service is unavailable.
// The route selects the actual interface rather than guessing a port name.
func ethernetLink(ctx context.Context, host string) string {
	address := net.ParseIP(host)
	if address == nil {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return "Unknown"
		}
		address = addresses[0].IP
	}
	out, err := exec.CommandContext(ctx, "ip", "-j", "route", "get", address.String()).Output()
	if err != nil || len(out) > 8192 {
		return "Unknown"
	}
	return ethernetRouteLink(out, "/sys/class/net")
}

func ethernetRouteLink(route []byte, sysfs string) string {
	var routes []struct{ Dev string }
	if json.Unmarshal(route, &routes) != nil || len(routes) == 0 || !interfaceName.MatchString(routes[0].Dev) || routes[0].Dev == "." || routes[0].Dev == ".." {
		return "Unknown"
	}
	read := func(name string) string {
		value, _ := os.ReadFile(filepath.Join(sysfs, routes[0].Dev, name))
		return strings.TrimSpace(string(value))
	}
	switch read("carrier") {
	case "0":
		return "Down"
	case "1":
		speed, err := strconv.Atoi(read("speed"))
		if err != nil || speed <= 0 {
			return "Up"
		}
		label := fmt.Sprintf("%d Mbps", speed)
		if duplex := read("duplex"); duplex == "full" || duplex == "half" {
			label += " (" + duplex + " duplex)"
		}
		return label
	default:
		return "Unknown"
	}
}
