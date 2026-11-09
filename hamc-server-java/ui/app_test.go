package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type env struct {
	t      *testing.T
	app    *App
	srv    *httptest.Server
	dir    string
	mc     *fakeMC
	rcon   *fakeRCON
	sup    *fakeSup
	broker *fakeBroker
}

const statusJSON = `{"version":{"name":"1.21.4","protocol":769},"players":{"max":20,"online":1,"sample":[{"name":"Alice","id":"u"}]},"description":"Test-Server"}`

func newEnv(t *testing.T, extraOpts map[string]any) *env {
	t.Helper()
	t.Setenv("SUPERVISOR_TOKEN", "testtoken")
	dir := t.TempDir()
	e := &env{t: t, dir: dir}
	e.mc = newFakeMC(t, statusJSON)
	e.rcon = newFakeRCON(t, "pw")
	e.broker = newFakeBroker(t)
	opts := map[string]any{"RCON_PASSWORD": "pw", "WHITELIST": "Alice", "PLAYIT_SECRET": "geheim",
		"MQTT_HOST": "127.0.0.1", "MQTT_PORT": float64(e.broker.port())}
	for k, v := range extraOpts {
		opts[k] = v
	}
	e.sup = newFakeSup(t, opts)

	optsFile := filepath.Join(t.TempDir(), "options.json")
	b, _ := json.Marshal(opts)
	_ = os.WriteFile(optsFile, b, 0o644)

	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "world"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "logs"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "world", "level.dat"), []byte("LEVEL"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "world", "region.mca"), bytes.Repeat([]byte("x"), 5000), 0o644))
	must(os.WriteFile(filepath.Join(dir, "logs", "latest.log"), []byte("[12:00:00] [Server thread/INFO]: Zeile1\n[12:00:01] [Server thread/INFO]: Zeile2\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "server.properties"), []byte("motd=Hi\nwhite-list=true\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "whitelist.json"), []byte(`[{"uuid":"1","name":"Alice"}]`), 0o644))

	cfg := Config{DataDir: dir, Listen: "127.0.0.1:0", MCAddr: e.mc.addr(), RconAddr: e.rcon.addr(),
		OptionsPaths: []string{optsFile}, RconPassFile: filepath.Join(dir, "none"), SupervisorURL: e.sup.URL,
		AllowAll: true, UID: os.Getuid(), GID: os.Getgid()}
	e.app = newApp(cfg)
	e.srv = httptest.NewServer(e.app.handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) req(method, p string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, e.srv.URL+p, rd)
	r.Header.Set("X-Requested-With", "hamc")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func TestGuardCSRF(t *testing.T) {
	e := newEnv(t, nil)
	r, _ := http.NewRequest("POST", e.srv.URL+"/api/console/command", strings.NewReader(`{"cmd":"list"}`))
	resp, _ := http.DefaultClient.Do(r) // ohne X-Requested-With
	if resp.StatusCode != 403 {
		t.Fatalf("POST ohne Header muss 403 liefern, war %d", resp.StatusCode)
	}
	// Zugriff von fremder Adresse (nicht Supervisor) wird abgelehnt
	e.app.cfg.AllowAll = false
	rr := httptest.NewRecorder()
	rq := httptest.NewRequest("GET", "/api/stats", nil)
	rq.RemoteAddr = "192.168.1.50:5555"
	e.app.handler().ServeHTTP(rr, rq)
	if rr.Code != 403 {
		t.Fatalf("fremde IP muss 403 liefern, war %d", rr.Code)
	}
	rq.RemoteAddr = "172.30.32.2:5555"
	rr = httptest.NewRecorder()
	e.app.handler().ServeHTTP(rr, rq)
	if rr.Code != 200 {
		t.Fatalf("Supervisor-IP muss erlaubt sein, war %d", rr.Code)
	}
}

