package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- Fake Minecraft-Server (Status-Ping) ----------

type fakeMC struct {
	ln   net.Listener
	json string
}

func newFakeMC(t *testing.T, statusJSON string) *fakeMC {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMC{ln: ln, json: statusJSON}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				if _, err := readPacket(br); err != nil { // Handshake
					return
				}
				if _, err := readPacket(br); err != nil { // Status-Request
					return
				}
				var b bytes.Buffer
				writeVarInt(&b, 0)
				writeVarInt(&b, int32(len(f.json)))
				b.WriteString(f.json)
				_ = sendPacket(c, b.Bytes())
				if p, err := readPacket(br); err == nil { // Ping -> Pong
					_ = sendPacket(c, p)
				}
			}()
		}
	}()
	return f
}
func (f *fakeMC) addr() string { return f.ln.Addr().String() }

// ---------- Fake RCON-Server ----------

type fakeRCON struct {
	ln    net.Listener
	pass  string
	mu    sync.Mutex
	cmds  []string
	resp  map[string]string
	onCmd func(string)
	conns int
}

func newFakeRCON(t *testing.T, pass string) *fakeRCON {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRCON{ln: ln, pass: pass, resp: map[string]string{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.handle(c)
		}
	}()
	return f
}

func (f *fakeRCON) handle(c net.Conn) {
	defer c.Close()
	for {
		id, typ, body, err := readRconPacket(c)
		if err != nil {
			return
		}
		switch typ {
		case 3:
			if body == f.pass {
				_ = writeRconPacket(c, id, 2, "")
			} else {
				_ = writeRconPacket(c, -1, 2, "")
			}
		case 2:
			f.mu.Lock()
			f.cmds = append(f.cmds, body)
			r, ok := f.resp[body]
			cb := f.onCmd
			f.mu.Unlock()
			if !ok {
				r = "Unknown or incomplete command, see below for error"
			}
			if cb != nil {
				cb(body)
			}
			if r == "" {
				_ = writeRconPacket(c, id, 0, "")
			}
			for len(r) > 0 { // Antworten > 4096 Bytes werden wie bei Minecraft aufgeteilt
				n := len(r)
				if n > 4096 {
					n = 4096
				}
				_ = writeRconPacket(c, id, 0, r[:n])
				r = r[n:]
			}
		case 0:
			_ = writeRconPacket(c, id, 0, "Unknown request 0")
		}
	}
}

func (f *fakeRCON) set(cmd, resp string) { f.mu.Lock(); f.resp[cmd] = resp; f.mu.Unlock() }
func (f *fakeRCON) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}
func (f *fakeRCON) addr() string { return f.ln.Addr().String() }

// ---------- Fake Supervisor ----------

type fakeSup struct {
	*httptest.Server
	mu      sync.Mutex
	options map[string]any
	posted  []map[string]any
	mqtt    map[string]any
}

func newFakeSup(t *testing.T, opts map[string]any) *fakeSup {
	f := &fakeSup{options: opts}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"result":"error","message":"Unauthorized"}`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		out := func(v any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "ok", "data": v})
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /addons/self/stats":
			out(map[string]any{"cpu_percent": 12.5, "memory_usage": 536870912, "memory_limit": 1073741824, "memory_percent": 50.0, "network_rx": 1048576, "network_tx": 2097152})
		case "GET /addons/self/info":
			cp := map[string]any{}
			for k, v := range f.options {
				cp[k] = v
			}
			out(map[string]any{"version": "1.5.0", "options": cp})
		case "POST /addons/self/options":
			var in struct {
				Options map[string]any `json:"options"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &in)
			f.posted = append(f.posted, in.Options)
			f.options = in.Options
			out(map[string]any{})
		case "GET /services/mqtt":
			if f.mqtt == nil {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"result":"error","message":"not found"}`))
				return
			}
			out(f.mqtt)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"result":"error","message":"unknown"}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSup) postedOptions() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.posted...)
}

// ---------- Fake MQTT-Broker ----------

type mqttMsg struct {
	topic   string
	payload []byte
	retain  bool
}

type fakeBroker struct {
	ln      net.Listener
	mu      sync.Mutex
	connect []byte
	msgs    []mqttMsg
	pings   int
}

func newFakeBroker(t *testing.T) *fakeBroker {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.handle(c)
		}
	}()
	return b
}

func (b *fakeBroker) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for {
		first, err := br.ReadByte()
		if err != nil {
			return
		}
		n, shift := 0, uint(0)
		for {
			x, err := br.ReadByte()
			if err != nil {
				return
			}
			n |= int(x&0x7F) << shift
			if x&0x80 == 0 {
				break
			}
			shift += 7
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		switch first >> 4 {
		case 1:
			b.mu.Lock()
			b.connect = body
			b.mu.Unlock()
			_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
		case 3:
			tl := int(binary.BigEndian.Uint16(body))
			b.mu.Lock()
			b.msgs = append(b.msgs, mqttMsg{string(body[2 : 2+tl]), append([]byte(nil), body[2+tl:]...), first&1 == 1})
			b.mu.Unlock()
		case 12:
			b.mu.Lock()
			b.pings++
			b.mu.Unlock()
			_, _ = c.Write([]byte{0xD0, 0x00})
		}
	}
}

func (b *fakeBroker) port() int { return b.ln.Addr().(*net.TCPAddr).Port }
func (b *fakeBroker) messages() []mqttMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]mqttMsg(nil), b.msgs...)
}
func (b *fakeBroker) find(topic string) *mqttMsg {
	for _, m := range b.messages() {
		if m.topic == topic {
			mm := m
			return &mm
		}
	}
	return nil
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Zeitüberschreitung: %s", what)
}

var _ = strings.Contains
