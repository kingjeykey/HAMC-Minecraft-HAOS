package main

import (
	"errors"
	"strings"
	"sync"
	"syscall"
	"time"
)

type AddonSnap struct {
	CPU        float64 `json:"cpu"`
	MemPercent float64 `json:"mem_percent"`
	MemUsedMB  float64 `json:"mem_used_mb"`
	MemLimitMB float64 `json:"mem_limit_mb"`
	NetRxMB    float64 `json:"net_rx_mb"`
	NetTxMB    float64 `json:"net_tx_mb"`
}

// Snapshot ist der gesamte Statuszustand für Oberfläche und MQTT.
type Snapshot struct {
	Time             time.Time  `json:"time"`
	Online           bool       `json:"online"`
	Version          string     `json:"version"`
	Protocol         int        `json:"protocol"`
	MOTD             string     `json:"motd"`
	PlayersOnline    int        `json:"players_online"`
	PlayersMax       int        `json:"players_max"`
	Players          []string   `json:"players"`
	LatencyMs        *float64   `json:"latency_ms"`
	TPS              *float64   `json:"tps"`
	MSPT             *float64   `json:"mspt"`
	MSPTP95          *float64   `json:"mspt_p95"`
	TickMethod       string     `json:"tick_method"`
	Addon            *AddonSnap `json:"addon"`
	DiskFreeGB       float64    `json:"disk_free_gb"`
	DiskTotalGB      float64    `json:"disk_total_gb"`
	WhitelistCount   int        `json:"whitelist_count"`
	WhitelistEnabled bool       `json:"whitelist_enabled"`
	RconOK           bool       `json:"rcon_ok"`
	RconError        string     `json:"rcon_error"`
}

type Point struct {
	T       int64    `json:"t"`
	Players int      `json:"players"`
	TPS     *float64 `json:"tps"`
	MSPT    *float64 `json:"mspt"`
	CPU     *float64 `json:"cpu"`
	Mem     *float64 `json:"mem"`
}

const historyMax = 360 // 1 Stunde bei 10 s

var tickCandidates = []string{"tick query", "tps", "neoforge tps", "forge tps"}

// Collector sammelt regelmäßig alle Werte. RCON-Abfragen laufen seltener als der Ping.
type Collector struct {
	app *App
	mu  sync.RWMutex

	snap Snapshot
	hist []Point
	diag map[string]string

	wasOnline  bool
	rconOK     bool
	rconErr    string
	players    []string
	tick       *TickInfo
	tickMethod string
	hasMSPT    bool
	tickFails  int
	lastDetect time.Time
	detectErr  bool
	supErr     string
	lastPing   string
}

func newCollector(a *App) *Collector {
	return &Collector{app: a, diag: map[string]string{}}
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

func (c *Collector) History() []Point {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Point(nil), c.hist...)
}

func (c *Collector) IsOnline() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap.Online
}

func (c *Collector) Diag() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	raw := map[string]string{}
	for k, v := range c.diag {
		raw[k] = v
	}
	return map[string]any{
		"tick_method":    c.tickMethod,
		"has_mspt":       c.hasMSPT,
		"tick_raw":       raw,
		"supervisor_err": c.supErr,
		"ping_error":     c.lastPing,
		"rcon_ok":        c.rconOK,
		"rcon_error":     c.rconErr,
	}
}

func (c *Collector) run() {
	c.collect(true)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	n := 0
	for range t.C {
		n++
		c.collect(n%3 == 0)
	}
}

func rconErrText(err error) string {
	switch {
	case errors.Is(err, ErrAuth):
		return "RCON-Passwort wird vom Server abgelehnt"
	case strings.Contains(err.Error(), "kein RCON-Passwort"):
		return "Kein RCON-Passwort gesetzt (RCON_PASSWORD in der Konfiguration)"
	default:
		return "RCON nicht erreichbar: " + err.Error()
	}
}

