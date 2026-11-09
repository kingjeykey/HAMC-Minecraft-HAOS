package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mqttNode       = "hamc_java"
	mqttPrefix     = "homeassistant"
	topicState     = "hamc_java/state"
	topicAttrs     = "hamc_java/attributes"
	topicAvailable = "hamc_java/availability"
)

type entityDef struct {
	comp, key, name, unit, devClass, stateClass, icon, category string
	attrs                                                       bool
}

var entityDefs = []entityDef{
	{comp: "binary_sensor", key: "online", name: "Server läuft", devClass: "running"},
	{comp: "sensor", key: "players_online", name: "Spieler online", icon: "mdi:account-multiple", stateClass: "measurement", attrs: true},
	{comp: "sensor", key: "players_max", name: "Spieler maximal", icon: "mdi:account-group"},
	{comp: "sensor", key: "latency_ms", name: "Latenz", unit: "ms", devClass: "duration", stateClass: "measurement"},
	{comp: "sensor", key: "tps", name: "TPS", unit: "TPS", icon: "mdi:speedometer", stateClass: "measurement"},
	{comp: "sensor", key: "mspt", name: "Tick-Zeit (MSPT)", unit: "ms", devClass: "duration", stateClass: "measurement"},
	{comp: "sensor", key: "mspt_p95", name: "Tick-Zeit P95", unit: "ms", devClass: "duration", stateClass: "measurement"},
	{comp: "sensor", key: "addon_cpu", name: "Add-on CPU", unit: "%", icon: "mdi:cpu-64-bit", stateClass: "measurement"},
	{comp: "sensor", key: "addon_memory_percent", name: "Add-on RAM", unit: "%", icon: "mdi:memory", stateClass: "measurement"},
	{comp: "sensor", key: "addon_memory_mb", name: "Add-on RAM belegt", unit: "MB", devClass: "data_size", stateClass: "measurement"},
	{comp: "sensor", key: "disk_free_gb", name: "Freier Speicher", unit: "GB", devClass: "data_size", stateClass: "measurement"},
	{comp: "sensor", key: "whitelist_count", name: "Whitelist-Einträge", icon: "mdi:format-list-checks"},
	{comp: "binary_sensor", key: "whitelist_enabled", name: "Whitelist aktiv", category: "diagnostic", icon: "mdi:shield-account"},
	{comp: "sensor", key: "version", name: "Server-Version", category: "diagnostic", icon: "mdi:tag"},
	{comp: "sensor", key: "motd", name: "MOTD", category: "diagnostic", icon: "mdi:message-text"},
}

// MQTTPublisher veröffentlicht Statuswerte per MQTT-Discovery. Es wird nur ein
// minimaler MQTT-3.1.1-Client (Publish mit QoS 0) benötigt, daher keine Fremdbibliothek.
type MQTTPublisher struct {
	app *App
	ch  chan Snapshot
	mu  sync.Mutex
	st  string
}

func newMQTT(a *App) *MQTTPublisher {
	return &MQTTPublisher{app: a, ch: make(chan Snapshot, 1), st: "startet"}
}

func (m *MQTTPublisher) Status() string { m.mu.Lock(); defer m.mu.Unlock(); return m.st }
func (m *MQTTPublisher) setStatus(s string) {
	m.mu.Lock()
	changed := m.st != s
	m.st = s
	m.mu.Unlock()
	if changed {
		log.Printf("MQTT: %s", s)
	}
}

func (m *MQTTPublisher) Push(s Snapshot) {
	select {
	case m.ch <- s:
	default:
		select {
		case <-m.ch:
		default:
		}
		select {
		case m.ch <- s:
		default:
		}
	}
}

func (m *MQTTPublisher) enabled() bool {
	v, ok := m.app.opts["MQTT_ENABLED"]
	if !ok {
		return true
	}
	b, isBool := v.(bool)
	return !isBool || b
}

