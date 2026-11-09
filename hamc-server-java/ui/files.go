package main

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

var errOutside = errors.New("Pfad liegt außerhalb des Serverordners")

const maxEditSize = 2 << 20 // 2 MB für den Texteditor

type fileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mod   int64  `json:"mod"`
	World bool   `json:"world,omitempty"`
}

func (a *App) within(p string) bool {
	return p == a.root || strings.HasPrefix(p, a.root+string(filepath.Separator))
}

// resolve wandelt einen vom Browser gelieferten Pfad in einen absoluten Pfad innerhalb
// des Serverordners um. Symlinks, die aus dem Ordner hinausführen, werden abgelehnt.
func (a *App) resolve(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	clean := path.Clean("/" + rel)
	abs := filepath.Join(a.root, filepath.FromSlash(clean))
	p := abs
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			if !a.within(real) {
				return "", errOutside
			}
			return abs, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errOutside
		}
		p = parent
	}
}

func (a *App) rel(abs string) string {
	r := strings.TrimPrefix(abs, a.root)
	if r == "" {
		return "/"
	}
	return filepath.ToSlash(r)
}

func (a *App) chown(p string) {
	if os.Geteuid() == 0 {
		_ = os.Lchown(p, a.cfg.UID, a.cfg.GID)
	}
}

func (a *App) chownTree(p string) {
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err == nil {
			a.chown(q)
		}
		return nil
	})
}

func validName(n string) bool {
	return n != "" && n != "." && n != ".." && len(n) <= 200 && !strings.ContainsAny(n, "/\\\x00")
}

func (a *App) diskFree() int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(a.root, &st) != nil {
		return 1 << 62
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

func (a *App) apiPath(w http.ResponseWriter, p string) (string, bool) {
	abs, err := a.resolve(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return "", false
	}
	return abs, true
}

func (a *App) filesList(w http.ResponseWriter, r *http.Request) {
	abs, ok := a.apiPath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		httpErr(w, 404, "Ordner nicht gefunden")
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		full := filepath.Join(abs, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(full)
			if err != nil || !a.within(real) {
				continue
			}
		}
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		fe := fileEntry{Name: e.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mod: fi.ModTime().Unix()}
		if fe.Dir {
			if _, err := os.Stat(filepath.Join(full, "level.dat")); err == nil {
				fe.World = true
			}
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, 200, map[string]any{"path": a.rel(abs), "entries": out, "disk_free": a.diskFree()})
}

func (a *App) filesDownload(w http.ResponseWriter, r *http.Request) {
	abs, ok := a.apiPath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		httpErr(w, 404, "Nicht gefunden")
		return
	}
	name := filepath.Base(abs)
	if abs == a.root {
		name = "server"
	}
	if !info.IsDir() {
		f, err := os.Open(abs)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		defer f.Close()
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}
	// Ordner (z. B. eine Welt) als ZIP. Läuft der Server, wird das Speichern kurz
	// angehalten, damit die Welt konsistent kopiert wird.
	if _, err := os.Stat(filepath.Join(abs, "level.dat")); err == nil && a.col.IsOnline() {
		if _, err := a.rcon.Exec("save-off"); err == nil {
			defer func() { _, _ = a.rcon.Exec("save-on") }()
			_, _ = a.rcon.Exec("save-all flush")
		}
	}
	a.streamZip(w, abs, name)
}

func (a *App) streamZip(w http.ResponseWriter, dir, name string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})
	defer zw.Close()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		zname := name + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			_, err := zw.Create(zname + "/")
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			return nil
		}
		hdr.Name, hdr.Method = zname, zip.Deflate
		wr, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(wr, f)
		return err
	})
}

func (a *App) filesRead(w http.ResponseWriter, r *http.Request) {
	abs, ok := a.apiPath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		httpErr(w, 404, "Datei nicht gefunden")
		return
	}
	if info.Size() > maxEditSize {
		httpErr(w, 413, "Datei ist größer als 2 MB und kann nicht bearbeitet werden")
		return
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, c := range head {
		if c == 0 {
			httpErr(w, 415, "Binärdatei, nicht bearbeitbar")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"path": a.rel(abs), "content": string(b)})
}

func (a *App) filesWrite(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path, Content string }
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Content) > maxEditSize {
		httpErr(w, 413, "Inhalt zu groß")
		return
	}
	abs, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	if abs == a.root {
		httpErr(w, 400, "Ungültiger Pfad")
		return
	}
	_, statErr := os.Stat(abs)
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	_, err = f.WriteString(in.Content)
	f.Close()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if statErr != nil {
		a.chown(abs)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) filesUpload(w http.ResponseWriter, r *http.Request) {
	dir, ok := a.apiPath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		httpErr(w, 404, "Zielordner nicht gefunden")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		httpErr(w, 400, "Kein Multipart-Upload")
		return
	}
	var saved []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		name := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		if part.FileName() == "" {
			continue
		}
		if !validName(name) {
			httpErr(w, 400, "Ungültiger Dateiname")
			return
		}
		dst, err := a.resolve(path.Join(a.rel(dir), name))
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		if err := a.saveStream(dst, part); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		saved = append(saved, name)
	}
	writeJSON(w, 200, map[string]any{"uploaded": saved})
}

