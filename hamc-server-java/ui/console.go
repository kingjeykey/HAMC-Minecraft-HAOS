package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const consoleBacklog = 300

func tailLines(p string, n int) ([]string, int64) {
	f, err := os.Open(p)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0
	}
	size := st.Size()
	start := size - 256<<10
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, size
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\r\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // erste Zeile ist vermutlich abgeschnitten
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines, size
}

func sseLine(w io.Writer, line string) {
	fmt.Fprintf(w, "data: %s\n\n", stripCodes(line))
}

// consoleStream sendet das Server-Log als Server-Sent-Events: zuerst die letzten
// Zeilen, danach alles Neue. Log-Rotation (Neustart des Servers) wird erkannt.
func (a *App) consoleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, "Streaming nicht unterstützt")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	logPath := filepath.Join(a.root, "logs", "latest.log")

	lines, off := tailLines(logPath, consoleBacklog)
	if _, err := os.Stat(logPath); err != nil {
		fmt.Fprint(w, "event: info\ndata: Noch keine Log-Datei vorhanden (logs/latest.log). Sie erscheint, sobald der Server startet.\n\n")
	}
	for _, l := range lines {
		sseLine(w, l)
	}
	fl.Flush()

	var lastIno uint64
	if fi, err := os.Stat(logPath); err == nil {
		lastIno = inode(fi)
	}
	var partial string
	tick := time.NewTicker(500 * time.Millisecond)
	beat := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-tick.C:
			fi, err := os.Stat(logPath)
			if err != nil {
				continue
			}
			if ino := inode(fi); ino != lastIno || fi.Size() < off {
				off, lastIno, partial = 0, ino, ""
				fmt.Fprint(w, "event: info\ndata: — Log wurde neu gestartet —\n\n")
			}
			if fi.Size() == off {
				continue
			}
			f, err := os.Open(logPath)
			if err != nil {
				continue
			}
			toRead := fi.Size() - off
			if toRead > 1<<20 {
				toRead = 1 << 20
			}
			buf := make([]byte, toRead)
			n, _ := f.ReadAt(buf, off)
			f.Close()
			off += int64(n)
			data := partial + string(buf[:n])
			parts := strings.Split(data, "\n")
			partial = parts[len(parts)-1]
			for _, l := range parts[:len(parts)-1] {
				sseLine(w, strings.TrimRight(l, "\r"))
			}
			fl.Flush()
		}
	}
}

func (a *App) consoleCommand(w http.ResponseWriter, r *http.Request) {
	var in struct{ Cmd string }
	if !readJSON(w, r, &in) {
		return
	}
	cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.Cmd), "/"))
	if cmd == "" || len(cmd) > 1000 || strings.ContainsAny(cmd, "\r\n") {
		httpErr(w, 400, "Ungültiger Befehl")
		return
	}
	out, err := a.rcon.Exec(cmd)
	if err != nil {
		httpErr(w, 502, rconErrText(err))
		return
	}
	writeJSON(w, 200, map[string]any{"output": strings.TrimRight(stripCodes(out), "\n")})
}
