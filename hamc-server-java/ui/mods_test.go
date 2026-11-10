package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func makeJar(files map[string]string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range files {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(c))
	}
	zw.Close()
	return b.Bytes()
}

func sum512(b []byte) string { h := sha512.Sum512(b); return hex.EncodeToString(h[:]) }

func TestJarMeta(t *testing.T) {
	d := t.TempDir()
	write := func(n string, files map[string]string) string {
		p := filepath.Join(d, n)
		_ = os.WriteFile(p, makeJar(files), 0o644)
		return p
	}
	if m := readJarMeta(write("f.jar", map[string]string{"fabric.mod.json": `{"id":"sodium","name":"Sodium","version":"0.6.0"}`})); m.ID != "sodium" || m.Name != "Sodium" || m.Version != "0.6.0" || m.Kind != "Fabric" {
		t.Fatalf("fabric: %+v", m)
	}
	toml := "modLoader=\"javafml\"\n[[mods]]\nmodId=\"jei\"\nversion=\"${file.jarVersion}\"\ndisplayName=\"Just Enough Items\"\n[[dependencies.jei]]\nmodId=\"forge\"\n"
	if m := readJarMeta(write("g.jar", map[string]string{"META-INF/mods.toml": toml, "META-INF/MANIFEST.MF": "Implementation-Version: 19.2.0\n"})); m.ID != "jei" || m.Name != "Just Enough Items" || m.Version != "19.2.0" || m.Kind != "Forge" {
		t.Fatalf("forge: %+v", m)
	}
	if m := readJarMeta(write("n.jar", map[string]string{"META-INF/neoforge.mods.toml": "[[mods]]\nmodId = \"x\"\nversion = \"1.2\"\n"})); m.Kind != "NeoForge" || m.Version != "1.2" {
		t.Fatalf("neoforge: %+v", m)
	}
	if m := readJarMeta(write("p.jar", map[string]string{"plugin.yml": "name: Essentials\nversion: '2.20.1'\nmain: x.Y\n"})); m.Name != "Essentials" || m.Version != "2.20.1" || m.Kind != "Plugin" {
		t.Fatalf("plugin: %+v", m)
	}
	if m := readJarMeta(write("q.jar", map[string]string{"quilt.mod.json": `{"quilt_loader":{"id":"qm","version":"3"},"metadata":{"name":"Q"}}`})); m.Kind != "Quilt" || m.ID != "qm" {
		t.Fatalf("quilt: %+v", m)
	}
	if m := readJarMeta(filepath.Join(d, "nope.jar")); m.Kind != "" {
		t.Fatal("fehlende Datei")
	}
}

