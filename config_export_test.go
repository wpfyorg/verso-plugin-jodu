package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExportODUConfigPublishesPrivateOwnedSnapshotAndReplacesIt(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "uci")
	writeSnapshotUCI(t, command, "192.168.225.1", "Admin", "secret-one", "1")
	path := filepath.Join(dir, "config.json")
	uid, gid := os.Getuid(), os.Getgid()

	if err := exportODUConfig(context.Background(), command, dir, dir, path, uid, gid); err != nil {
		t.Fatal("publish failed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal("snapshot missing")
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode = %o", info.Mode().Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatal("snapshot owner mismatch")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"host":"192.168.225.1"`) || !strings.Contains(string(data), `"username":"Admin"`) {
		t.Fatal("snapshot JSON field names are not stable")
	}
	cfg, err := readConfigSnapshot(path)
	if err != nil || cfg.Model != model5174x || cfg.Host != "192.168.225.1" || cfg.Username != "Admin" || cfg.Password != "secret-one" || !cfg.Enabled {
		t.Fatal("snapshot did not round-trip")
	}

	writeSnapshotUCI(t, command, "192.168.225.2", "Other", "secret-two", "0")
	if err := exportODUConfig(context.Background(), command, dir, dir, path, uid, gid); err != nil {
		t.Fatal("replacement failed")
	}
	cfg, err = readConfigSnapshot(path)
	if err != nil || cfg.Host != "192.168.225.2" || cfg.Username != "Other" || cfg.Password != "secret-two" || cfg.Enabled {
		t.Fatal("replacement not visible")
	}
}

func TestExportODUConfigFailureReplacesSnapshotWithInvalidState(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "uci")
	writeSnapshotUCI(t, command, "192.168.225.1", "Admin", "secret", "1")
	path := filepath.Join(dir, "config.json")
	if err := exportODUConfig(context.Background(), command, dir, dir, path, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal("initial publish failed")
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal("cannot set failing UCI fixture")
	}
	if err := exportODUConfig(context.Background(), command, dir, dir, path, os.Getuid(), os.Getgid()); !errors.Is(err, errConfigUnavailable) {
		t.Fatal("failed read was not reported as unavailable")
	}
	if _, err := readConfigSnapshot(path); !errors.Is(err, errConfigUnavailable) {
		t.Fatal("old config remained usable after source failure")
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "secret") || !strings.Contains(string(data), `"valid":false`) {
		t.Fatal("invalid snapshot is missing or contains config values")
	}
}

func TestReadConfigSnapshotRejectsMalformedOversizeAndStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for name, contents := range map[string][]byte{
		"malformed":       []byte(`{"valid":`),
		"unknown field":   []byte(`{"valid":true,"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","config":{"Host":"192.168.225.1","Username":"Admin","Password":"x","Enabled":true},"other":1}`),
		"missing field":   []byte(`{"valid":true,"timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","config":{"host":"192.168.225.1","username":"Admin","enabled":true}}`),
		"invalid model":   mustMarshalSnapshot(t, configSnapshot{Valid: true, Timestamp: time.Now().UTC(), Config: snapshotConfig{Model: "auto", Host: "192.168.225.1", Username: "Admin"}}),
		"empty model":     mustMarshalSnapshot(t, configSnapshot{Valid: true, Timestamp: time.Now().UTC(), Config: snapshotConfig{Model: "", Host: "192.168.225.1", Username: "Admin"}}),
		"oversize":        []byte(strings.Repeat("x", configSnapshotLimit+1)),
		"invalid wrapper": mustMarshalSnapshot(t, configSnapshot{Valid: false, Timestamp: time.Now().UTC()}),
		"stale":           mustMarshalSnapshot(t, configSnapshot{Valid: true, Timestamp: time.Now().Add(-configSnapshotMaxAge - time.Second), Config: snapshotConfig{Host: "192.168.225.1", Username: "Admin"}}),
		"future":          mustMarshalSnapshot(t, configSnapshot{Valid: true, Timestamp: time.Now().Add(configSnapshotFuture + time.Second), Config: snapshotConfig{Host: "192.168.225.1", Username: "Admin"}}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, contents, 0600); err != nil {
				t.Fatal("cannot write test snapshot")
			}
			if _, err := readConfigSnapshot(path); !errors.Is(err, errConfigUnavailable) {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func writeSnapshotUCI(t *testing.T, command, host, username, password, enabled string) {
	t.Helper()
	script := "#!/bin/sh\nfor last do :; done\ncase \"$last\" in\n" +
		"jodu.main) printf 'jodu\\n';;\n" +
		"jodu.main.host) printf '" + host + "\\n';;\n" +
		"jodu.main.username) printf '" + username + "\\n';;\n" +
		"jodu.main.password) printf '" + password + "\\n';;\n" +
		"jodu.main.enabled) printf '" + enabled + "\\n';;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal("cannot write UCI fixture")
	}
}

func mustMarshalSnapshot(t *testing.T, snapshot configSnapshot) []byte {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal("cannot encode fixture")
	}
	return data
}
