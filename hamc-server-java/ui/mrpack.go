package main

import (
	"archive/zip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Modrinth-Modpacks (.mrpack): ZIP mit modrinth.index.json, Download-Liste und
// overrides/ bzw. server-overrides/. Spezifikation: docs.modrinth.com/modpacks/format

type mrFile struct {
	Path   string            `json:"path"`
	Hashes map[string]string `json:"hashes"`
	Env    *struct {
		Client string `json:"client"`
		Server string `json:"server"`
	} `json:"env"`
	Downloads []string `json:"downloads"`
	FileSize  int64    `json:"fileSize"`
}

type mrIndex struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary"`
	Files         []mrFile          `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

const (
	mrMaxIndex = 8 << 20
	mrMaxFiles = 5000
)

// mrHostOK: Die Spezifikation erlaubt Downloads nur von diesen Hosts (per https).
// In Tests austauschbar.
var mrHostOK = func(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com":
		return true
	}
	return false
}

func openMrpack(abs string) (*zip.ReadCloser, *mrIndex, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, nil, errors.New("Keine gültige .mrpack-Datei (ZIP nicht lesbar)")
	}
	for _, f := range zr.File {
		if f.Name != "modrinth.index.json" {
			continue
		}
		if f.UncompressedSize64 > mrMaxIndex {
			zr.Close()
			return nil, nil, errors.New("modrinth.index.json ist zu groß")
		}
		rc, err := f.Open()
		if err != nil {
			zr.Close()
			return nil, nil, err
		}
		var idx mrIndex
		err = json.NewDecoder(io.LimitReader(rc, mrMaxIndex)).Decode(&idx)
		rc.Close()
		if err != nil {
			zr.Close()
			return nil, nil, errors.New("modrinth.index.json ist fehlerhaft")
		}
		if idx.Game != "" && idx.Game != "minecraft" {
			zr.Close()
			return nil, nil, errors.New("Dieses Modpack ist nicht für Minecraft")
		}
		if len(idx.Files) > mrMaxFiles {
			zr.Close()
			return nil, nil, errors.New("Zu viele Dateien im Modpack")
		}
		return zr, &idx, nil
	}
	zr.Close()
	return nil, nil, errors.New("Keine modrinth.index.json gefunden. Das ist kein Modrinth-Modpack (.mrpack)")
}

// mrLoader liefert Servertyp (Add-on-Option TYPE), Loader-Name und -Version.
func (m *mrIndex) loader() (typ, name, ver string) {
	for _, c := range []struct{ dep, typ, name string }{
		{"fabric-loader", "FABRIC", "Fabric"},
		{"quilt-loader", "QUILT", "Quilt"},
		{"neoforge", "NEOFORGE", "NeoForge"},
		{"forge", "FORGE", "Forge"},
	} {
		if v, ok := m.Dependencies[c.dep]; ok {
			return c.typ, c.name, v
		}
	}
	return "", "", ""
}

func (f *mrFile) serverWanted() bool {
	return f.Env == nil || f.Env.Server != "unsupported"
}

// mrTarget prüft den Zielpfad aus dem Modpack und liefert den absoluten Pfad.
func (a *App) mrTarget(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\x00") || (len(rel) > 1 && rel[1] == ':') {
		return "", fmt.Errorf("ungültiger Pfad %q", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("ungültiger Pfad %q", rel)
		}
	}
	return a.resolve(path.Clean(rel))
}

func (a *App) mrInspect(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path string }
	if !readJSON(w, r, &in) {
		return
	}
	abs, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	zr, idx, err := openMrpack(abs)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	zr.Close()
	typ, lname, lver := idx.loader()
	var size int64
	skipped := 0
	for i := range idx.Files {
		if idx.Files[i].serverWanted() {
			size += idx.Files[i].FileSize
		} else {
			skipped++
		}
	}
	res := map[string]any{
		"name": idx.Name, "version": idx.VersionID, "summary": idx.Summary,
		"minecraft": idx.Dependencies["minecraft"], "type": typ, "loader": lname, "loader_version": lver,
		"files": len(idx.Files) - skipped, "skipped_client": skipped, "size": size, "disk_free": a.diskFree(),
	}
	if a.sup.Available() {
		if info, err := a.sup.Info(); err == nil && info.Options != nil {
			res["current_type"], _ = info.Options["TYPE"].(string)
			res["current_version"], _ = info.Options["VERSION"].(string)
			res["can_apply"] = typ != ""
		}
	}
	writeJSON(w, 200, res)
}

// applyServerOptions setzt TYPE und VERSION in der Add-on-Konfiguration (wirkt nach Neustart).
func (a *App) applyServerOptions(typ, mc string) error {
	info, err := a.sup.Info()
	if err != nil {
		return err
	}
	if info.Options == nil {
		return errors.New("Supervisor lieferte keine Optionen")
	}
	for k, v := range info.Options {
		if s, ok := v.(string); ok && s != "" && strings.Trim(s, "*") == "" {
			return fmt.Errorf("Option %s wird maskiert geliefert, Änderung abgebrochen", k)
		}
	}
	if typ != "" {
		info.Options["TYPE"] = typ
	}
	if mc != "" {
		info.Options["VERSION"] = mc
	}
	return a.sup.SetOptions(info.Options)
}

func (a *App) mrInstall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path  string
		Apply bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	abs, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	zr0, idx, err := openMrpack(abs)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	zr0.Close()
	// Alle Pfade und Adressen vorab prüfen, damit nichts halb installiert wird.
	var todo []mrFile
	var total int64
	for _, f := range idx.Files {
		if !f.serverWanted() {
			continue
		}
		if _, err := a.mrTarget(f.Path); err != nil {
			httpErr(w, 400, "Modpack enthält "+err.Error())
			return
		}
		if len(f.Downloads) == 0 {
			httpErr(w, 400, "Keine Download-Adresse für "+f.Path)
			return
		}
		if f.Hashes["sha512"] == "" && f.Hashes["sha1"] == "" {
			httpErr(w, 400, "Keine Prüfsumme für "+f.Path)
			return
		}
		todo = append(todo, f)
		total += f.FileSize
	}
	if total > a.diskFree()-300<<20 {
		httpErr(w, 400, "Nicht genug freier Speicherplatz für das Modpack")
		return
	}
	res := map[string]any{}
	if in.Apply {
		typ, _, _ := idx.loader()
		if err := a.applyServerOptions(typ, idx.Dependencies["minecraft"]); err != nil {
			res["apply_error"] = err.Error()
		} else {
			res["applied"] = true
		}
	}
	label := idx.Name
	if label == "" {
		label = filepath.Base(abs)
	}
	res["job"] = a.jobs.Start("Modpack "+label, func(progress func(done, total int64)) error {
		return a.mrRun(abs, todo, total, progress)
	})
	writeJSON(w, 200, res)
}

func (a *App) mrRun(abs string, todo []mrFile, total int64, progress func(done, total int64)) error {
	var done int64
	progress(0, total)
	var mu sync.Mutex
	var errs []string
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range todo {
		f := todo[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := a.mrDownload(f, func(n int64) { progress(atomic.AddInt64(&done, n), total) }); err != nil {
				mu.Lock()
				if len(errs) < 5 {
					errs = append(errs, path.Base(f.Path)+": "+err.Error())
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.New("Downloads fehlgeschlagen: " + strings.Join(errs, "; "))
	}
	// Konfigurationsdateien des Modpacks; server-overrides gewinnen gegen overrides.
	zr, _, err := openMrpack(abs)
	if err != nil {
		return err
	}
	defer zr.Close()
	tops := map[string]bool{}
	for i := range todo {
		tops[topSeg(todo[i].Path)] = true
	}
	for _, prefix := range []string{"overrides/", "server-overrides/"} {
		if err := a.mrExtract(zr, prefix, tops); err != nil {
			return err
		}
	}
	// Neu angelegte Ordner gehören sonst root, der Server (uid 1000) könnte dort nicht schreiben.
	for t := range tops {
		if dir, err := a.mrTarget(t); err == nil {
			if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
				a.chownTree(dir)
			}
		}
	}
	return nil
}

func topSeg(rel string) string {
	rel = strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

func (a *App) mrDownload(f mrFile, add func(int64)) error {
	dst, err := a.mrTarget(f.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var lastErr error
	for _, raw := range f.Downloads {
		u, err := url.Parse(raw)
		if err != nil || !mrHostOK(u) {
			lastErr = fmt.Errorf("Adresse nicht erlaubt: %s", raw)
			continue
		}
		var n int64
		if lastErr = a.mrFetchOne(u.String(), dst, f, &n); lastErr == nil {
			add(f.FileSize)
			return nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("keine Download-Adresse")
	}
	return lastErr
}

func (a *App) mrFetchOne(u, dst string, f mrFile, n *int64) error {
	client := &http.Client{
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 5 {
				return errors.New("Weiterleitung nicht erlaubt")
			}
			return nil
		},
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "HAMC-Addon")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var h hash.Hash
	want := f.Hashes["sha512"]
	if want != "" {
		h = sha512.New()
	} else {
		want, h = f.Hashes["sha1"], sha1.New()
	}
	if err := a.saveStream(dst, io.TeeReader(countReader{resp.Body, n, func() {}}, h)); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		os.Remove(dst)
		return errors.New("Prüfsumme stimmt nicht überein")
	}
	return nil
}

func (a *App) mrExtract(zr *zip.ReadCloser, prefix string, tops map[string]bool) error {
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, "/") || !f.Mode().IsRegular() {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		dst, err := a.mrTarget(rel)
		if err != nil {
			continue // unsichere Einträge werden still übersprungen
		}
		tops[topSeg(rel)] = true
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = a.saveStream(dst, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
