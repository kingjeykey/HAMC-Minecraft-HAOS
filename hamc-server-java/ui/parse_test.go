package main

import (
	"math"
	"testing"
	"time"
)

func near(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 0.011 }

func TestParseTickQuery(t *testing.T) {
	out := "The game is running normally\nTarget tick rate: 20.0 per second.\nAverage time per tick: 4.2ms (Target: 50.0ms)\nPercentiles: P50: 3.9ms P95: 7.1ms P99: 9.8ms, based on 100 ticks"
	ti, ok := parseTickQuery(out)
	if !ok || !near(ti.MSPT, 4.2) || !near(ti.P95, 7.1) || !near(ti.P50, 3.9) || !near(ti.P99, 9.8) || !near(ti.TPS, 20) {
		t.Fatalf("falsch erkannt: %+v ok=%v", ti, ok)
	}
	// Überlast: MSPT 80 ms -> TPS 12,5
	ti, ok = parseTickQuery("Target tick rate: 20.0 per second.\nAverage time per tick: 80.0ms (Target: 50.0ms)")
	if !ok || !near(ti.TPS, 12.5) {
		t.Fatalf("Überlast: %+v", ti)
	}
	if _, ok := parseTickQuery("Unknown or incomplete command"); ok {
		t.Fatal("Unbekannter Befehl darf nicht erkannt werden")
	}
}

func TestParsePaper(t *testing.T) {
	ti, ok := parsePaperTPS("§6TPS from last 1m, 5m, 15m: §a*20.0, §a19.8, §a19.5")
	if !ok || !near(ti.TPS, 20) {
		t.Fatalf("%+v", ti)
	}
	ti, ok = parsePaperTPS("TPS from last 5s, 1m, 5m, 15m: 18.4, 19.9, 20.0, 20.0")
	if !ok || !near(ti.TPS, 18.4) {
		t.Fatalf("%+v", ti)
	}
	v, ok := parseMSPT("§6Server tick times §7(avg/min/max)§6 from last 5s, 10s, 1m:\n◴ §a12.3§7/§a4.1§7/§a30.2§r, §a11.0/3.0/25.0")
	if !ok || !near(v, 12.3) {
		t.Fatalf("mspt: %v %v", v, ok)
	}
	if _, ok := parseMSPT("Unknown command"); ok {
		t.Fatal("kein MSPT erwartet")
	}
}

func TestParseForge(t *testing.T) {
	out := "Dim 0 (minecraft:overworld): Mean tick time: 5.3 ms. Mean TPS: 20.000\nOverall: Mean tick time: 8.516 ms. Mean TPS: 20.000"
	ti, ok := parseForge(out)
	if !ok || !near(ti.MSPT, 8.516) || !near(ti.TPS, 20) {
		t.Fatalf("%+v", ti)
	}
}

func TestParsePlayerList(t *testing.T) {
	on, mx, names, ok := parsePlayerList("There are 2 of a max of 20 players online: Alice, Bob")
	if !ok || on != 2 || mx != 20 || len(names) != 2 || names[1] != "Bob" {
		t.Fatalf("%v %v %v %v", on, mx, names, ok)
	}
	on, mx, names, ok = parsePlayerList("There are 0/10 players online:")
	if !ok || on != 0 || mx != 10 || len(names) != 0 {
		t.Fatalf("alt: %v %v %v %v", on, mx, names, ok)
	}
	if _, _, _, ok := parsePlayerList("???"); ok {
		t.Fatal("kein Treffer erwartet")
	}
}

func TestPing(t *testing.T) {
	mc := newFakeMC(t, `{"version":{"name":"1.21.4","protocol":769},"players":{"max":20,"online":2,"sample":[{"name":"Alice","id":"x"},{"name":"Bob","id":"y"}]},"description":{"text":"Hallo ","extra":[{"text":"Welt"}]}}`)
	r, err := pingServer(mc.addr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if r.VersionName != "1.21.4" || r.Online != 2 || r.Max != 20 || r.MOTD != "Hallo Welt" || len(r.Sample) != 2 || r.LatencyMs < 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := pingServer("127.0.0.1:1", 500*time.Millisecond); err == nil {
		t.Fatal("Fehler erwartet")
	}
}

func TestRCON(t *testing.T) {
	f := newFakeRCON(t, "geheim")
	f.set("list", "There are 0 of a max of 20 players online:")
	big := ""
	for i := 0; i < 1000; i++ {
		big += "0123456789"
	}
	f.set("big", big) // 10.000 Byte -> 3 Pakete
	r := newRCON(f.addr(), func() string { return "geheim" })
	out, err := r.Exec("list")
	if err != nil || out != "There are 0 of a max of 20 players online:" {
		t.Fatalf("%q %v", out, err)
	}
	out, err = r.Exec("big")
	if err != nil || out != big {
		t.Fatalf("mehrteilige Antwort: len=%d err=%v", len(out), err)
	}
	f.mu.Lock()
	conns := f.conns
	f.mu.Unlock()
	if conns != 1 {
		t.Fatalf("es soll nur eine Verbindung genutzt werden, waren %d", conns)
	}
	// Falsches Passwort
	bad := newRCON(f.addr(), func() string { return "falsch" })
	if _, err := bad.Exec("list"); err != ErrAuth {
		t.Fatalf("ErrAuth erwartet, war %v", err)
	}
	// Leeres Passwort
	if _, err := newRCON(f.addr(), func() string { return "" }).Exec("list"); err == nil {
		t.Fatal("Fehler bei leerem Passwort erwartet")
	}
	// Befehl mit Zeilenumbruch wird abgelehnt (Injection-Schutz)
	if _, err := r.Exec("list\nstop"); err == nil {
		t.Fatal("Zeilenumbruch muss abgelehnt werden")
	}
}

func TestRCONReconnectAfterServerRestart(t *testing.T) {
	f := newFakeRCON(t, "pw")
	f.set("list", "ok")
	r := newRCON(f.addr(), func() string { return "pw" })
	if _, err := r.Exec("list"); err != nil {
		t.Fatal(err)
	}
	r.conn.Close() // Verbindung bricht weg (Server-Neustart)
	out, err := r.Exec("list")
	if err != nil || out != "ok" {
		t.Fatalf("Wiederverbindung fehlgeschlagen: %q %v", out, err)
	}
}