func TestStatsCollect(t *testing.T) {
	e := newEnv(t, nil)
	e.rcon.set("list", "There are 1 of a max of 20 players online: Alice")
	e.rcon.set("tick query", "Target tick rate: 20.0 per second.\nAverage time per tick: 4.2ms (Target: 50.0ms)\nPercentiles: P50: 3.9ms P95: 7.1ms P99: 9.8ms, based on 100 ticks")
	e.app.col.collect(true)
	s := e.app.col.Snapshot()
	if !s.Online || s.PlayersOnline != 1 || s.PlayersMax != 20 || len(s.Players) != 1 || s.Players[0] != "Alice" {
		t.Fatalf("Spieler: %+v", s)
	}
	if s.Version != "1.21.4" || s.MOTD != "Test-Server" || !near(s.TPS, 20) || !near(s.MSPT, 4.2) || !near(s.MSPTP95, 7.1) || s.TickMethod != "tick query" {
		t.Fatalf("Tick: %+v", s)
	}
	if s.Addon == nil || s.Addon.CPU != 12.5 || s.Addon.MemUsedMB != 512 || !s.RconOK || s.WhitelistCount != 1 || !s.WhitelistEnabled || s.DiskTotalGB <= 0 {
		t.Fatalf("Rest: %+v addon=%+v", s, s.Addon)
	}
	code, m := e.req("GET", "/api/stats", nil)
	if code != 200 || m["players_online"].(float64) != 1 {
		t.Fatalf("API: %d %v", code, m)
	}
	if len(e.app.col.History()) != 1 {
		t.Fatal("Verlauf fehlt")
	}
}

func TestTickFallbackPaper(t *testing.T) {
	e := newEnv(t, nil)
	e.rcon.set("list", "There are 0 of a max of 20 players online:")
	e.rcon.set("tps", "§6TPS from last 1m, 5m, 15m: §a*20.0, §a19.8, §a19.5")
	e.rcon.set("mspt", "Server tick times (avg/min/max) from last 5s, 10s, 1m: ◴ 12.3/4.1/30.2, 11.0/3.0/25.0")
	e.app.col.collect(true)
	s := e.app.col.Snapshot()
	if s.TickMethod != "tps" || !near(s.TPS, 20) || !near(s.MSPT, 12.3) {
		t.Fatalf("Paper-Fallback: %+v", s)
	}
	// Server ohne jede Tick-Unterstützung: kein Fehler, nur keine Werte
	e2 := newEnv(t, nil)
	e2.rcon.set("list", "There are 0 of a max of 20 players online:")
	e2.app.col.collect(true)
	s2 := e2.app.col.Snapshot()
	if !s2.Online || s2.TPS != nil || s2.TickMethod != "" || !s2.RconOK {
		t.Fatalf("ohne Tick: %+v", s2)
	}
}

func TestRconProblemsReported(t *testing.T) {
	e := newEnv(t, map[string]any{"RCON_PASSWORD": "falsch"})
	e.app.col.collect(true)
	s := e.app.col.Snapshot()
	if !s.Online || s.RconOK || !strings.Contains(s.RconError, "abgelehnt") {
		t.Fatalf("%+v", s)
	}
	code, m := e.req("POST", "/api/console/command", map[string]any{"cmd": "list"})
	if code != 502 || !strings.Contains(m["error"].(string), "abgelehnt") {
		t.Fatalf("%d %v", code, m)
	}
}

