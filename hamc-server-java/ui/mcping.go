package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// PingResult ist das Ergebnis des Minecraft-"Server List Ping" (Status-Abfrage).
type PingResult struct {
	VersionName string
	Protocol    int
	Online, Max int
	Sample      []string
	MOTD        string
	LatencyMs   float64
}

func writeVarInt(b *bytes.Buffer, v int32) {
	u := uint32(v)
	for {
		if u&^0x7F == 0 {
			b.WriteByte(byte(u))
			return
		}
		b.WriteByte(byte(u&0x7F) | 0x80)
		u >>= 7
	}
}

func readVarInt(r io.ByteReader) (int32, error) {
	var res uint32
	for i := 0; i < 5; i++ {
		c, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		res |= uint32(c&0x7F) << (7 * uint(i))
		if c&0x80 == 0 {
			return int32(res), nil
		}
	}
	return 0, errors.New("VarInt zu lang")
}

func sendPacket(w io.Writer, payload []byte) error {
	var b bytes.Buffer
	writeVarInt(&b, int32(len(payload)))
	b.Write(payload)
	_, err := w.Write(b.Bytes())
	return err
}

func readPacket(r *bufio.Reader) ([]byte, error) {
	n, err := readVarInt(r)
	if err != nil {
		return nil, err
	}
	if n < 1 || n > 4<<20 {
		return nil, fmt.Errorf("ungültige Paketlänge %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func flattenChat(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var sb strings.Builder
		for _, e := range t {
			sb.WriteString(flattenChat(e))
		}
		return sb.String()
	case map[string]any:
		var sb strings.Builder
		if s, ok := t["text"].(string); ok {
			sb.WriteString(s)
		}
		if ex, ok := t["extra"]; ok {
			sb.WriteString(flattenChat(ex))
		}
		return sb.String()
	}
	return ""
}

func pingServer(addr string, timeout time.Duration) (*PingResult, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portStr)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	br := bufio.NewReader(conn)

	var hs bytes.Buffer
	writeVarInt(&hs, 0x00)
	writeVarInt(&hs, 767)
	writeVarInt(&hs, int32(len(host)))
	hs.WriteString(host)
	_ = binary.Write(&hs, binary.BigEndian, uint16(port))
	writeVarInt(&hs, 1)
	if err := sendPacket(conn, hs.Bytes()); err != nil {
		return nil, err
	}
	t0 := time.Now()
	if err := sendPacket(conn, []byte{0x00}); err != nil {
		return nil, err
	}
	pkt, err := readPacket(br)
	if err != nil {
		return nil, err
	}
	rtt := time.Since(t0)
	pr := bytes.NewReader(pkt)
	id, err := readVarInt(pr)
	if err != nil || id != 0x00 {
		return nil, errors.New("unerwartete Status-Antwort")
	}
	sl, err := readVarInt(pr)
	if err != nil || int(sl) > pr.Len() || sl < 0 {
		return nil, errors.New("ungültige Status-Antwort")
	}
	js := make([]byte, sl)
	_, _ = io.ReadFull(pr, js)

	var st struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int    `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
			Sample []struct {
				Name string `json:"name"`
			} `json:"sample"`
		} `json:"players"`
		Description any `json:"description"`
	}
	if err := json.Unmarshal(js, &st); err != nil {
		return nil, err
	}
	res := &PingResult{
		VersionName: stripCodes(st.Version.Name), Protocol: st.Version.Protocol,
		Online: st.Players.Online, Max: st.Players.Max,
		MOTD:      strings.TrimSpace(stripCodes(flattenChat(st.Description))),
		LatencyMs: float64(rtt.Microseconds()) / 1000,
	}
	for _, p := range st.Players.Sample {
		res.Sample = append(res.Sample, p.Name)
	}

	// Ping/Pong für die Latenz; bei Fehler bleibt die Status-Laufzeit stehen.
	var pg bytes.Buffer
	writeVarInt(&pg, 0x01)
	_ = binary.Write(&pg, binary.BigEndian, time.Now().UnixMilli())
	t1 := time.Now()
	if sendPacket(conn, pg.Bytes()) == nil {
		if p2, err := readPacket(br); err == nil && len(p2) > 0 && p2[0] == 0x01 {
			res.LatencyMs = float64(time.Since(t1).Microseconds()) / 1000
		}
	}
	return res, nil
}
