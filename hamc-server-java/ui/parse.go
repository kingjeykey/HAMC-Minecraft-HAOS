package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Parser für Server-Ausgaben. Sie sind bewusst tolerant, weil sich die Formate
// zwischen Vanilla, Paper, Forge usw. unterscheiden. Nicht erkannte Ausgaben
// führen zu "nicht verfügbar", niemals zu einem Fehler.

var (
	reCodes      = regexp.MustCompile("§.|\x1b\\[[0-9;]*[A-Za-z]")
	reList       = regexp.MustCompile(`(?i)There are (\d+)(?: of a max of |/)(\d+) players? online:?\s*(.*)`)
	reTickTarget = regexp.MustCompile(`(?i)Target tick rate:\s*([\d.]+)`)
	reTickAvg    = regexp.MustCompile(`(?i)Average time per tick:\s*([\d.]+)\s*ms`)
	reTickP50    = regexp.MustCompile(`(?i)P50:\s*([\d.]+)\s*ms`)
	reTickP95    = regexp.MustCompile(`(?i)P95:\s*([\d.]+)\s*ms`)
	reTickP99    = regexp.MustCompile(`(?i)P99:\s*([\d.]+)\s*ms`)
	rePaperTPS   = regexp.MustCompile(`(?i)TPS from last[^:]*:\s*\*?([\d.]+)`)
	reMSPT       = regexp.MustCompile(`([\d.]+)/([\d.]+)/([\d.]+)`)
	reForgeAll   = regexp.MustCompile(`(?is)Overall:.*?Mean tick time:\s*([\d.]+)\s*ms.*?Mean TPS:\s*([\d.]+)`)
	reForgeTime  = regexp.MustCompile(`(?i)Mean tick time:\s*([\d.]+)\s*ms`)
	reForgeTPS   = regexp.MustCompile(`(?i)Mean TPS:\s*([\d.]+)`)
)

// TickInfo enthält die erkannten Tick-Werte. Nicht erkannte Werte bleiben nil.
type TickInfo struct {
	TPS  *float64 `json:"tps"`
	MSPT *float64 `json:"mspt"`
	P50  *float64 `json:"p50"`
	P95  *float64 `json:"p95"`
	P99  *float64 `json:"p99"`
}

func stripCodes(s string) string { return reCodes.ReplaceAllString(s, "") }

func isUnknownCommand(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "unknown or incomplete command") ||
		strings.Contains(l, "unknown command") ||
		strings.Contains(l, "incorrect argument")
}

func f64(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func fp(v float64) *float64 { return &v }

func firstFloat(re *regexp.Regexp, s string) *float64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	if v, ok := f64(m[1]); ok {
		return fp(v)
	}
	return nil
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// parseTickQuery: Vanilla/Fabric/Paper ab 1.20.3 ("tick query").
func parseTickQuery(s string) (*TickInfo, bool) {
	s = stripCodes(s)
	avg := firstFloat(reTickAvg, s)
	if avg == nil {
		return nil, false
	}
	t := &TickInfo{MSPT: avg, P50: firstFloat(reTickP50, s), P95: firstFloat(reTickP95, s), P99: firstFloat(reTickP99, s)}
	target := 20.0
	if tr := firstFloat(reTickTarget, s); tr != nil && *tr > 0 {
		target = *tr
	}
	tps := target
	if *avg > 0 && 1000 / *avg < target {
		tps = 1000 / *avg
	}
	t.TPS = fp(round2(tps))
	return t, true
}

// parsePaperTPS: Spigot/Paper/Purpur ("tps").
func parsePaperTPS(s string) (*TickInfo, bool) {
	v := firstFloat(rePaperTPS, stripCodes(s))
	if v == nil {
		return nil, false
	}
	if *v > 20 {
		v = fp(20)
	}
	return &TickInfo{TPS: v}, true
}

// parseMSPT: Paper/Purpur ("mspt"), liefert den Durchschnitt der ersten Zeitspanne.
func parseMSPT(s string) (*float64, bool) {
	m := reMSPT.FindStringSubmatch(stripCodes(s))
	if m == nil {
		return nil, false
	}
	v, ok := f64(m[1])
	if !ok {
		return nil, false
	}
	return fp(v), true
}

// parseForge: Forge/NeoForge ("forge tps" / "neoforge tps").
func parseForge(s string) (*TickInfo, bool) {
	s = stripCodes(s)
	var t, tps *float64
	if m := reForgeAll.FindStringSubmatch(s); m != nil {
		if v, ok := f64(m[1]); ok {
			t = fp(v)
		}
		if v, ok := f64(m[2]); ok {
			tps = fp(v)
		}
	} else {
		t = firstFloat(reForgeTime, s)
		tps = firstFloat(reForgeTPS, s)
	}
	if t == nil && tps == nil {
		return nil, false
	}
	return &TickInfo{TPS: tps, MSPT: t}, true
}

// parsePlayerList: Ausgabe von "list".
func parsePlayerList(s string) (online, max int, names []string, ok bool) {
	m := reList.FindStringSubmatch(stripCodes(s))
	if m == nil {
		return 0, 0, nil, false
	}
	online, _ = strconv.Atoi(m[1])
	max, _ = strconv.Atoi(m[2])
	rest := strings.TrimSpace(m[3])
	// Mehrzeilige Ausgabe (Gruppen) nur in der ersten Zeile auswerten.
	if i := strings.IndexAny(rest, "\r\n"); i >= 0 {
		rest = rest[:i]
	}
	if rest != "" {
		for _, n := range strings.Split(rest, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	return online, max, names, true
}
