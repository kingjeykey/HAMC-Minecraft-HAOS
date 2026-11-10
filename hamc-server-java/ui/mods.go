package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mod-/Plugin-Verwaltung mit Modrinth als Quelle. Unabhängig vom Servertyp:
// Ordner und Loader ergeben sich aus der Option TYPE.

var modrinthAPI = "https://api.modrinth.com/v2"

const modUA = "kingjeykey/HAMC-Minecraft-HAOS/1.7.0"

var modClient = &http.Client{
	Timeout: 25 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 20 * time.Second, TLSHandshakeTimeout: 15 * time.Second,
	},
}

type mrVersion struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	VersionNumber string   `json:"version_number"`
	VersionType   string   `json:"version_type"`
	DatePublished string   `json:"date_published"`
	GameVersions  []string `json:"game_versions"`
	Loaders       []string `json:"loaders"`
	Files         []struct {
		Hashes   map[string]string `json:"hashes"`
		URL      string            `json:"url"`
		Filename string            `json:"filename"`
		Primary  bool              `json:"primary"`
		Size     int64             `json:"size"`
	} `json:"files"`
	Dependencies []struct {
		VersionID      string `json:"version_id"`
		ProjectID      string `json:"project_id"`
		DependencyType string `json:"dependency_type"`
	} `json:"dependencies"`
}

func (v *mrVersion) file() (mrFile, bool) {
	if len(v.Files) == 0 {
		return mrFile{}, false
	}
	f := v.Files[0]
	for _, c := range v.Files {
		if c.Primary {
			f = c
			break
		}
	}
	return mrFile{Path: f.Filename, Hashes: f.Hashes, Downloads: []string{f.URL}, FileSize: f.Size}, f.Filename != ""
}