func TestMods(t *testing.T) {
	oldHost, oldAPI := mrHostOK, modrinthAPI
	mrHostOK = func(u *url.URL) bool { return u.Hostname() == "127.0.0.1" }
	defer func() { mrHostOK, modrinthAPI = oldHost, oldAPI }()

	jarA := makeJar(map[string]string{"fabric.mod.json": `{"id":"moda","name":"Mod A","version":"2.0"}`})
	jarB := makeJar(map[string]string{"fabric.mod.json": `{"id":"modb","name":"Mod B","version":"1.0"}`})
	jarOld := makeJar(map[string]string{"fabric.mod.json": `{"id":"modc","name":"Mod C","version":"1.0"}`})
	jarNew := makeJar(map[string]string{"fabric.mod.json": `{"id":"modc","name":"Mod C","version":"1.1"}`})

	var mu sync.Mutex
	var searchFacets string
	var srv *httptest.Server
	ver := func(id, proj, num, date string, jar []byte, fn string, deps []map[string]any) map[string]any {
		return map[string]any{"id": id, "project_id": proj, "version_number": num, "version_type": "release", "date_published": date,
			"files":        []map[string]any{{"hashes": map[string]string{"sha512": sum512(jar)}, "url": srv.URL + "/cdn/" + fn, "filename": fn, "primary": true, "size": len(jar)}},
			"dependencies": deps}
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(400)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/cdn/"):
			switch r.URL.Path {
			case "/cdn/moda.jar":
				_, _ = w.Write(jarA)
			case "/cdn/modb.jar":
				_, _ = w.Write(jarB)
			case "/cdn/modc-1.1.jar":
				_, _ = w.Write(jarNew)
			default:
				w.WriteHeader(404)
			}
		case r.URL.Path == "/search":
			mu.Lock()
			searchFacets = r.URL.Query().Get("facets")
			mu.Unlock()
			enc(map[string]any{"total_hits": 1, "hits": []map[string]any{{"project_id": "PA", "slug": "moda", "title": "Mod A", "description": "d", "author": "x", "downloads": 5, "server_side": "required"}}})
		case r.URL.Path == "/project/PA/version":
			enc([]map[string]any{ver("VA", "PA", "2.0", "2026-01-01", jarA, "moda.jar", []map[string]any{{"project_id": "PB", "dependency_type": "required"}, {"project_id": "PX", "dependency_type": "optional"}})})
		case r.URL.Path == "/project/PB/version":
			enc([]map[string]any{ver("VB", "PB", "1.0", "2026-01-01", jarB, "modb.jar", nil)})
		case r.URL.Path == "/version/VC2":
			enc(ver("VC2", "PC", "1.1", "2026-03-01", jarNew, "modc-1.1.jar", nil))
		case r.URL.Path == "/version_files":
			var in struct{ Hashes []string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			out := map[string]any{}
			for _, h := range in.Hashes {
				if h == sum512(jarOld) {
					out[h] = ver("VC1", "PC", "1.0", "2026-02-01", jarOld, "modc-1.0.jar", nil)
				}
			}
			enc(out)
		case r.URL.Path == "/version_files/update":
			var in struct {
				Hashes       []string
				GameVersions []string `json:"game_versions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			out := map[string]any{}
			for _, h := range in.Hashes {
				if h == sum512(jarOld) && len(in.GameVersions) == 1 {
					out[h] = ver("VC2", "PC", "1.1", "2026-03-01", jarNew, "modc-1.1.jar", nil)
				}
			}
			enc(out)
		default:
			w.WriteHeader(404)
			enc(map[string]any{"description": "nicht gefunden"})
		}
	}))
	defer srv.Close()
	modrinthAPI = srv.URL

	e := newEnv(t, map[string]any{"TYPE": "FABRIC"})
	mods := filepath.Join(e.dir, "mods")
	_ = os.MkdirAll(mods, 0o755)
	_ = os.WriteFile(filepath.Join(mods, "modc-1.0.jar"), jarOld, 0o644)
	_ = os.WriteFile(filepath.Join(mods, "own.jar"), makeJar(map[string]string{"fabric.mod.json": `{"id":"own","name":"Eigener Mod","version":"9"}`}), 0o644)
	waitJob := func(want string) map[string]any {
		var last map[string]any
		waitFor(t, 10*time.Second, "Job "+want, func() bool {
			resp, _ := http.Get(e.srv.URL + "/api/jobs")
			var jobs []map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&jobs)
			resp.Body.Close()
			if len(jobs) > 0 && jobs[0]["status"] != "running" {
				last = jobs[0]
				return true
			}
			return false
		})
		if last["status"] != want {
			t.Fatalf("Job-Status %v: %v", last["status"], last["error"])
		}
		return last
	}

	e.app.col.collect(true)
	if code, m := e.req("GET", "/api/mods/info", nil); code != 200 || m["type"] != "FABRIC" || m["supported"] != true || m["mc"] != "1.21.4" {
		t.Fatalf("info: %d %v", code, m)
	}
	if code, m := e.req("GET", "/api/mods/list?dir=mods", nil); code != 200 || len(m["mods"].([]any)) != 2 {
		t.Fatalf("list: %d %v", code, m)
	} else if first := m["mods"].([]any)[0].(map[string]any); first["name"] != "Eigener Mod" || first["kind"] != "Fabric" {
		t.Fatalf("Metadaten/Sortierung: %v", first)
	}
	if code, _ := e.req("GET", "/api/mods/list?dir=world", nil); code != 400 {
		t.Fatal("nur mods/plugins erlaubt")
	}

	// Suche: Facets enthalten Typ, Loader, Version
	if code, m := e.req("GET", "/api/mods/search?q=mod", nil); code != 200 || len(m["hits"].([]any)) != 1 {
		t.Fatalf("search: %d %v", code, m)
	}
	mu.Lock()
	f := searchFacets
	mu.Unlock()
	if !strings.Contains(f, "categories:fabric") || !strings.Contains(f, "versions:1.21.4") || !strings.Contains(f, "project_type:mod") {
		t.Fatalf("Facets: %s", f)
	}

	// Installation mit Abhängigkeit
	if code, m := e.req("POST", "/api/mods/install", map[string]any{"dir": "mods", "project": "PA", "title": "Mod A"}); code != 200 {
		t.Fatalf("install: %d %v", code, m)
	}
	waitJob("done")
	for _, n := range []string{"moda.jar", "modb.jar"} {
		if _, err := os.Stat(filepath.Join(mods, n)); err != nil {
			t.Fatalf("%s fehlt (Abhängigkeit?)", n)
		}
	}
	// erneut: nichts zu tun
	e.req("POST", "/api/mods/install", map[string]any{"dir": "mods", "project": "PA"})
	if j := waitJob("error"); !strings.Contains(j["error"].(string), "Bereits") {
		t.Fatalf("zweite Installation: %v", j)
	}

	// Updates
	code, m := e.req("POST", "/api/mods/updates", map[string]any{"dir": "mods"})
	ups := m["updates"].([]any)
	if code != 200 || len(ups) != 1 || ups[0].(map[string]any)["file"] != "modc-1.0.jar" || ups[0].(map[string]any)["latest"] != "1.1" || m["unknown"].(float64) != 3 {
		t.Fatalf("updates: %d %v", code, m)
	}
	e.req("POST", "/api/mods/update", map[string]any{"dir": "mods", "items": []map[string]string{{"File": "modc-1.0.jar", "VersionID": "VC2"}}})
	waitJob("done")
	if _, err := os.Stat(filepath.Join(mods, "modc-1.1.jar")); err != nil {
		t.Fatal("neue Version fehlt")
	}
	if _, err := os.Stat(filepath.Join(mods, "modc-1.0.jar")); err == nil {
		t.Fatal("alte Version muss entfernt werden")
	}
	// fremde Datei darf nicht über eine fremde Version „aktualisiert“ werden
	e.req("POST", "/api/mods/update", map[string]any{"dir": "mods", "items": []map[string]string{{"File": "own.jar", "VersionID": "VC2"}}})
	if j := waitJob("error"); !strings.Contains(j["error"].(string), "gehört nicht") {
		t.Fatalf("Projektprüfung: %v", j)
	}

	// Deaktivieren, Aktivieren, Löschen, Pfadschutz
	if code, m := e.req("POST", "/api/mods/toggle", map[string]any{"dir": "mods", "file": "own.jar"}); code != 200 || m["file"] != "own.jar.disabled" || m["enabled"] != false {
		t.Fatalf("toggle: %d %v", code, m)
	}
	if code, m := e.req("POST", "/api/mods/toggle", map[string]any{"dir": "mods", "file": "own.jar.disabled"}); code != 200 || m["file"] != "own.jar" {
		t.Fatalf("toggle zurück: %d %v", code, m)
	}
	if code, _ := e.req("POST", "/api/mods/delete", map[string]any{"dir": "mods", "file": "../server.properties"}); code != 400 {
		t.Fatal("Pfad-Ausbruch muss abgelehnt werden")
	}
	if code, _ := e.req("POST", "/api/mods/delete", map[string]any{"dir": "mods", "file": "readme.txt"}); code != 400 {
		t.Fatal("nur Jar-Dateien")
	}
	if code, _ := e.req("POST", "/api/mods/delete", map[string]any{"dir": "mods", "file": "own.jar"}); code != 200 {
		t.Fatal("Löschen")
	}
	if _, err := os.Stat(filepath.Join(mods, "own.jar")); err == nil {
		t.Fatal("own.jar noch da")
	}

	// Vanilla: nicht unterstützt
	e2 := newEnv(t, map[string]any{"TYPE": "VANILLA"})
	if code, m := e2.req("GET", "/api/mods/info", nil); code != 200 || m["supported"] != false {
		t.Fatalf("vanilla: %v", m)
	}
	if code, _ := e2.req("GET", "/api/mods/search?q=x", nil); code != 400 {
		t.Fatal("Suche bei Vanilla muss abgelehnt werden")
	}
}
