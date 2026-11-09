package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Supervisor kapselt die Home-Assistant-Supervisor-API (nur für Add-on-eigene Aufrufe).
type Supervisor struct {
	base  string
	token string
	hc    *http.Client
}

type AddonStats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsage   float64 `json:"memory_usage"`
	MemoryLimit   float64 `json:"memory_limit"`
	MemoryPercent float64 `json:"memory_percent"`
	NetworkRx     float64 `json:"network_rx"`
	NetworkTx     float64 `json:"network_tx"`
}

type AddonInfo struct {
	Version string         `json:"version"`
	Options map[string]any `json:"options"`
}

type mqttCreds struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"username"`
	Pass string `json:"password"`
	SSL  bool   `json:"ssl"`
}

func newSupervisor(base string) *Supervisor {
	tok := os.Getenv("SUPERVISOR_TOKEN")
	if tok == "" {
		for _, p := range []string{"/run/s6/container_environment/SUPERVISOR_TOKEN", "/var/run/s6/container_environment/SUPERVISOR_TOKEN"} {
			if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
				tok = strings.TrimSpace(string(b))
				break
			}
		}
	}
	return &Supervisor{base: strings.TrimRight(base, "/"), token: tok, hc: &http.Client{Timeout: 10 * time.Second}}
}

func (s *Supervisor) Available() bool { return s.token != "" }

func (s *Supervisor) call(method, p string, in, out any) error {
	if !s.Available() {
		return errors.New("kein Supervisor-Token (hassio_api aktiv?)")
	}
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env struct {
		Result  string          `json:"result"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("Supervisor HTTP %d: ungültige Antwort", resp.StatusCode)
	}
	if resp.StatusCode >= 400 || env.Result == "error" {
		return fmt.Errorf("Supervisor: %s (HTTP %d)", env.Message, resp.StatusCode)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func (s *Supervisor) Stats() (*AddonStats, error) {
	var st AddonStats
	if err := s.call("GET", "/addons/self/stats", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Supervisor) Info() (*AddonInfo, error) {
	var in AddonInfo
	if err := s.call("GET", "/addons/self/info", nil, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

func (s *Supervisor) SetOptions(opts map[string]any) error {
	return s.call("POST", "/addons/self/options", map[string]any{"options": opts}, nil)
}

func (s *Supervisor) MQTT() (*mqttCreds, error) {
	var c mqttCreds
	if err := s.call("GET", "/services/mqtt", nil, &c); err != nil {
		return nil, err
	}
	if c.Host == "" {
		return nil, errors.New("Supervisor liefert keinen MQTT-Broker")
	}
	if c.Port == 0 {
		c.Port = 1883
	}
	return &c, nil
}