func modrinth(method, p string, q url.Values, body, out any) error {
	u := modrinthAPI + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", modUA)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := modClient.Do(req)
	if err != nil {
		return fmt.Errorf("Modrinth nicht erreichbar: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == 404 {
		return errors.New("Bei Modrinth nicht gefunden")
	}
	if resp.StatusCode != 200 {
		var e struct{ Description string }
		_ = json.Unmarshal(raw, &e)
		if e.Description == "" {
			e.Description = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return errors.New("Modrinth: " + e.Description)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func jsonArr(v []string) string { b, _ := json.Marshal(v); return string(b) }

/* ---------- Servertyp, Ordner, Loader ---------- */

type modInfo struct {
	Type      string   `json:"type"`
	Loaders   []string `json:"loaders"`
	Folders   []string `json:"folders"`
	MC        string   `json:"mc"`
	Supported bool     `json:"supported"`
}

var reMCVer = regexp.MustCompile(`\b\d+\.\d+(?:\.\d+)?\b`)

func (a *App) modServerInfo() modInfo {
	opts := readOptions(a.cfg.OptionsPaths)
	typ, _ := opts["TYPE"].(string)
	typ = strings.ToUpper(strings.TrimSpace(typ))
	info := modInfo{Type: typ, Supported: true}
	switch typ {
	case "FABRIC":
		info.Loaders, info.Folders = []string{"fabric"}, []string{"mods"}
	case "QUILT":
		info.Loaders, info.Folders = []string{"quilt", "fabric"}, []string{"mods"}
	case "FORGE":
		info.Loaders, info.Folders = []string{"forge"}, []string{"mods"}
	case "NEOFORGE":
		info.Loaders, info.Folders = []string{"neoforge"}, []string{"mods"}
	case "PAPER":
		info.Loaders, info.Folders = []string{"paper", "spigot", "bukkit"}, []string{"plugins"}
	case "SPIGOT":
		info.Loaders, info.Folders = []string{"spigot", "bukkit"}, []string{"plugins"}
	case "BUKKIT":
		info.Loaders, info.Folders = []string{"bukkit"}, []string{"plugins"}
	case "MAGMA", "MAGMA_MAINTAINED", "KETTING", "MOHIST", "CATSERVER":
		info.Loaders, info.Folders = []string{"forge", "spigot", "bukkit"}, []string{"mods", "plugins"}
	default: // VANILLA oder unbekannt
		info.Supported = false
		info.Loaders, info.Folders = nil, []string{"mods", "plugins"}
	}
	if v := a.col.Snapshot().Version; v != "" {
		info.MC = reMCVer.FindString(v)
	}
	if info.MC == "" {
		if v, _ := opts["VERSION"].(string); reMCVer.FindString(v) == strings.TrimSpace(v) {
			info.MC = strings.TrimSpace(v)
		}
	}
	return info
}

func (a *App) modInfoH(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.modServerInfo())
}

func (a *App) modDir(name string, create bool) (string, error) {
	if name != "mods" && name != "plugins" {
		return "", errors.New("Ungültiger Ordner")
	}
	abs, err := a.resolve(name)
	if err != nil {
		return "", err
	}
	if create {
		if _, e := os.Stat(abs); e != nil {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return "", err
			}
			a.chown(abs)
		}
	}
	return abs, nil
}

func validModFile(n string) bool {
	return validName(n) && !strings.HasPrefix(n, ".") && (strings.HasSuffix(n, ".jar") || strings.HasSuffix(n, ".jar.disabled"))
}

/* ---------- Liste, Metadaten, Hashes ---------- */

type modEntry struct {
	File    string `json:"file"`
	Enabled bool   `json:"enabled"`
	Size    int64  `json:"size"`
	Mod     int64  `json:"mod"`
	JarMeta
}

var modCache sync.Map // key: Pfad|Größe|Zeit -> JarMeta bzw. string (sha512)

func cacheKey(kind, abs string, fi os.FileInfo) string {
	return kind + "|" + abs + "|" + strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

func metaOf(abs string, fi os.FileInfo) JarMeta {
	k := cacheKey("m", abs, fi)
	if v, ok := modCache.Load(k); ok {
		return v.(JarMeta)
	}
	m := readJarMeta(abs)
	modCache.Store(k, m)
	return m
}

func sha512Of(abs string, fi os.FileInfo) (string, error) {
	k := cacheKey("h", abs, fi)
	if v, ok := modCache.Load(k); ok {
		return v.(string), nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	s := hex.EncodeToString(h.Sum(nil))
	modCache.Store(k, s)
	return s, nil
}

func (a *App) modList(w http.ResponseWriter, r *http.Request) {
	dir, err := a.modDir(r.URL.Query().Get("dir"), false)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	out := []modEntry{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || !validModFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		m := metaOf(filepath.Join(dir, e.Name()), fi)
		out = append(out, modEntry{File: e.Name(), Enabled: strings.HasSuffix(e.Name(), ".jar"), Size: fi.Size(), Mod: fi.ModTime().Unix(), JarMeta: m})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(firstNonEmpty(out[i].Name, out[i].File)) < strings.ToLower(firstNonEmpty(out[j].Name, out[j].File))
	})
	writeJSON(w, 200, map[string]any{"mods": out})
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *App) modFilePath(dirName, file string) (string, error) {
	dir, err := a.modDir(dirName, false)
	if err != nil {
		return "", err
	}
	if !validModFile(file) {
		return "", errors.New("Ungültiger Dateiname")
	}
	return filepath.Join(dir, file), nil
}

func (a *App) modToggle(w http.ResponseWriter, r *http.Request) {
	var in struct{ Dir, File string }
	if !readJSON(w, r, &in) {
		return
	}
	p, err := a.modFilePath(in.Dir, in.File)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var to string
	if strings.HasSuffix(p, ".disabled") {
		to = strings.TrimSuffix(p, ".disabled")
	} else {
		to = p + ".disabled"
	}
	if _, err := os.Lstat(to); err == nil {
		httpErr(w, 409, "Es gibt schon eine Datei mit dem Zielnamen")
		return
	}
	if err := os.Rename(p, to); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"file": filepath.Base(to), "enabled": !strings.HasSuffix(to, ".disabled")})
}

func (a *App) modDelete(w http.ResponseWriter, r *http.Request) {
	var in struct{ Dir, File string }
	if !readJSON(w, r, &in) {
		return
	}
	p, err := a.modFilePath(in.Dir, in.File)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := os.Remove(p); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

/* ---------- Suche ---------- */

func (a *App) modSearch(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	info := a.modServerInfo()
	loaders := info.Loaders
	if l := strings.TrimSpace(qs.Get("loaders")); l != "" {
		loaders = strings.Split(l, ",")
	}
	mc := strings.TrimSpace(qs.Get("mc"))
	if mc == "" {
		mc = info.MC
	}
	if len(loaders) == 0 {
		httpErr(w, 400, "Für diesen Servertyp gibt es keine Mods oder Plugins")
		return
	}
	var cats []string
	for _, l := range loaders {
		cats = append(cats, "categories:"+strings.TrimSpace(l))
	}
	facets := [][]string{{"project_type:mod"}, cats}
	if mc != "" {
		facets = append(facets, []string{"versions:" + mc})
	}
	fb, _ := json.Marshal(facets)
	v := url.Values{}
	v.Set("query", strings.TrimSpace(qs.Get("q")))
	v.Set("facets", string(fb))
	v.Set("limit", "20")
	if o, err := strconv.Atoi(qs.Get("offset")); err == nil && o > 0 {
		v.Set("offset", strconv.Itoa(o))
	}
	if qs.Get("q") == "" {
		v.Set("index", "downloads")
	}
	var res struct {
		Hits []struct {
			ProjectID  string `json:"project_id"`
			Slug       string `json:"slug"`
			Title      string `json:"title"`
			Desc       string `json:"description"`
			Author     string `json:"author"`
			Downloads  int64  `json:"downloads"`
			IconURL    string `json:"icon_url"`
			ServerSide string `json:"server_side"`
		} `json:"hits"`
		Total int `json:"total_hits"`
	}
	if err := modrinth("GET", "/search", v, nil, &res); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	type hit struct {
		ID         string `json:"id"`
		Slug       string `json:"slug"`
		Title      string `json:"title"`
		Desc       string `json:"description"`
		Author     string `json:"author"`
		Downloads  int64  `json:"downloads"`
		Icon       string `json:"icon"`
		ServerSide string `json:"server_side"`
	}
	hits := []hit{}
	for _, h := range res.Hits {
		hits = append(hits, hit{h.ProjectID, h.Slug, h.Title, h.Desc, h.Author, h.Downloads, h.IconURL, h.ServerSide})
	}
	writeJSON(w, 200, map[string]any{"hits": hits, "total": res.Total, "mc": mc, "loaders": loaders})
}

/* ---------- Installation und Updates ---------- */

// installedVersions liefert zu jeder Datei im Ordner die passende Modrinth-Version (falls bekannt).
func (a *App) installedVersions(dir string) (map[string]mrVersion, int) {
	byFile := map[string]mrVersion{}
	hashFile := map[string]string{}
	var hashes []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || !validModFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		h, err := sha512Of(filepath.Join(dir, e.Name()), fi)
		if err != nil {
			continue
		}
		hashes = append(hashes, h)
		hashFile[h] = e.Name()
	}
	if len(hashes) == 0 {
		return byFile, 0
	}
	var res map[string]mrVersion
	if err := modrinth("POST", "/version_files", nil, map[string]any{"hashes": hashes, "algorithm": "sha512"}, &res); err != nil {
		return byFile, len(hashes)
	}
	for h, v := range res {
		if f, ok := hashFile[h]; ok {
			byFile[f] = v
		}
	}
	return byFile, len(hashes)
}

func pickVersion(list []mrVersion) *mrVersion {
	for i := range list {
		if list[i].VersionType == "release" {
			return &list[i]
		}
	}
	if len(list) > 0 {
		return &list[0]
	}
	return nil
}

func latestVersion(project string, loaders []string, mc string) (*mrVersion, error) {
	q := url.Values{}
	q.Set("loaders", jsonArr(loaders))
	if mc != "" {
		q.Set("game_versions", jsonArr([]string{mc}))
	}
	var list []mrVersion
	if err := modrinth("GET", "/project/"+url.PathEscape(project)+"/version", q, nil, &list); err != nil {
		return nil, err
	}
	v := pickVersion(list)
	if v == nil {
		return nil, fmt.Errorf("Keine passende Version für %s %s gefunden", strings.Join(loaders, "/"), mc)
	}
	return v, nil
}

type planItem struct {
	ver  mrVersion
	file mrFile
}

func (a *App) modPlan(dir string, project string, loaders []string, mc string) ([]planItem, error) {
	seen := map[string]bool{}
	have := map[string]bool{}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			have[e.Name()] = true
		}
	}
	inst, _ := a.installedVersions(dir)
	for _, v := range inst {
		seen[v.ProjectID] = true
	}
	var plan []planItem
	var add func(v *mrVersion, depth int) error
	add = func(v *mrVersion, depth int) error {
		if seen[v.ProjectID] {
			return nil
		}
		seen[v.ProjectID] = true
		f, ok := v.file()
		if !ok {
			return errors.New("Version ohne Datei")
		}
		if !validModFile(f.Path) {
			return fmt.Errorf("Unerwarteter Dateiname %q", f.Path)
		}
		if !have[f.Path] {
			plan = append(plan, planItem{*v, f})
		}
		if depth >= 4 {
			return nil
		}
		for _, d := range v.Dependencies {
			if d.DependencyType != "required" || d.ProjectID == "" || seen[d.ProjectID] {
				continue
			}
			var dv *mrVersion
			if d.VersionID != "" {
				var x mrVersion
				if err := modrinth("GET", "/version/"+url.PathEscape(d.VersionID), nil, nil, &x); err == nil {
					dv = &x
				}
			}
			if dv == nil {
				var err error
				if dv, err = latestVersion(d.ProjectID, loaders, mc); err != nil {
					return fmt.Errorf("Benötigte Abhängigkeit nicht installierbar: %v", err)
				}
			}
			if err := add(dv, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	v, err := latestVersion(project, loaders, mc)
	if err != nil {
		return nil, err
	}
	if err := add(v, 0); err != nil {
		return nil, err
	}
	return plan, nil
}

func (a *App) modInstall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir, Project, Title string
		Loaders             []string
		MC                  string
	}
	if !readJSON(w, r, &in) {
		return
	}
	dir, err := a.modDir(in.Dir, true)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if in.Project == "" || len(in.Project) > 64 {
		httpErr(w, 400, "Kein Projekt angegeben")
		return
	}
	if len(in.Loaders) == 0 {
		in.Loaders = a.modServerInfo().Loaders
	}
	if len(in.Loaders) == 0 {
		httpErr(w, 400, "Für diesen Servertyp gibt es keine Mods oder Plugins")
		return
	}
	title := firstNonEmpty(in.Title, in.Project)
	id := a.jobs.Start("Installiere "+title, func(progress func(done, total int64)) error {
		plan, err := a.modPlan(dir, in.Project, in.Loaders, in.MC)
		if err != nil {
			return err
		}
		if len(plan) == 0 {
			return errors.New("Bereits installiert")
		}
		var total, done int64
		for _, p := range plan {
			total += p.file.FileSize
		}
		if total > a.diskFree()-100<<20 {
			return errors.New("nicht genug freier Speicherplatz")
		}
		progress(0, total)
		for _, p := range plan {
			if err := a.modDownload(dir, p.file); err != nil {
				return fmt.Errorf("%s: %v", p.file.Path, err)
			}
			done += p.file.FileSize
			progress(done, total)
		}
		return nil
	})
	writeJSON(w, 200, map[string]any{"job": id})
}

