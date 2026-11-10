package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func buildMrpack(t *testing.T, files []map[string]any, extra map[string]string, deps map[string]string) []byte {
	t.Helper()
	idx, _ := json.Marshal(map[string]any{"formatVersion": 1, "game": "minecraft", "versionId": "1.0", "name": "Testpack", "files": files, "dependencies": deps})
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("modrinth.index.json")
	_, _ = w.Write(idx)
	for n, c := range extra {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(c))
	}
	zw.Close()
	return b.Bytes()
}

func TestMrpack(t *testing.T) {
	old := mrHostOK
	mrHostOK = func(u *url.URL) bool { return u.Hostname() == "127.0.0.1" }
	defer func() { mrHostOK = old }()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.jar":
			_, _ = w.Write([]byte("MODA"))
		case "/b.jar":
			_, _ = w.Write([]byte("MODB"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer cdn.Close()
	h512 := sha512.Sum512([]byte("MODA"))
	h1 := sha1.Sum([]byte("MODB"))
	files := []map[string]any{
		{"path": "mods/a.jar", "hashes": map[string]string{"sha512": hex.EncodeToString(h512[:]), "sha1": "x"}, "downloads": []string{cdn.URL + "/a.jar"}, "fileSize": 4},
		{"path": "mods/b.jar", "hashes": map[string]string{"sha1": hex.EncodeToString(h1[:])}, "downloads": []string{cdn.URL + "/missing", cdn.URL + "/b.jar"}, "fileSize": 4},
		{"path": "mods/client.jar", "hashes": map[string]string{"sha1": "x"}, "downloads": []string{cdn.URL + "/a.jar"}, "fileSize": 4, "env": map[string]string{"client": "required", "server": "unsupported"}},
	}
	deps := map[string]string{"minecraft": "1.21.1", "fabric-loader": "0.16.5"}
	pack := buildMrpack(t, files, map[string]string{
		"overrides/config/x.toml": "A", "overrides/config/y.toml": "OV", "server-overrides/config/y.toml": "SRV",
		"client-overrides/config/c.toml": "C", "overrides/../evil.txt": "EVIL",
	}, deps)

	e := newEnv(t, map[string]any{"TYPE": "VANILLA", "VERSION": "LATEST"})
	_ = os.WriteFile(filepath.Join(e.dir, "pack.mrpack"), pack, 0o644)

	code, m := e.req("POST", "/api/mrpack/inspect", map[string]any{"path": "/pack.mrpack"})
	if code != 200 || m["type"] != "FABRIC" || m["minecraft"] != "1.21.1" || m["files"].(float64) != 2 || m["skipped_client"].(float64) != 1 || m["current_type"] != "VANILLA" {
		t.Fatalf("inspect: %d %v", code, m)
	}

	code, m = e.req("POST", "/api/mrpack/install", map[string]any{"path": "/pack.mrpack", "apply": true})
	if code != 200 || m["applied"] != true {
		t.Fatalf("install: %d %v", code, m)
	}
	waitFor(t, 10*time.Second, "Modpack", func() bool {
		resp, _ := http.Get(e.srv.URL + "/api/jobs")
		var jobs []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&jobs)
		resp.Body.Close()
		if len(jobs) > 0 && jobs[0]["status"] == "error" {
			t.Fatalf("Job-Fehler: %v", jobs[0]["error"])
		}
		return len(jobs) > 0 && jobs[0]["status"] == "done"
	})
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(e.dir, p)); return string(b) }
	if read("mods/a.jar") != "MODA" || read("mods/b.jar") != "MODB" {
		t.Fatal("Mods fehlen")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "mods", "client.jar")); err == nil {
		t.Fatal("Client-Mod darf nicht installiert werden")
	}
	if read("config/x.toml") != "A" || read("config/y.toml") != "SRV" {
		t.Fatalf("Overrides falsch: %q %q", read("config/x.toml"), read("config/y.toml"))
	}
	if _, err := os.Stat(filepath.Join(e.dir, "config", "c.toml")); err == nil {
		t.Fatal("client-overrides dürfen nicht angewendet werden")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.dir), "evil.txt")); err == nil {
		t.Fatal("Pfad-Ausbruch")
	}
	e.sup.mu.Lock()
	opts := e.sup.options
	e.sup.mu.Unlock()
	if opts["TYPE"] != "FABRIC" || opts["VERSION"] != "1.21.1" {
		t.Fatalf("Optionen nicht gesetzt: %v", opts)
	}

	// falsche Prüfsumme
	bad := buildMrpack(t, []map[string]any{{"path": "mods/a.jar", "hashes": map[string]string{"sha1": "00"}, "downloads": []string{cdn.URL + "/a.jar"}, "fileSize": 4}}, nil, deps)
	_ = os.WriteFile(filepath.Join(e.dir, "bad.mrpack"), bad, 0o644)
	_ = os.Remove(filepath.Join(e.dir, "mods", "a.jar"))
	e.req("POST", "/api/mrpack/install", map[string]any{"path": "/bad.mrpack"})
	waitFor(t, 10*time.Second, "Fehlerjob", func() bool {
		resp, _ := http.Get(e.srv.URL + "/api/jobs")
		var jobs []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&jobs)
		resp.Body.Close()
		return len(jobs) > 0 && jobs[0]["status"] == "error"
	})
	if _, err := os.Stat(filepath.Join(e.dir, "mods", "a.jar")); err == nil {
		t.Fatal("Datei mit falscher Prüfsumme muss gelöscht werden")
	}

	// unsicherer Pfad, nicht erlaubter Host, kein Modpack
	evil := buildMrpack(t, []map[string]any{{"path": "../x.jar", "hashes": map[string]string{"sha1": "1"}, "downloads": []string{cdn.URL + "/a.jar"}, "fileSize": 1}}, nil, deps)
	_ = os.WriteFile(filepath.Join(e.dir, "evil.mrpack"), evil, 0o644)
	if code, _ := e.req("POST", "/api/mrpack/install", map[string]any{"path": "/evil.mrpack"}); code != 400 {
		t.Fatal("../-Pfad muss abgelehnt werden")
	}
	_ = os.WriteFile(filepath.Join(e.dir, "x.mrpack"), []byte("kein zip"), 0o644)
	if code, _ := e.req("POST", "/api/mrpack/inspect", map[string]any{"path": "/x.mrpack"}); code != 400 {
		t.Fatal("Kein-ZIP muss abgelehnt werden")
	}
	mrHostOK = old
	if mrHostOK(&url.URL{Scheme: "https", Host: "evil.example.com"}) || mrHostOK(&url.URL{Scheme: "http", Host: "cdn.modrinth.com"}) || !mrHostOK(&url.URL{Scheme: "https", Host: "cdn.modrinth.com"}) {
		t.Fatal("Host-Erlaubnisliste falsch")
	}
}
