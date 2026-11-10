// hamc-ui: Web-Oberfläche (Ingress) für das HAMC-Minecraft-Add-on.
// Übersicht, Konsole, Whitelist, Dateien und MQTT-Entitäten laufen in diesem einen Programm.
package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var webFS embed.FS

type Config struct {
	DataDir       string
	Listen        string
	MCAddr        string
	RconAddr      string
	OptionsPaths  []string
	RconPassFile  string
	SupervisorURL string
	AllowAll      bool
	UID, GID      int
}

type App struct {
	cfg  Config
	opts map[string]any
	root string
	sup  *Supervisor
	rcon *RCON
	col  *Collector
	jobs *Jobs
	wl   *WhitelistSync
	mq   *MQTTPublisher
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return d
}

func loadConfig() Config {
	return Config{
		DataDir:       envOr("HAMC_DATA_DIR", "/data"),
		Listen:        envOr("HAMC_LISTEN", ":8099"),
		MCAddr:        envOr("HAMC_MC_ADDR", "127.0.0.1:25565"),
		RconAddr:      envOr("HAMC_RCON_ADDR", "127.0.0.1:25575"),
		OptionsPaths:  strings.Split(envOr("HAMC_OPTIONS", "/hassio_data/options.json,/data/options.json"), ","),
		RconPassFile:  envOr("HAMC_RCON_PASS_FILE", "/hassio_data/rcon_password"),
		SupervisorURL: envOr("HAMC_SUPERVISOR_URL", "http://supervisor"),
		AllowAll:      os.Getenv("HAMC_ALLOW_ALL") == "1",
		UID:           envInt("UID", 1000),
		GID:           envInt("GID", 1000),
	}
}

func readOptions(paths []string) map[string]any {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			m := map[string]any{}
			if json.Unmarshal(b, &m) == nil {
				return m
			}
		}
	}
	return map[string]any{}
}

func newApp(cfg Config) *App {
	a := &App{cfg: cfg, jobs: &Jobs{}}
	a.opts = readOptions(cfg.OptionsPaths)
	if r, err := filepath.EvalSymlinks(cfg.DataDir); err == nil {
		a.root = r
	} else {
		a.root = filepath.Clean(cfg.DataDir)
	}
	a.sup = newSupervisor(cfg.SupervisorURL)
	a.rcon = newRCON(cfg.RconAddr, a.rconPassword)
	a.col = newCollector(a)
	a.wl = newWhitelistSync(a)
	a.mq = newMQTT(a)
	return a
}

func (a *App) rconPassword() string {
	if v, ok := a.opts["RCON_PASSWORD"].(string); ok && v != "" {
		return v
	}
	if b, err := os.ReadFile(a.cfg.RconPassFile); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httpErr(w, 400, "Ungültige Anfrage")
		return false
	}
	return true
}

func (a *App) apiStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.col.Snapshot())
}

func (a *App) apiHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.col.History())
}

func (a *App) apiJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.jobs.Snapshot())
}

func (a *App) apiDiag(w http.ResponseWriter, r *http.Request) {
	d := a.col.Diag()
	d["supervisor_token"] = a.sup.Available()
	d["mqtt"] = a.mq.Status()
	d["whitelist_sync"] = a.wl.Status()
	d["rcon_password_set"] = a.rconPassword() != ""
	d["data_dir"] = a.root
	writeJSON(w, 200, d)
}

func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.cfg.AllowAll {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			// Nur der Ingress-Proxy des Supervisors (und lokale Prozesse) dürfen zugreifen.
			if host != "172.30.32.2" && host != "127.0.0.1" && host != "::1" {
				httpErr(w, 403, "Zugriff nur über Home Assistant")
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "hamc" {
			httpErr(w, 403, "Ungültige Anfrage")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	route := func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != method {
				httpErr(w, 405, "Methode nicht erlaubt")
				return
			}
			h(w, r)
		})
	}
	get, post := http.MethodGet, http.MethodPost
	route(get, "/api/stats", a.apiStats)
	route(get, "/api/history", a.apiHistory)
	route(get, "/api/diag", a.apiDiag)
	route(get, "/api/jobs", a.apiJobs)
	route(get, "/api/console/stream", a.consoleStream)
	route(post, "/api/console/command", a.consoleCommand)
	route(get, "/api/whitelist", a.wlGet)
	route(post, "/api/whitelist/add", a.wlAdd)
	route(post, "/api/whitelist/remove", a.wlRemove)
	route(post, "/api/whitelist/toggle", a.wlToggle)
	route(post, "/api/whitelist/reload", a.wlReload)
	route(get, "/api/files/list", a.filesList)
	route(get, "/api/files/download", a.filesDownload)
	route(get, "/api/files/read", a.filesRead)
	route(post, "/api/files/upload", a.filesUpload)
	route(post, "/api/files/mkdir", a.filesMkdir)
	route(post, "/api/files/rename", a.filesRename)
	route(post, "/api/files/delete", a.filesDelete)
	route(post, "/api/files/write", a.filesWrite)
	route(post, "/api/files/unzip", a.filesUnzip)
	route(post, "/api/mrpack/inspect", a.mrInspect)
	route(post, "/api/mrpack/install", a.mrInstall)
	route(post, "/api/files/fetch", a.filesFetch)
	return a.guard(mux)
}

func main() {
	log.SetFlags(log.LstdFlags)
	cfg := loadConfig()
	app := newApp(cfg)
	log.Printf("hamc-ui startet auf %s (Datenordner %s, Supervisor-Token: %v)", cfg.Listen, app.root, app.sup.Available())
	go app.col.run()
	go app.wl.loop()
	go app.mq.run()
	srv := &http.Server{Addr: cfg.Listen, Handler: app.handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
