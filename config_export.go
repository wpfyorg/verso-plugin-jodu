package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const configSnapshotLimit = 8192

const (
	configSnapshotMaxAge = 45 * time.Second
	configSnapshotFuture = 5 * time.Second
)

type configSnapshot struct {
	Config    snapshotConfig `json:"config"`
	Valid     bool           `json:"valid"`
	Timestamp time.Time      `json:"timestamp"`
}

type snapshotConfig struct {
	Model    string `json:"model"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
}

// exportODUConfig publishes only the committed UCI configuration. Invalid
// reads replace any previous snapshot so the UI cannot keep using old values.
func exportODUConfig(ctx context.Context, command, configDir, saveDir, snapshotPath string, uid, gid int) error {
	cfg, loadErr := loadODUConfig(ctx, command, configDir, saveDir)
	snapshot := configSnapshot{Valid: loadErr == nil, Timestamp: time.Now().UTC()}
	if loadErr == nil {
		snapshot.Config = snapshotConfig{Model: cfg.Model, Host: cfg.Host, Username: cfg.Username, Password: cfg.Password, Enabled: cfg.Enabled}
	}
	if err := writeConfigSnapshot(snapshotPath, snapshot, uid, gid); err != nil {
		return err
	}
	if loadErr != nil {
		return errConfigUnavailable
	}
	return nil
}

func writeConfigSnapshot(path string, snapshot configSnapshot, uid, gid int) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return errConfigUnavailable
	}
	if len(data) > configSnapshotLimit {
		return errConfigUnavailable
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".config-snapshot-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

func readConfigSnapshot(path string) (oduConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return oduConfig{}, errConfigUnavailable
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, configSnapshotLimit+1))
	if err != nil || len(data) > configSnapshotLimit {
		return oduConfig{}, errConfigUnavailable
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || !hasJSONFields(fields, "config", "valid", "timestamp") {
		return oduConfig{}, errConfigUnavailable
	}
	var configFields map[string]json.RawMessage
	if json.Unmarshal(fields["config"], &configFields) != nil {
		return oduConfig{}, errConfigUnavailable
	}
	// Old private snapshots predate explicit model selection and mean 5174x.
	if _, ok := configFields["model"]; !ok {
		configFields["model"] = json.RawMessage(`"jodu5174x"`)
	}
	if !hasJSONFields(configFields, "model", "host", "username", "password", "enabled") {
		return oduConfig{}, errConfigUnavailable
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var snapshot configSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return oduConfig{}, errConfigUnavailable
	}
	if snapshot.Config.Model == "" {
		if _, exists := configFields["model"]; exists {
			return oduConfig{}, errConfigUnavailable
		}
		snapshot.Config.Model = model5174x
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return oduConfig{}, errConfigUnavailable
	}
	if !snapshot.Valid || snapshot.Timestamp.IsZero() ||
		time.Since(snapshot.Timestamp) > configSnapshotMaxAge ||
		snapshot.Timestamp.After(time.Now().Add(configSnapshotFuture)) ||
		!validModel(snapshot.Config.Model) || !validHost(snapshot.Config.Host) || snapshot.Config.Username == "" ||
		len(snapshot.Config.Username) > 64 || len(snapshot.Config.Password) > 256 ||
		strings.ContainsAny(snapshot.Config.Username, "\r\n") {
		return oduConfig{}, errConfigUnavailable
	}
	return oduConfig{Model: snapshot.Config.Model, Host: snapshot.Config.Host, Username: snapshot.Config.Username, Password: snapshot.Config.Password, Enabled: snapshot.Config.Enabled}, nil
}

func hasJSONFields(fields map[string]json.RawMessage, expected ...string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, name := range expected {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}
