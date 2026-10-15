package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

// Reboot is asynchronous: the shell's plugin gateway has a five-second budget,
// while the ODU can close its connection while restarting. A one-time token
// prevents a repeated browser POST from issuing another hardware action.
func (m *monitor) startReboot(host string) bool {
	m.mu.Lock()
	if m.rebooting || m.reboot == nil {
		m.mu.Unlock()
		return false
	}
	m.rebooting = true
	m.generation++
	m.value, m.updated, m.checked, m.err = reading{}, time.Time{}, time.Now(), "ODU reboot request pending."
	m.rebootMessage, m.rebootLevel = "ODU reboot request pending. Reload this page to check the outcome.", "info"
	reboot := m.reboot
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := reboot(ctx, host)
		message, level := "ODU acknowledged the reboot request. Internet access may pause while it restarts.", "success"
		if errors.Is(err, errRebootUnconfirmed) {
			message, level = "Reboot request sent, but the ODU disconnected before confirming it. Check its state before trying again.", "warning"
		} else if err != nil {
			message, level = "Could not confirm an ODU reboot. Check its settings and connection before trying again.", "warning"
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.generation++
		m.rebooting = false
		m.rebootMessage, m.rebootLevel = message, level
		m.value, m.updated, m.checked = reading{}, time.Time{}, time.Now()
		if err == nil || errors.Is(err, errRebootUnconfirmed) {
			m.err = "Waiting for the ODU after a reboot request."
		} else {
			m.err = "Could not confirm an ODU reboot. Check Settings."
		}
	}()
	return true
}

func (m *monitor) rebootView(host string) map[string]any {
	m.mu.RLock()
	active, message, level := m.rebooting, m.rebootMessage, m.rebootLevel
	m.mu.RUnlock()
	if active {
		return envelope("Reboot 5G ODU", map[string]any{"type": "stack", "children": []any{
			map[string]any{"type": "callout", "variant": "info", "title": "ODU reboot", "body": message},
			map[string]any{"type": "link", "label": "Check reboot status", "href": "/plugins/jodu5174x/reboot"},
		}})
	}
	page := rebootPage(m.rebootToken(host))
	if message != "" {
		page["notice"] = map[string]any{"level": level, "text": message}
	}
	return page
}

func (m *monitor) rebootRequest(r *http.Request, current map[string]string) (map[string]any, int) {
	if r.Method == http.MethodGet {
		return m.rebootView(current["host"]), http.StatusOK
	}
	if r.ParseForm() != nil || r.PostForm.Get("confirm") != "1" || !m.consumeRebootToken(r.PostForm.Get("token"), current["host"]) {
		page := m.rebootView(current["host"])
		page["notice"] = map[string]any{"level": "warning", "text": "Confirm the ODU reboot using a fresh confirmation page."}
		return page, http.StatusUnprocessableEntity
	}
	started := m.startReboot(current["host"])
	page := m.rebootView(current["host"])
	if !started {
		page["notice"] = map[string]any{"level": "warning", "text": "A reboot is already pending or the reboot service is unavailable."}
	}
	return page, http.StatusOK
}

type rebootConfirmation struct {
	host  string
	until time.Time
}

func (m *monitor) rebootToken(host string) string {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.confirmations == nil {
		m.confirmations = map[string]rebootConfirmation{}
	}
	for token, confirmation := range m.confirmations {
		if time.Now().After(confirmation.until) {
			delete(m.confirmations, token)
		}
	}
	if len(m.confirmations) >= 64 {
		return ""
	}
	token := hex.EncodeToString(random[:])
	m.confirmations[token] = rebootConfirmation{host: host, until: time.Now().Add(5 * time.Minute)}
	return token
}

func (m *monitor) consumeRebootToken(token, host string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	confirmation, ok := m.confirmations[token]
	delete(m.confirmations, token)
	return ok && confirmation.host == host && time.Now().Before(confirmation.until)
}

func rebootPage(token string) map[string]any {
	children := []any{
		warn("Reboot the outdoor unit", "Internet access through the ODU will pause. This restarts the outdoor unit, not this router."),
	}
	if token != "" {
		children = append(children, map[string]any{"type": "form", "submit": "Reboot ODU", "fields": []any{
			map[string]any{"type": "field", "kind": "hidden", "name": "token", "value": token},
			map[string]any{"type": "switch", "name": "confirm", "label": "I understand Internet access will pause", "on": false},
		}})
	} else {
		children = append(children, warn("Confirmation unavailable", "Reload this page later to request another confirmation."))
	}
	children = append(children, map[string]any{"type": "link", "label": "Back to ODU", "href": "/plugins/jodu5174x/"})
	return envelope("Reboot 5G ODU", map[string]any{"type": "stack", "children": children})
}