func TestFilesAPI(t *testing.T) {
	e := newEnv(t, nil)
	code, m := e.req("GET", "/api/files/list?path=/", nil)
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	var sawWorld, sawEscape bool
	for _, x := range m["entries"].([]any) {
		en := x.(map[string]any)
		if en["name"] == "world" && en["world"] == true {
			sawWorld = true
		}
		if en["name"] == "escape" {
			sawEscape = true
		}
	}
	if !sawWorld {
		t.Fatal("Welt nicht erkannt")
	}
	_ = sawEscape

	// Pfad-Ausbruch: ".." wird an der Wurzel abgeschnitten, Symlinks nach außen werden abgelehnt
	if code, _ := e.req("GET", "/api/files/read?path=../../../../etc/passwd", nil); code != 404 {
		t.Fatalf("Traversal muss 404 liefern, war %d", code)
	}
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "geheim.txt"), []byte("topsecret"), 0o644)
	if err := os.Symlink(outside, filepath.Join(e.dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.req("GET", "/api/files/list?path=escape", nil); code != 400 {
		t.Fatalf("Symlink nach außen muss abgelehnt werden, war %d", code)
	}
	if code, _ := e.req("GET", "/api/files/read?path=escape/geheim.txt", nil); code != 400 {
		t.Fatalf("Datei hinter Symlink muss abgelehnt werden, war %d", code)
	}
	code, m = e.req("GET", "/api/files/list?path=/", nil)
	for _, x := range m["entries"].([]any) {
		if x.(map[string]any)["name"] == "escape" {
			t.Fatal("Symlink nach außen darf nicht gelistet werden")
		}
	}

	// mkdir, Upload (mehrere Dateien), lesen, schreiben, umbenennen, löschen
	if code, m := e.req("POST", "/api/files/mkdir", map[string]any{"path": "/", "name": "plugins"}); code != 200 {
		t.Fatalf("mkdir %d %v", code, m)
	}
	if code, _ := e.req("POST", "/api/files/mkdir", map[string]any{"path": "/", "name": "../x"}); code != 400 {
		t.Fatal("ungültiger Ordnername muss abgelehnt werden")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "Essentials.jar")
	_, _ = fw.Write([]byte("JARDATA"))
	fw2, _ := mw.CreateFormFile("files", "../../evil.txt") // Pfadanteile werden entfernt
	_, _ = fw2.Write([]byte("EVIL"))
	mw.Close()
	r, _ := http.NewRequest("POST", e.srv.URL+"/api/files/upload?path=/plugins", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-Requested-With", "hamc")
	resp, err := http.DefaultClient.Do(r)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("Upload: %v %v", err, resp)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "plugins", "Essentials.jar")); string(b) != "JARDATA" {
		t.Fatal("Upload-Inhalt falsch")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "plugins", "evil.txt")); err != nil {
		t.Fatal("evil.txt muss unschädlich IM Zielordner landen")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.dir), "evil.txt")); err == nil {
		t.Fatal("Datei außerhalb des Serverordners angelegt!")
	}
	matches, _ := filepath.Glob(filepath.Join(e.dir, "plugins", "*.hamc-part"))
	if len(matches) != 0 {
		t.Fatal("temporäre Upload-Datei übrig")
	}
	if os.Geteuid() == 0 {
		st, _ := os.Stat(filepath.Join(e.dir, "plugins", "Essentials.jar"))
		_ = st // Besitzer wird per Lchown gesetzt, hier UID des Testprozesses
	}

	if code, m := e.req("POST", "/api/files/write", map[string]any{"path": "/server.properties", "content": "motd=Neu\n"}); code != 200 {
		t.Fatalf("write %d %v", code, m)
	}
	if code, m := e.req("GET", "/api/files/read?path=/server.properties", nil); code != 200 || m["content"] != "motd=Neu\n" {
		t.Fatalf("read %d %v", code, m)
	}
	if code, _ := e.req("GET", "/api/files/read?path=/world/level.dat", nil); code != 200 {
		t.Fatal("Textdatei lesbar")
	}
	_ = os.WriteFile(filepath.Join(e.dir, "bin.dat"), []byte{1, 2, 0, 3}, 0o644)
	if code, _ := e.req("GET", "/api/files/read?path=/bin.dat", nil); code != 415 {
		t.Fatalf("Binärdatei muss 415 liefern, war %d", code)
	}
	if code, _ := e.req("POST", "/api/files/rename", map[string]any{"path": "/plugins/evil.txt", "name": "ok.txt"}); code != 200 {
		t.Fatal("rename")
	}
	if code, _ := e.req("POST", "/api/files/rename", map[string]any{"path": "/plugins/ok.txt", "name": "../ausbruch.txt"}); code != 400 {
		t.Fatal("rename mit Pfad muss abgelehnt werden")
	}
	if code, _ := e.req("POST", "/api/files/delete", map[string]any{"path": "/plugins/ok.txt"}); code != 200 {
		t.Fatal("delete")
	}
	if code, _ := e.req("POST", "/api/files/delete", map[string]any{"path": "/"}); code != 400 {
		t.Fatal("Wurzel darf nicht gelöscht werden")
	}
	if code, _ := e.req("POST", "/api/files/delete", map[string]any{"path": "../.."}); code != 400 {
		t.Fatal("Wurzel über .. darf nicht gelöscht werden")
	}

	// Einzeldatei-Download inkl. Range
	r, _ = http.NewRequest("GET", e.srv.URL+"/api/files/download?path=/plugins/Essentials.jar", nil)
	r.Header.Set("Range", "bytes=0-2")
	resp, _ = http.DefaultClient.Do(r)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || string(body) != "JAR" || !strings.Contains(resp.Header.Get("Content-Disposition"), "Essentials.jar") {
		t.Fatalf("Download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
}

func TestWorldDownloadSavesSafely(t *testing.T) {
	e := newEnv(t, nil)
	e.rcon.set("save-off", "Automatic saving is now disabled")
	e.rcon.set("save-all flush", "Saved the game")
	e.rcon.set("save-on", "Automatic saving is now enabled")
	e.rcon.set("list", "There are 0 of a max of 20 players online:")
	e.app.col.collect(true) // Server als online markieren

	resp, err := http.Get(e.srv.URL + "/api/files/download?path=/world")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("keine gültige ZIP: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["world/level.dat"] || !names["world/region.mca"] {
		t.Fatalf("ZIP-Inhalt: %v", names)
	}
	cmds := e.rcon.commands()
	var seq []string
	for _, c := range cmds {
		if strings.HasPrefix(c, "save-") {
			seq = append(seq, c)
		}
	}
	if strings.Join(seq, "|") != "save-off|save-all flush|save-on" {
		t.Fatalf("Speicher-Reihenfolge falsch: %v", seq)
	}
}

func TestUnzipAndFetch(t *testing.T) {
	e := newEnv(t, nil)
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, content := range map[string]string{"karte/level.dat": "L", "karte/data/a.txt": "A", "../evil.txt": "EVIL", "/abs/evil2.txt": "EVIL2"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	zw.Close()
	_ = os.WriteFile(filepath.Join(e.dir, "karte.zip"), zb.Bytes(), 0o644)
	code, m := e.req("POST", "/api/files/unzip", map[string]any{"path": "/karte.zip"})
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	waitFor(t, 5*time.Second, "Entpacken", func() bool {
		_, jm := e.req("GET", "/api/jobs", nil)
		_ = jm
		resp, _ := http.Get(e.srv.URL + "/api/jobs")
		var jobs []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&jobs)
		resp.Body.Close()
		return len(jobs) > 0 && jobs[0]["status"] == "done"
	})
	if b, _ := os.ReadFile(filepath.Join(e.dir, "karte", "karte", "level.dat")); string(b) != "L" {
		t.Fatal("entpackte Datei fehlt")
	}
	for _, p := range []string{filepath.Join(filepath.Dir(e.dir), "evil.txt"), filepath.Join(e.dir, "evil.txt"), "/abs/evil2.txt"} {
		if _, err := os.Stat(p); err == nil && !strings.HasPrefix(p, filepath.Join(e.dir, "karte")) {
			t.Fatalf("Zip-Slip: %s wurde außerhalb angelegt", p)
		}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "karte", "evil.txt")); err != nil {
		t.Fatal("../evil.txt muss entschärft INNERHALB des Zielordners landen")
	}

	// Download von URL
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("PLUGINBYTES")) }))
	defer ts.Close()
	if code, _ := e.req("POST", "/api/files/fetch", map[string]any{"path": "/", "url": "file:///etc/passwd"}); code != 400 {
		t.Fatal("file:// muss abgelehnt werden")
	}
	if code, m := e.req("POST", "/api/files/fetch", map[string]any{"path": "/", "url": ts.URL + "/mein-plugin.jar"}); code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	waitFor(t, 5*time.Second, "Download", func() bool {
		b, err := os.ReadFile(filepath.Join(e.dir, "mein-plugin.jar"))
		return err == nil && string(b) == "PLUGINBYTES"
	})
}