func (m *MQTTPublisher) creds() (*mqttCreds, error) {
	o := m.app.opts
	if host, _ := o["MQTT_HOST"].(string); strings.TrimSpace(host) != "" {
		c := &mqttCreds{Host: strings.TrimSpace(host), Port: 1883}
		if p, ok := o["MQTT_PORT"].(float64); ok && p > 0 {
			c.Port = int(p)
		}
		c.User, _ = o["MQTT_USER"].(string)
		c.Pass, _ = o["MQTT_PASSWORD"].(string)
		return c, nil
	}
	return m.app.sup.MQTT()
}

func (m *MQTTPublisher) run() {
	if !m.enabled() {
		m.setStatus("deaktiviert")
		return
	}
	for {
		cr, err := m.creds()
		if err != nil {
			m.setStatus("kein Broker gefunden (" + err.Error() + ")")
			time.Sleep(60 * time.Second)
			continue
		}
		err = m.session(cr)
		m.setStatus("getrennt: " + err.Error())
		time.Sleep(15 * time.Second)
	}
}

func mqttStr(b *bytes.Buffer, s string) {
	b.WriteByte(byte(len(s) >> 8))
	b.WriteByte(byte(len(s)))
	b.WriteString(s)
}

func mqttRemaining(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			return out
		}
	}
}

func mqttPacket(first byte, body []byte) []byte {
	out := []byte{first}
	out = append(out, mqttRemaining(len(body))...)
	return append(out, body...)
}

func buildConnect(cr *mqttCreds) []byte {
	var b bytes.Buffer
	mqttStr(&b, "MQTT")
	flags := byte(0x02 | 0x04 | 0x20) // clean session, will, will retain
	if cr.User != "" {
		flags |= 0x80
		if cr.Pass != "" {
			flags |= 0x40
		}
	}
	b.WriteByte(4)
	b.WriteByte(flags)
	b.WriteByte(0)
	b.WriteByte(60)
	mqttStr(&b, "hamc-java-addon")
	mqttStr(&b, topicAvailable)
	mqttStr(&b, "offline")
	if cr.User != "" {
		mqttStr(&b, cr.User)
		if cr.Pass != "" {
			mqttStr(&b, cr.Pass)
		}
	}
	return mqttPacket(0x10, b.Bytes())
}

func buildPublish(topic string, payload []byte, retain bool) []byte {
	var b bytes.Buffer
	mqttStr(&b, topic)
	b.Write(payload)
	first := byte(0x30)
	if retain {
		first |= 0x01
	}
	return mqttPacket(first, b.Bytes())
}

func (m *MQTTPublisher) session(cr *mqttCreds) error {
	addr := net.JoinHostPort(cr.Host, strconv.Itoa(cr.Port))
	d := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if cr.SSL {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true}) // interner Broker
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(buildConnect(cr)); err != nil {
		return err
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		return err
	}
	if ack[0] != 0x20 || ack[3] != 0 {
		return fmt.Errorf("Broker lehnt Verbindung ab (Code %d, Benutzername/Passwort prüfen)", ack[3])
	}
	_ = conn.SetDeadline(time.Time{})

	errCh := make(chan error, 1)
	go func() { // eingehende Pakete (PINGRESP u. a.) verwerfen
		br := bufio.NewReader(conn)
		for {
			if _, err := br.ReadByte(); err != nil {
				errCh <- err
				return
			}
			n, shift := 0, uint(0)
			for {
				c, err := br.ReadByte()
				if err != nil {
					errCh <- err
					return
				}
				n |= int(c&0x7F) << shift
				if c&0x80 == 0 {
					break
				}
				shift += 7
			}
			if _, err := br.Discard(n); err != nil {
				errCh <- err
				return
			}
		}
	}()

	var wmu sync.Mutex
	send := func(p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := conn.Write(p)
		return err
	}
	pub := func(topic string, payload []byte) error { return send(buildPublish(topic, payload, true)) }

	if err := pub(topicAvailable, []byte("online")); err != nil {
		return err
	}
	if err := m.publishDiscovery(pub); err != nil {
		return err
	}
	m.setStatus(fmt.Sprintf("verbunden mit %s", addr))
	log.Printf("MQTT: Entitäten veröffentlicht (%d Stück)", len(entityDefs))

	var lastState string
	var lastSent time.Time
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case s := <-m.ch:
			st, attrs := stateJSON(s)
			key := string(st)
			if key == lastState && time.Since(lastSent) < 60*time.Second {
				continue
			}
			if err := pub(topicState, st); err != nil {
				return err
			}
			if err := pub(topicAttrs, attrs); err != nil {
				return err
			}
			lastState, lastSent = key, time.Now()
		case <-ping.C:
			if err := send([]byte{0xC0, 0x00}); err != nil {
				return err
			}
		case err := <-errCh:
			if err == nil {
				err = errors.New("Verbindung beendet")
			}
			return err
		}
	}
}