func (a *App) modDownload(dir string, f mrFile) error {
	u, err := url.Parse(f.Downloads[0])
	if err != nil || !mrHostOK(u) {
		return errors.New("Download-Adresse nicht erlaubt")
	}
	var n int64
	return a.mrFetchOne(u.String(), filepath.Join(dir, f.Path), f, &n)
}

func (a *App) modUpdates(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir     string
		Loaders []string
		MC      string
	}
	if !readJSON(w, r, &in) {
		return
	}
	dir, err := a.modDir(in.Dir, false)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	info := a.modServerInfo()
	if len(in.Loaders) == 0 {
		in.Loaders = info.Loaders
	}
	if in.MC == "" {
		in.MC = info.MC
	}
	if in.MC == "" || len(in.Loaders) == 0 {
		httpErr(w, 400, "Minecraft-Version oder Loader unbekannt. Gib sie an.")
		return
	}
	hashFile := map[string]string{}
	var hashes []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jar") || !validModFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if h, err := sha512Of(filepath.Join(dir, e.Name()), fi); err == nil {
			hashes = append(hashes, h)
			hashFile[h] = e.Name()
		}
	}
	type upd struct {
		File      string `json:"file"`
		Current   string `json:"current"`
		Latest    string `json:"latest"`
		VersionID string `json:"version_id"`
	}
	updates := []upd{}
	unknown := 0
	if len(hashes) > 0 {
		var cur, lat map[string]mrVersion
		if err := modrinth("POST", "/version_files", nil, map[string]any{"hashes": hashes, "algorithm": "sha512"}, &cur); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
		if err := modrinth("POST", "/version_files/update", nil, map[string]any{
			"hashes": hashes, "algorithm": "sha512", "loaders": in.Loaders, "game_versions": []string{in.MC}, "version_types": []string{"release", "beta"},
		}, &lat); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
		for _, h := range hashes {
			c, ok := cur[h]
			if !ok {
				unknown++
				continue
			}
			if l, ok := lat[h]; ok && l.ID != c.ID && l.DatePublished > c.DatePublished {
				updates = append(updates, upd{hashFile[h], c.VersionNumber, l.VersionNumber, l.ID})
			}
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].File < updates[j].File })
	writeJSON(w, 200, map[string]any{"updates": updates, "unknown": unknown, "checked": len(hashes)})
}

