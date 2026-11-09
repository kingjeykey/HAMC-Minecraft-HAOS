package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// ErrAuth wird zurückgegeben, wenn der Server das RCON-Passwort ablehnt.
var ErrAuth = errors.New("RCON-Passwort abgelehnt")

// RCON ist ein minimaler Client für das Source-RCON-Protokoll, wie es Minecraft nutzt.
// Es wird bewusst nur EINE dauerhafte Verbindung gehalten, weil jeder Verbindungsaufbau
// im Server-Log eine Zeile erzeugt.
type RCON struct {
	addr     string
	password func() string
	mu       sync.Mutex
	conn     net.Conn
	nextID   int32
}

func newRCON(addr string, password func() string) *RCON {
	return &RCON{addr: addr, password: password, nextID: 10}
}

func writeRconPacket(c net.Conn, id, typ int32, body string) error {
	buf := make([]byte, 4+4+4+len(body)+2)
	binary.LittleEndian.PutUint32(buf[0:], uint32(4+4+len(body)+2))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	_, err := c.Write(buf)
	return err
}

func readRconPacket(c net.Conn) (id, typ int32, body string, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	n := int32(binary.LittleEndian.Uint32(hdr[:]))
	if n < 10 || n > 1<<16 {
		err = fmt.Errorf("ungültige RCON-Paketlänge %d", n)
		return
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(c, buf); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:]))
	body = string(buf[8 : n-2])
	return
}

func (r *RCON) connect() error {
	pw := r.password()
	if pw == "" {
		return errors.New("kein RCON-Passwort gesetzt")
	}
	c, err := net.DialTimeout("tcp", r.addr, 3*time.Second)
	if err != nil {
		return err
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeRconPacket(c, 1, 3, pw); err != nil {
		c.Close()
		return err
	}
	for {
		id, typ, _, err := readRconPacket(c)
		if err != nil {
			c.Close()
			return err
		}
		if typ == 0 { // manche Server senden vorab ein leeres RESPONSE_VALUE
			continue
		}
		if id == -1 {
			c.Close()
			return ErrAuth
		}
		break
	}
	r.conn = c
	return nil
}

func (r *RCON) close() {
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
}

// Exec führt einen Befehl aus und liefert die (ggf. mehrteilige) Antwort.
func (r *RCON) Exec(cmd string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.ContainsAny(cmd, "\r\n") {
		return "", errors.New("ungültiger Befehl")
	}
	reused := r.conn != nil
	out, sent, err := r.execLocked(cmd)
	// Nur wiederholen, wenn eine bereits offene Verbindung beim Senden/Lesen sofort
	// kaputt war, bevor der Server etwas geantwortet hat (z. B. nach Server-Neustart).
	if err != nil && reused && !sent {
		out, _, err = r.execLocked(cmd)
	}
	return out, err
}

func (r *RCON) execLocked(cmd string) (out string, gotReply bool, err error) {
	if r.conn == nil {
		if err = r.connect(); err != nil {
			return "", false, err
		}
	}
	c := r.conn
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	idA := r.nextID
	idB := idA + 1
	r.nextID += 2
	if r.nextID > 1<<30 {
		r.nextID = 10
	}
	if err = writeRconPacket(c, idA, 2, cmd); err != nil {
		r.close()
		return "", false, err
	}
	// Zweites, leeres Paket als Endemarke: der Server beantwortet es nach der Befehlsantwort.
	if err = writeRconPacket(c, idB, 0, ""); err != nil {
		r.close()
		return "", false, err
	}
	var sb strings.Builder
	for {
		id, _, body, e := readRconPacket(c)
		if e != nil {
			r.close()
			return sb.String(), gotReply, e
		}
		gotReply = true
		if id == idB {
			break
		}
		if id == idA {
			sb.WriteString(body)
		}
	}
	return sb.String(), true, nil
}

// Close beendet die Verbindung.
func (r *RCON) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.close()
}