func stateJSON(s Snapshot) ([]byte, []byte) {
	st := map[string]any{
		"online": s.Online, "players_online": s.PlayersOnline, "latency_ms": s.LatencyMs,
		"tps": s.TPS, "mspt": s.MSPT, "mspt_p95": s.MSPTP95,
		"disk_free_gb": s.DiskFreeGB, "whitelist_count": s.WhitelistCount, "whitelist_enabled": s.WhitelistEnabled,
		"version": s.Version, "motd": s.MOTD,
	}
	if s.Online {
		st["players_max"] = s.PlayersMax
	} else {
		st["players_max"] = nil
		st["version"], st["motd"] = nil, nil
	}
	if s.Addon != nil {
		st["addon_cpu"], st["addon_memory_percent"], st["addon_memory_mb"] = s.Addon.CPU, s.Addon.MemPercent, s.Addon.MemUsedMB
	} else {
		st["addon_cpu"], st["addon_memory_percent"], st["addon_memory_mb"] = nil, nil, nil
	}
	players := s.Players
	if players == nil {
		players = []string{}
	}
	a, _ := json.Marshal(map[string]any{"players": players})
	b, _ := json.Marshal(st)
	return b, a
}

func (m *MQTTPublisher) deviceInfo() map[string]any {
	d := map[string]any{
		"identifiers": []string{mqttNode}, "name": "HAMC Minecraft Server",
		"manufacturer": "kingjeykey", "model": "HAMC Server Java",
	}
	if info, err := m.app.sup.Info(); err == nil && info.Version != "" {
		d["sw_version"] = info.Version
	}
	return d
}

func (m *MQTTPublisher) publishDiscovery(pub func(string, []byte) error) error {
	dev := m.deviceInfo()
	for _, e := range entityDefs {
		cfg := map[string]any{
			"name": e.name, "unique_id": "hamc_java_" + e.key, "has_entity_name": true,
			"state_topic": topicState, "availability_topic": topicAvailable,
			"payload_available": "online", "payload_not_available": "offline", "device": dev,
		}
		if e.comp == "binary_sensor" {
			cfg["value_template"] = "{{ 'ON' if value_json." + e.key + " else 'OFF' }}"
			cfg["payload_on"], cfg["payload_off"] = "ON", "OFF"
		} else {
			cfg["value_template"] = "{{ value_json." + e.key + " }}"
		}
		if e.unit != "" {
			cfg["unit_of_measurement"] = e.unit
		}
		if e.devClass != "" {
			cfg["device_class"] = e.devClass
		}
		if e.stateClass != "" {
			cfg["state_class"] = e.stateClass
		}
		if e.icon != "" {
			cfg["icon"] = e.icon
		}
		if e.category != "" {
			cfg["entity_category"] = e.category
		}
		if e.attrs {
			cfg["json_attributes_topic"] = topicAttrs
		}
		b, _ := json.Marshal(cfg)
		if err := pub(fmt.Sprintf("%s/%s/%s/%s/config", mqttPrefix, e.comp, mqttNode, e.key), b); err != nil {
			return err
		}
	}
	return nil
}
