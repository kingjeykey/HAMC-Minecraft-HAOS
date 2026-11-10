package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// JarMeta enthält die aus einer Mod-/Plugin-Jar gelesenen Angaben.
type JarMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Kind    string `json:"kind"` // Fabric, Quilt, Forge, NeoForge, Plugin
}

const maxMetaFile = 1 << 20

func readZipFile(zr *zip.ReadCloser, name string) ([]byte, bool) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxMetaFile))
		return b, err == nil
	}
	return nil, false
}

var (
	reTomlKV  = regexp.MustCompile(`^\s*([A-Za-z]+)\s*=\s*["']([^"']*)["']`)
	reYamlKV  = regexp.MustCompile(`^([A-Za-z_-]+)\s*:\s*["']?([^"'#\r\n]*?)["']?\s*(#.*)?$`)
	reManifVr = regexp.MustCompile(`(?m)^Implementation-Version:\s*(\S+)`)
)

func placeholder(s string) bool { return s == "" || strings.Contains(s, "${") }

// readJarMeta liest Fabric, Quilt, Forge, NeoForge und Bukkit/Spigot/Paper. Fehler ergeben leere Felder.
func readJarMeta(abs string) JarMeta {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return JarMeta{}
	}
	defer zr.Close()
	var m JarMeta
	if b, ok := readZipFile(zr, "fabric.mod.json"); ok {
		var j struct{ ID, Name, Version string }
		if json.Unmarshal(b, &j) == nil {
			m = JarMeta{ID: j.ID, Name: j.Name, Version: j.Version, Kind: "Fabric"}
		}
	} else if b, ok := readZipFile(zr, "quilt.mod.json"); ok {
		var j struct {
			QL struct{ ID, Version string } `json:"quilt_loader"`
			MD struct{ Name string }        `json:"metadata"`
		}
		if json.Unmarshal(b, &j) == nil {
			m = JarMeta{ID: j.QL.ID, Name: j.MD.Name, Version: j.QL.Version, Kind: "Quilt"}
		}
	} else if b, ok := readZipFile(zr, "META-INF/neoforge.mods.toml"); ok {
		m = parseModsToml(string(b))
		m.Kind = "NeoForge"
	} else if b, ok := readZipFile(zr, "META-INF/mods.toml"); ok {
		m = parseModsToml(string(b))
		m.Kind = "Forge"
	} else {
		for _, n := range []string{"paper-plugin.yml", "plugin.yml"} {
			if b, ok := readZipFile(zr, n); ok {
				m = parsePluginYml(string(b))
				m.Kind = "Plugin"
				break
			}
		}
	}
	if m.Kind != "" && placeholder(m.Version) {
		if b, ok := readZipFile(zr, "META-INF/MANIFEST.MF"); ok {
			if mm := reManifVr.FindStringSubmatch(string(b)); mm != nil {
				m.Version = mm[1]
			} else if strings.Contains(m.Version, "${") {
				m.Version = ""
			}
		} else if strings.Contains(m.Version, "${") {
			m.Version = ""
		}
	}
	return m
}

func parseModsToml(s string) JarMeta {
	var m JarMeta
	inMods := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[[") {
			if inMods && m.ID != "" {
				break
			}
			inMods = t == "[[mods]]"
			continue
		}
		if !inMods {
			continue
		}
		if kv := reTomlKV.FindStringSubmatch(line); kv != nil {
			switch kv[1] {
			case "modId":
				m.ID = kv[2]
			case "displayName":
				m.Name = kv[2]
			case "version":
				m.Version = kv[2]
			}
		}
	}
	return m
}

func parsePluginYml(s string) JarMeta {
	var m JarMeta
	for _, line := range strings.Split(s, "\n") {
		if kv := reYamlKV.FindStringSubmatch(strings.TrimRight(line, "\r")); kv != nil {
			switch kv[1] {
			case "name":
				m.Name, m.ID = kv[2], strings.ToLower(kv[2])
			case "version":
				m.Version = kv[2]
			}
		}
	}
	return m
}