func (a *App) modUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir   string
		Items []struct{ File, VersionID string }
	}
	if !readJSON(w, r, &in) {
		return
	}
	dir, err := a.modDir(in.Dir, false)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if len(in.Items) == 0 || len(in.Items) > 200 {
		httpErr(w, 400, "Keine Mods ausgewählt")
		return
	}
	for _, it := range in.Items {
		if !strings.HasSuffix(it.File, ".jar") || !validModFile(it.File) {
			httpErr(w, 400, "Ungültiger Dateiname")
			return
		}
	}
	name := "Aktualisiere " + strconv.Itoa(len(in.Items)) + " Mod(s)"
	if len(in.Items) == 1 {
		name = "Aktualisiere " + in.Items[0].File
	}
	id := a.jobs.Start(name, func(progress func(done, total int64)) error {
		inst, _ := a.installedVersions(dir)
		var errs []string
		for i, it := range in.Items {
			progress(int64(i), int64(len(in.Items)))
			var v mrVersion
			if err := modrinth("GET", "/version/"+url.PathEscape(it.VersionID), nil, nil, &v); err != nil {
				errs = append(errs, it.File+": "+err.Error())
				continue
			}
			if cur, ok := inst[it.File]; !ok || cur.ProjectID != v.ProjectID {
				errs = append(errs, it.File+": gehört nicht zu diesem Projekt")
				continue
			}
			f, ok := v.file()
			if !ok || !validModFile(f.Path) {
				errs = append(errs, it.File+": keine gültige Datei")
				continue
			}
			if err := a.modDownload(dir, f); err != nil {
				errs = append(errs, it.File+": "+err.Error())
				continue
			}
			if f.Path != it.File {
				_ = os.Remove(filepath.Join(dir, it.File))
			}
		}
		progress(int64(len(in.Items)), int64(len(in.Items)))
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return nil
	})
	writeJSON(w, 200, map[string]any{"job": id})
}
