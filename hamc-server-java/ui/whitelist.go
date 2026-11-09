package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type wlEntry struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

var reWLName = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,32}$`)

func (a *App) readWhitelistFile() ([]wlEntry, error) {
	b, err := os.ReadFile(filepath.Join(a.root, "whitelist.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []wlEntry
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list, nil
}

// whitelistEnabled liest die Eigenschaft aus der server.properties (beide Schreibweisen).
func (a *App) whitelistEnabled() bool {
	f, err := os.Open(filepath.Join(a.root, "server.properties"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(l, "="); ok && (k == "white-list" || k == "whitelist") {
			return strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
	return false
}

func (a *App) wlGet(w http.ResponseWriter, r *http.Request) {
	list, err := a.readWhitelistFile()
	if err != nil {
		httpErr(w, 500, "whitelist.json nicht lesbar: "+err.Error())
		return
	}
	if list == nil {
		list = []wlEntry{}
	}
	s := a.col.Snapshot()
	writeJSON(w, 200, map[string]any{
		"enabled": a.whitelistEnabled(), "players": list, "server_online": s.Online,
		"rcon_ok": s.RconOK, "rcon_error": s.RconError, "sync": a.wl.Status(),
	})
}

func (a *App) wlCommand(w http.ResponseWriter, cmd string, sync bool) {
	out, err := a.rcon.Exec(cmd)
	if err != nil {
		httpErr(w, 502, rconErrText(err))
		return
	}
	resp := map[string]any{"output": strings.TrimSpace(stripCodes(out))}
	if sync {
		time.Sleep(500 * time.Millisecond)
		if err := a.wl.SyncNow(); err != nil {
			resp["sync_error"] = err.Error()
		}
	}
	writeJSON(w, 200, resp)
}

func (a *App) wlAdd(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if !readJSON(w, r, &in) {
		return
	}
	n := strings.TrimSpace(in.Name)
	if !reWLName.MatchString(n) {
		httpErr(w, 400, "Ungültiger Spielername")
		return
	}
	a.wlCommand(w, "whitelist add "+n, true)
}

func (a *App) wlRemove(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if !readJSON(w, r, &in) {
		return
	}
	n := strings.TrimSpace(in.Name)
	if !reWLName.MatchString(n) {
		httpErr(w, 400, "Ungültiger Spielername")
		return
	}
	a.wlCommand(w, "whitelist remove "+n, true)
}

func (a *App) wlToggle(w http.ResponseWriter, r *http.Request) {
	var in struct{ On bool }
	if !readJSON(w, r, &in) {
		return
	}
	if in.On {
		a.wlCommand(w, "whitelist on", false)
	} else {
		a.wlCommand(w, "whitelist off", false)
	}
}

func (a *App) wlReload(w http.ResponseWriter, r *http.Request) {
	a.wlCommand(w, "whitelist reload", true)
}

// WhitelistSync hält die Option WHITELIST der Add-on-Konfiguration mit der
// whitelist.json des Servers synchron. Grund: Das itzg-Image fügt die Namen aus
// WHITELIST bei jedem Start wieder zur Datei hinzu. Ohne Abgleich würde ein im
// Spiel oder im Reiter entfernter Spieler nach dem Neustart wieder auftauchen.
type WhitelistSync struct {
	app      *App
	mu       sync.Mutex
	base     string
	initDone bool
	status   string
	lastErr  string
	lastSync time.Time
	logged   string
}

func newWhitelistSync(a *App) *WhitelistSync { return &WhitelistSync{app: a, status: "wartet"} }

func (w *WhitelistSync) Status() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := map[string]any{"status": w.status, "error": w.lastErr}
	if !w.lastSync.IsZero() {
		m["last_sync"] = w.lastSync.Unix()
	}
	return m
}

func namesKey(list []wlEntry) string {
	n := make([]string, 0, len(list))
	for _, e := range list {
		n = append(n, strings.ToLower(e.Name))
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

func parseNames(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ';' })
	out := make([]string, 0, len(f))
	for _, x := range f {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	norm := func(x []string) string {
		y := make([]string, len(x))
		for i, s := range x {
			y[i] = strings.ToLower(s)
		}
		sort.Strings(y)
		return strings.Join(y, ",")
	}
	return norm(a) == norm(b)
}

func (w *WhitelistSync) SyncNow() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncLocked()
}

func (w *WhitelistSync) fail(err error) error {
	w.status, w.lastErr = "fehler", err.Error()
	if w.logged != w.lastErr {
		log.Printf("Whitelist-Abgleich mit der Konfiguration fehlgeschlagen: %v", err)
		w.logged = w.lastErr
	}
	return err
}

func (w *WhitelistSync) syncLocked() error {
	a := w.app
	if !a.sup.Available() {
		return w.fail(errors.New("kein Supervisor-Zugriff (hassio_api)"))
	}
	list, err := a.readWhitelistFile()
	if err != nil {
		return w.fail(err)
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name)
	}
	info, err := a.sup.Info()
	if err != nil {
		return w.fail(err)
	}
	if info.Options == nil {
		return w.fail(errors.New("Supervisor lieferte keine Optionen"))
	}
	// Schutz: Würden Passwörter maskiert geliefert, dürfen wir sie nicht zurückschreiben.
	for k, v := range info.Options {
		if s, ok := v.(string); ok && s != "" && strings.Trim(s, "*") == "" {
			return w.fail(fmt.Errorf("Option %s wird maskiert geliefert, Abgleich abgebrochen", k))
		}
	}
	cur, _ := info.Options["WHITELIST"].(string)
	if !sameSet(parseNames(cur), names) {
		info.Options["WHITELIST"] = strings.Join(names, ",")
		if err := a.sup.SetOptions(info.Options); err != nil {
			return w.fail(err)
		}
		log.Printf("Whitelist in die Konfiguration übernommen (%d Einträge)", len(names))
	}
	w.base, w.initDone = namesKey(list), true
	w.status, w.lastErr, w.lastSync, w.logged = "ok", "", time.Now(), ""
	return nil
}

// loop erkennt Änderungen an der whitelist.json (auch durch /whitelist im Spiel).
func (w *WhitelistSync) loop() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for range t.C {
		list, err := w.app.readWhitelistFile()
		if err != nil {
			continue
		}
		key := namesKey(list)
		w.mu.Lock()
		if !w.initDone {
			w.base, w.initDone = key, true
			if w.status == "wartet" {
				w.status = "bereit"
			}
			w.mu.Unlock()
			continue
		}
		changed := key != w.base
		w.mu.Unlock()
		if !changed || !w.app.col.IsOnline() {
			continue
		}
		w.mu.Lock()
		_ = w.syncLocked()
		w.mu.Unlock()
	}
}