func (c *Collector) collect(forceRcon bool) {
	a := c.app
	s := Snapshot{Time: time.Now()}
	pr, perr := pingServer(a.cfg.MCAddr, 4*time.Second)

	c.mu.Lock()
	if perr == nil {
		c.lastPing = ""
	} else {
		c.lastPing = perr.Error()
	}
	c.mu.Unlock()

	if perr == nil {
		s.Online = true
		s.Version, s.Protocol, s.MOTD = pr.VersionName, pr.Protocol, pr.MOTD
		s.PlayersOnline, s.PlayersMax = pr.Online, pr.Max
		s.Players = pr.Sample
		s.LatencyMs = fp(round2(pr.LatencyMs))
	}

	c.mu.Lock()
	prev := c.wasOnline
	c.wasOnline = s.Online
	c.mu.Unlock()

	if s.Online {
		if forceRcon || !prev {
			c.pollRcon()
		}
	} else {
		c.mu.Lock()
		c.rconOK, c.rconErr, c.players, c.tick, c.tickMethod, c.hasMSPT = false, "", nil, nil, "", false
		c.lastDetect = time.Time{}
		c.mu.Unlock()
	}

	c.mu.Lock()
	if s.Online {
		if len(c.players) > 0 || (c.rconOK && s.PlayersOnline == 0) {
			s.Players = c.players
		}
		if c.tick != nil {
			s.TPS, s.MSPT, s.MSPTP95 = c.tick.TPS, c.tick.MSPT, c.tick.P95
		}
	}
	s.TickMethod = c.tickMethod
	s.RconOK, s.RconError = c.rconOK, c.rconErr
	c.mu.Unlock()

	if a.sup.Available() {
		st, err := a.sup.Stats()
		c.mu.Lock()
		if err == nil {
			c.supErr = ""
			s.Addon = &AddonSnap{
				CPU: round2(st.CPUPercent), MemPercent: round2(st.MemoryPercent),
				MemUsedMB: round2(st.MemoryUsage / 1048576), MemLimitMB: round2(st.MemoryLimit / 1048576),
				NetRxMB: round2(st.NetworkRx / 1048576), NetTxMB: round2(st.NetworkTx / 1048576),
			}
		} else {
			c.supErr = err.Error()
		}
		c.mu.Unlock()
	}

	var fs syscall.Statfs_t
	if syscall.Statfs(a.cfg.DataDir, &fs) == nil {
		s.DiskFreeGB = round2(float64(fs.Bavail) * float64(fs.Bsize) / 1073741824)
		s.DiskTotalGB = round2(float64(fs.Blocks) * float64(fs.Bsize) / 1073741824)
	}
	if list, err := a.readWhitelistFile(); err == nil {
		s.WhitelistCount = len(list)
	}
	s.WhitelistEnabled = a.whitelistEnabled()

	pt := Point{T: s.Time.Unix(), Players: s.PlayersOnline, TPS: s.TPS, MSPT: s.MSPT}
	if s.Addon != nil {
		pt.CPU, pt.Mem = fp(s.Addon.CPU), fp(s.Addon.MemPercent)
	}
	c.mu.Lock()
	c.snap = s
	c.hist = append(c.hist, pt)
	if len(c.hist) > historyMax {
		c.hist = c.hist[len(c.hist)-historyMax:]
	}
	c.mu.Unlock()

	if a.mq != nil {
		a.mq.Push(s)
	}
}

func (c *Collector) setDiag(cmd, out string) {
	if len(out) > 1200 {
		out = out[:1200] + " …"
	}
	c.mu.Lock()
	c.diag[cmd] = out
	c.mu.Unlock()
}

func (c *Collector) pollRcon() {
	out, err := c.app.rcon.Exec("list")
	c.mu.Lock()
	if err != nil {
		c.rconOK, c.rconErr, c.players = false, rconErrText(err), nil
		c.mu.Unlock()
		return
	}
	c.rconOK, c.rconErr = true, ""
	if _, _, names, ok := parsePlayerList(out); ok {
		c.players = names
	}
	c.mu.Unlock()
	c.pollTick()
}

func (c *Collector) runTick(method string) (*TickInfo, bool) {
	out, err := c.app.rcon.Exec(method)
	if err != nil {
		// Verbindungsfehler: Erkennung bald wiederholen statt 5 Minuten zu warten.
		c.mu.Lock()
		c.detectErr = true
		c.mu.Unlock()
		return nil, false
	}
	c.setDiag(method, out)
	if isUnknownCommand(out) {
		return nil, false
	}
	var t *TickInfo
	var ok bool
	switch method {
	case "tick query":
		t, ok = parseTickQuery(out)
	case "tps":
		t, ok = parsePaperTPS(out)
	default:
		t, ok = parseForge(out)
	}
	return t, ok
}

func (c *Collector) addMSPT(t *TickInfo) {
	out, err := c.app.rcon.Exec("mspt")
	if err != nil {
		return
	}
	c.setDiag("mspt", out)
	if isUnknownCommand(out) {
		return
	}
	if v, ok := parseMSPT(out); ok {
		t.MSPT = v
		c.mu.Lock()
		c.hasMSPT = true
		c.mu.Unlock()
	}
}

func (c *Collector) pollTick() {
	c.mu.RLock()
	method, last := c.tickMethod, c.lastDetect
	c.mu.RUnlock()

	if method == "" {
		if !last.IsZero() && time.Since(last) < 5*time.Minute {
			return
		}
		c.mu.Lock()
		c.lastDetect = time.Now()
		c.detectErr = false
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			if c.detectErr {
				c.lastDetect = time.Time{}
			}
			c.mu.Unlock()
		}()
		for _, cand := range tickCandidates {
			if t, ok := c.runTick(cand); ok {
				if cand == "tps" {
					c.addMSPT(t)
				}
				c.mu.Lock()
				c.tick, c.tickMethod, c.tickFails = t, cand, 0
				c.mu.Unlock()
				return
			}
		}
		return
	}

	t, ok := c.runTick(method)
	if ok && method == "tps" {
		c.mu.RLock()
		has := c.hasMSPT
		c.mu.RUnlock()
		if has {
			c.addMSPT(t)
		}
	}
	c.mu.Lock()
	if ok {
		c.tick, c.tickFails = t, 0
	} else if c.tickFails++; c.tickFails >= 3 {
		c.tick, c.tickMethod, c.hasMSPT, c.lastDetect = nil, "", false, time.Time{}
	}
	c.mu.Unlock()
}