func TestConsole(t *testing.T) {
	e := newEnv(t, nil)
	e.rcon.set("say hallo", "")
	e.rcon.set("list", "§6There are 0 of a max of 20 players online:")
	code, m := e.req("POST", "/api/console/command", map[string]any{"cmd": "/list"})
	if code != 200 || m["output"] != "There are 0 of a max of 20 players online:" {
		t.Fatalf("%d %v", code, m)
	}
	if code, _ := e.req("POST", "/api/console/command", map[string]any{"cmd": "list\nstop"}); code != 400 {
		t.Fatal("mehrzeilige Befehle müssen abgelehnt werden")
	}

	resp, err := http.Get(e.srv.URL + "/api/console/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("kein SSE")
	}
	br := bufio.NewReader(resp.Body)
	lines := make(chan string, 20)
	go func() {
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "data: ") {
				lines <- strings.TrimSpace(strings.TrimPrefix(l, "data: "))
			}
		}
	}()
	expect := func(sub string) {
		t.Helper()
		for {
			select {
			case l := <-lines:
				if strings.Contains(l, sub) {
					return
				}
			case <-time.After(4 * time.Second):
				t.Fatalf("Zeile mit %q nicht empfangen", sub)
			}
		}
	}
	expect("Zeile1")
	expect("Zeile2")
	f, _ := os.OpenFile(filepath.Join(e.dir, "logs", "latest.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("[12:00:05] [Server thread/INFO]: Alice joined the game\n")
	f.Close()
	expect("Alice joined")
	// Log-Rotation: Datei wird ersetzt
	_ = os.Remove(filepath.Join(e.dir, "logs", "latest.log"))
	_ = os.WriteFile(filepath.Join(e.dir, "logs", "latest.log"), []byte("[13:00:00] [main/INFO]: Neustart\n"), 0o644)
	expect("Neustart")
}

func TestWhitelistFlowAndConfigSync(t *testing.T) {
	e := newEnv(t, nil)
	wlFile := filepath.Join(e.dir, "whitelist.json")
	e.rcon.mu.Lock()
	e.rcon.onCmd = func(cmd string) { // simuliert, dass der Server whitelist.json schreibt
		switch cmd {
		case "whitelist add Bob":
			_ = os.WriteFile(wlFile, []byte(`[{"uuid":"1","name":"Alice"},{"uuid":"2","name":"Bob"}]`), 0o644)
		case "whitelist remove Alice":
			_ = os.WriteFile(wlFile, []byte(`[{"uuid":"2","name":"Bob"}]`), 0o644)
		}
	}
	e.rcon.mu.Unlock()
	e.rcon.set("whitelist add Bob", "Added Bob to the whitelist")
	e.rcon.set("whitelist remove Alice", "Removed Alice from the whitelist")

	code, m := e.req("GET", "/api/whitelist", nil)
	if code != 200 || m["enabled"] != true || len(m["players"].([]any)) != 1 {
		t.Fatalf("%d %v", code, m)
	}
	if code, _ := e.req("POST", "/api/whitelist/add", map[string]any{"name": "Bob; stop"}); code != 400 {
		t.Fatal("Namen mit Sonderzeichen müssen abgelehnt werden")
	}
	code, m = e.req("POST", "/api/whitelist/add", map[string]any{"name": "Bob"})
	if code != 200 || m["sync_error"] != nil {
		t.Fatalf("add: %d %v", code, m)
	}
	posted := e.sup.postedOptions()
	if len(posted) != 1 || posted[0]["WHITELIST"] != "Alice,Bob" {
		t.Fatalf("Config-Abgleich fehlt: %v", posted)
	}
	if posted[0]["RCON_PASSWORD"] != "pw" || posted[0]["PLAYIT_SECRET"] != "geheim" {
		t.Fatalf("andere Optionen müssen unverändert bleiben: %v", posted[0])
	}

	code, m = e.req("POST", "/api/whitelist/remove", map[string]any{"name": "Alice"})
	if code != 200 {
		t.Fatalf("remove: %d %v", code, m)
	}
	posted = e.sup.postedOptions()
	if len(posted) != 2 || posted[1]["WHITELIST"] != "Bob" {
		t.Fatalf("Entfernen muss in die Config übernommen werden: %v", posted)
	}
	// Schon gleich -> kein unnötiger Schreibzugriff
	if err := e.app.wl.SyncNow(); err != nil || len(e.sup.postedOptions()) != 2 {
		t.Fatal("unnötiger Schreibzugriff")
	}
}

func TestWhitelistSyncRefusesMaskedOptions(t *testing.T) {
	e := newEnv(t, map[string]any{"PLAYIT_SECRET": "********"})
	_ = os.WriteFile(filepath.Join(e.dir, "whitelist.json"), []byte(`[{"uuid":"1","name":"Alice"},{"uuid":"2","name":"Zed"}]`), 0o644)
	err := e.app.wl.SyncNow()
	if err == nil || !strings.Contains(err.Error(), "maskiert") {
		t.Fatalf("Abbruch erwartet, war: %v", err)
	}
	if len(e.sup.postedOptions()) != 0 {
		t.Fatal("bei maskierten Optionen darf nichts geschrieben werden")
	}
}

func TestMQTTDiscoveryAndState(t *testing.T) {
	e := newEnv(t, nil)
	e.rcon.set("list", "There are 1 of a max of 20 players online: Alice")
	e.rcon.set("tick query", "Target tick rate: 20.0 per second.\nAverage time per tick: 4.2ms (Target: 50.0ms)\nPercentiles: P50: 3.9ms P95: 7.1ms P99: 9.8ms")
	e.app.col.collect(true)
	go e.app.mq.run()

	waitFor(t, 5*time.Second, "Discovery", func() bool {
		return e.broker.find("homeassistant/sensor/hamc_java/tps/config") != nil &&
			e.broker.find("homeassistant/binary_sensor/hamc_java/online/config") != nil
	})
	e.broker.mu.Lock()
	c := e.broker.connect
	e.broker.mu.Unlock()
	if !bytes.Contains(c, []byte("MQTT")) || !bytes.Contains(c, []byte("hamc_java/availability")) || !bytes.Contains(c, []byte("offline")) {
		t.Fatal("CONNECT ohne Last-Will")
	}
	cfg := e.broker.find("homeassistant/sensor/hamc_java/tps/config")
	var m map[string]any
	if err := json.Unmarshal(cfg.payload, &m); err != nil || !cfg.retain {
		t.Fatalf("Discovery muss gültiges JSON und retained sein: %v retain=%v", err, cfg.retain)
	}
	if m["unique_id"] != "hamc_java_tps" || m["state_topic"] != "hamc_java/state" || m["value_template"] != "{{ value_json.tps }}" || m["has_entity_name"] != true {
		t.Fatalf("Discovery-Inhalt: %v", m)
	}
	if dev, ok := m["device"].(map[string]any); !ok || dev["name"] != "HAMC Minecraft Server" || dev["sw_version"] != "1.5.0" {
		t.Fatalf("Geräteinfo: %v", m["device"])
	}
	if av := e.broker.find("hamc_java/availability"); av == nil || string(av.payload) != "online" {
		t.Fatal("Verfügbarkeit nicht veröffentlicht")
	}
	e.app.mq.Push(e.app.col.Snapshot())
	waitFor(t, 5*time.Second, "State", func() bool { return e.broker.find("hamc_java/state") != nil })
	var st map[string]any
	_ = json.Unmarshal(e.broker.find("hamc_java/state").payload, &st)
	if st["online"] != true || st["players_online"].(float64) != 1 || st["tps"].(float64) != 20 || st["mspt"].(float64) != 4.2 || st["addon_cpu"].(float64) != 12.5 {
		t.Fatalf("State: %v", st)
	}
	var at map[string]any
	_ = json.Unmarshal(e.broker.find("hamc_java/attributes").payload, &at)
	if p := at["players"].([]any); len(p) != 1 || p[0] != "Alice" {
		t.Fatalf("Attribute: %v", at)
	}
	if !strings.Contains(e.app.mq.Status(), "verbunden") {
		t.Fatalf("Status: %s", e.app.mq.Status())
	}
}

func TestMQTTViaSupervisorService(t *testing.T) {
	e := newEnv(t, map[string]any{"MQTT_HOST": ""})
	e.sup.mu.Lock()
	e.sup.mqtt = map[string]any{"host": "127.0.0.1", "port": e.broker.port(), "username": "u", "password": "p", "ssl": false}
	e.sup.mu.Unlock()
	go e.app.mq.run()
	waitFor(t, 5*time.Second, "Discovery über Service", func() bool {
		return e.broker.find("homeassistant/sensor/hamc_java/players_online/config") != nil
	})
	e.broker.mu.Lock()
	c := e.broker.connect
	e.broker.mu.Unlock()
	if !bytes.Contains(c, []byte("u")) || !bytes.Contains(c, []byte("p")) {
		t.Fatal("Zugangsdaten fehlen im CONNECT")
	}
	// Deaktiviert
	e2 := newEnv(t, map[string]any{"MQTT_ENABLED": false})
	e2.app.mq.run() // kehrt sofort zurück
	if e2.app.mq.Status() != "deaktiviert" {
		t.Fatalf("Status: %s", e2.app.mq.Status())
	}
}

func TestStaticAndDiag(t *testing.T) {
	e := newEnv(t, nil)
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("Startseite: %v %v", err, resp)
	}
	code, m := e.req("GET", "/api/diag", nil)
	if code != 200 || m["supervisor_token"] != true || m["rcon_password_set"] != true {
		t.Fatalf("%d %v", code, m)
	}
}

var _ = io.EOF