// saveStream schreibt zuerst in eine temporäre Datei und benennt sie erst am Ende um.
func (a *App) saveStream(dst string, src io.Reader) error {
	tmp := dst + ".hamc-part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	a.chown(dst)
	return nil
}

func (a *App) filesMkdir(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	if !validName(in.Name) {
		httpErr(w, 400, "Ungültiger Ordnername")
		return
	}
	parent, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	dst, err := a.resolve(path.Join(a.rel(parent), in.Name))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	a.chown(dst)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) filesRename(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	if !validName(in.Name) {
		httpErr(w, 400, "Ungültiger Name")
		return
	}
	src, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	if src == a.root {
		httpErr(w, 400, "Der Hauptordner kann nicht umbenannt werden")
		return
	}
	dst, err := a.resolve(path.Join(path.Dir(a.rel(src)), in.Name))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		httpErr(w, 409, "Ein Eintrag mit diesem Namen existiert bereits")
		return
	}
	if err := os.Rename(src, dst); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) filesDelete(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path string }
	if !readJSON(w, r, &in) {
		return
	}
	abs, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	if abs == a.root {
		httpErr(w, 400, "Der Hauptordner kann nicht gelöscht werden")
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func uniqueDir(base string) string {
	p := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); err != nil {
			return p
		}
		p = fmt.Sprintf("%s-%d", base, i)
	}
}

type countReader struct {
	r    io.Reader
	n    *int64
	tick func()
}

func (c countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	c.tick()
	return n, err
}

func (a *App) filesUnzip(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path string }
	if !readJSON(w, r, &in) {
		return
	}
	src, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	if fi, err := os.Stat(src); err != nil || fi.IsDir() || !strings.EqualFold(filepath.Ext(src), ".zip") {
		httpErr(w, 400, "Keine ZIP-Datei")
		return
	}
	dest := uniqueDir(strings.TrimSuffix(src, filepath.Ext(src)))
	name := filepath.Base(src)
	id := a.jobs.Start("Entpacke "+name, func(progress func(done, total int64)) error {
		zr, err := zip.OpenReader(src)
		if err != nil {
			return err
		}
		defer zr.Close()
		var total int64
		for _, f := range zr.File {
			total += int64(f.UncompressedSize64)
		}
		if total > a.diskFree()-200<<20 {
			return errors.New("nicht genug freier Speicherplatz")
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		var done int64
		last := time.Now()
		for _, f := range zr.File {
			clean := path.Clean("/" + strings.ReplaceAll(f.Name, "\\", "/"))
			target := filepath.Join(dest, filepath.FromSlash(clean))
			if !strings.HasPrefix(target, dest+string(filepath.Separator)) && target != dest {
				continue
			}
			mode := f.Mode()
			if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
				_ = os.MkdirAll(target, 0o755)
				continue
			}
			if !mode.IsRegular() {
				continue // Symlinks u. Ä. werden bewusst ignoriert
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, countReader{rc, &done, func() {
				if time.Since(last) > 500*time.Millisecond {
					progress(done, total)
					last = time.Now()
				}
			}})
			rc.Close()
			out.Close()
			if err != nil {
				return err
			}
		}
		a.chownTree(dest)
		progress(total, total)
		return nil
	})
	writeJSON(w, 200, map[string]any{"job": id, "target": a.rel(dest)})
}

func (a *App) filesFetch(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path, URL string }
	if !readJSON(w, r, &in) {
		return
	}
	dir, ok := a.apiPath(w, in.Path)
	if !ok {
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		httpErr(w, 400, "Nur http(s)-Adressen ohne Zugangsdaten sind erlaubt")
		return
	}
	id := a.jobs.Start("Lade "+u.Host+path.Base(u.Path), func(progress func(done, total int64)) error {
		client := &http.Client{Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second,
		}}
		req, _ := http.NewRequest("GET", u.String(), nil)
		req.Header.Set("User-Agent", "HAMC-Addon")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("Server antwortet mit HTTP %d", resp.StatusCode)
		}
		name := path.Base(u.Path)
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
				name = filepath.Base(params["filename"])
			}
		}
		if !validName(name) || name == "/" {
			name = "download"
		}
		if resp.ContentLength > a.diskFree()-200<<20 {
			return errors.New("nicht genug freier Speicherplatz")
		}
		dst, err := a.resolve(path.Join(a.rel(dir), name))
		if err != nil {
			return err
		}
		var done int64
		last := time.Now()
		return a.saveStream(dst, countReader{resp.Body, &done, func() {
			if time.Since(last) > 500*time.Millisecond {
				progress(done, resp.ContentLength)
				last = time.Now()
			}
		}})
	})
	writeJSON(w, 200, map[string]any{"job": id})
}
