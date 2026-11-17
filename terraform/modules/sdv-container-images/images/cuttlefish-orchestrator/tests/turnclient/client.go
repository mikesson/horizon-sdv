// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package turnclient is a minimal STUN/TURN client for exercising a real relay.
//
// It exists because verifying the relay means being a TURN client, and the
// alternative (a full WebRTC stack) would test the stack rather than the
// relay. Only what a relay conversation needs is implemented: the long-term
// credential handshake, Allocate, CreatePermission, Send, and Data.
//
// Everything here speaks the wire format directly, so a change in how the
// orchestrator derives keys shows up as a failed MESSAGE-INTEGRITY rather than
// as agreement between two copies of the same helper.
package turnclient

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // MD5 is the RFC 5389 long-term key digest.
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is the RFC 5766 REST API and MESSAGE-INTEGRITY digest.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const magicCookie = 0x2112A442

// Message types (method | class).
const (
	allocateReq       = 0x0003
	allocateSuccess   = 0x0103
	allocateErr       = 0x0113
	createPermReq     = 0x0008
	createPermSuccess = 0x0108
	createPermErr     = 0x0118
	sendIndication    = 0x0016
	dataIndication    = 0x0017
)

// Attributes.
const (
	attrUsername           = 0x0006
	attrMessageIntegrity   = 0x0008
	attrErrorCode          = 0x0009
	attrRealm              = 0x0014
	attrNonce              = 0x0015
	attrXorPeerAddress     = 0x0012
	attrData               = 0x0013
	attrXorRelayedAddress  = 0x0016
	attrRequestedTransport = 0x0019
	attrLifetime           = 0x000D
)

type msg struct {
	typ   uint16
	txID  [12]byte
	attrs []attr
}

type attr struct {
	typ uint16
	val []byte
}

func (m *msg) get(t uint16) []byte {
	for _, a := range m.attrs {
		if a.typ == t {
			return a.val
		}
	}
	return nil
}

// encode serializes the message, appending MESSAGE-INTEGRITY when key != nil.
// encode serializes the message, appending MESSAGE-INTEGRITY when a key is
// given. Messages here are a few hundred bytes, so lengths always fit a uint16.
func (m *msg) encode(key []byte) []byte {
	var body []byte
	for _, a := range m.attrs {
		body = appendAttr(body, a.typ, a.val)
	}
	out := make([]byte, 20, 20+len(body)+24)
	binary.BigEndian.PutUint16(out[0:], m.typ)
	binary.BigEndian.PutUint32(out[4:], magicCookie)
	copy(out[8:], m.txID[:])
	if key == nil {
		binary.BigEndian.PutUint16(out[2:], uint16(len(body))) //nolint:gosec // Bounded by construction.
		return append(out, body...)
	}
	// The length must cover the MESSAGE-INTEGRITY attribute (4 + 20) before
	// the HMAC is computed over it.
	binary.BigEndian.PutUint16(out[2:], uint16(len(body)+24)) //nolint:gosec // Bounded by construction.
	out = append(out, body...)
	mac := hmac.New(sha1.New, key)
	mac.Write(out)
	return appendAttr(out, attrMessageIntegrity, mac.Sum(nil))
}

// appendAttr appends one type-length-value attribute, padded to four bytes.
func appendAttr(b []byte, t uint16, v []byte) []byte {
	var h [4]byte
	binary.BigEndian.PutUint16(h[0:], t)
	binary.BigEndian.PutUint16(h[2:], uint16(len(v))) //nolint:gosec // Attribute values are bounded by construction.
	b = append(b, h[:]...)
	b = append(b, v...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func decode(b []byte) (*msg, error) {
	if len(b) < 20 {
		return nil, errors.New("short message")
	}
	m := &msg{typ: binary.BigEndian.Uint16(b[0:])}
	copy(m.txID[:], b[8:20])
	n := int(binary.BigEndian.Uint16(b[2:]))
	if 20+n > len(b) {
		return nil, fmt.Errorf("truncated: want %d have %d", 20+n, len(b))
	}
	p := b[20 : 20+n]
	for len(p) >= 4 {
		t := binary.BigEndian.Uint16(p[0:])
		l := int(binary.BigEndian.Uint16(p[2:]))
		if 4+l > len(p) {
			break
		}
		m.attrs = append(m.attrs, attr{typ: t, val: append([]byte(nil), p[4:4+l]...)})
		adv := 4 + l
		for adv%4 != 0 {
			adv++
		}
		if adv > len(p) {
			break
		}
		p = p[adv:]
	}
	return m, nil
}

func xorAddr(val []byte) (net.IP, int, error) {
	if len(val) < 8 {
		return nil, 0, errors.New("bad xor-address")
	}
	port := int(binary.BigEndian.Uint16(val[2:]) ^ uint16(magicCookie>>16))
	ip := make(net.IP, 4)
	ck := make([]byte, 4)
	binary.BigEndian.PutUint32(ck, magicCookie)
	for i := range 4 {
		ip[i] = val[4+i] ^ ck[i]
	}
	return ip, port, nil
}

func encXorAddr(ip net.IP, port int) []byte {
	v := make([]byte, 8)
	v[1] = 1                                                                // IPv4
	binary.BigEndian.PutUint16(v[2:], uint16(port)^uint16(magicCookie>>16)) //nolint:gosec // Port is a uint16 by definition.
	ck := make([]byte, 4)
	binary.BigEndian.PutUint32(ck, magicCookie)
	ip4 := ip.To4()
	for i := range 4 {
		v[4+i] = ip4[i] ^ ck[i]
	}
	return v
}

// Conn is a transport carrying STUN messages.
type Conn interface {
	send([]byte) error
	recv(time.Duration) ([]byte, error)
	Close() error
}

type udpConn struct{ c *net.UDPConn }

func (u *udpConn) send(b []byte) error {
	if _, err := u.c.Write(b); err != nil {
		return fmt.Errorf("writing to the relay: %w", err)
	}
	return nil
}

func (u *udpConn) recv(d time.Duration) ([]byte, error) {
	_ = u.c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	n, err := u.c.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("reading from the relay: %w", err)
	}
	return buf[:n], nil
}

// Close releases the transport.
func (u *udpConn) Close() error {
	if err := u.c.Close(); err != nil {
		return fmt.Errorf("closing the transport: %w", err)
	}
	return nil
}

// DialUDP opens a UDP transport to a relay.
func DialUDP(addr string) (Conn, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", addr, err)
	}
	c, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}
	return &udpConn{c}, nil
}

func newTxID() [12]byte {
	var id [12]byte
	_, _ = rand.Read(id[:])
	return id
}

// Credential computes the TURN REST API password for a username: the base64
// HMAC-SHA1 of the username under the shared secret.
func Credential(secret, username string) string {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// LongTermKey derives the RFC 5389 long-term key a relay checks
// MESSAGE-INTEGRITY against.
func LongTermKey(username, realm, password string) []byte {
	s := md5.Sum([]byte(username + ":" + realm + ":" + password)) //nolint:gosec // Fixed by RFC 5389.
	return s[:]
}

// Session is an open TURN allocation.
type Session struct {
	c        Conn
	username string
	password string
	realm    string
	nonce    []byte
	relayIP  net.IP
	relayPrt int
}

// RelayIP is the relayed address the relay allocated for this session.
func (s *Session) RelayIP() net.IP { return s.relayIP }

// RelayPort is the relayed port the relay allocated for this session.
func (s *Session) RelayPort() int { return s.relayPrt }

// Allocate performs the unauthenticated probe that draws a realm and nonce,
// then the authenticated Allocate that opens the relayed address.
func Allocate(c Conn, username, password string) (*Session, error) {
	m := &msg{typ: allocateReq, txID: newTxID()}
	m.attrs = append(m.attrs, attr{attrRequestedTransport, []byte{17, 0, 0, 0}})
	if err := c.send(m.encode(nil)); err != nil {
		return nil, err
	}
	raw, err := c.recv(3 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("no response to unauthenticated allocate: %w", err)
	}
	resp, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if resp.typ != allocateErr {
		return nil, fmt.Errorf("expected 401 challenge, got type 0x%04x", resp.typ)
	}
	realm := string(resp.get(attrRealm))
	nonce := resp.get(attrNonce)
	if realm == "" || nonce == nil {
		return nil, errors.New("challenge missing realm/nonce")
	}

	s := &Session{c: c, username: username, password: password, realm: realm, nonce: nonce}
	key := LongTermKey(username, realm, password)

	m2 := &msg{typ: allocateReq, txID: newTxID()}
	m2.attrs = append(m2.attrs,
		attr{attrRequestedTransport, []byte{17, 0, 0, 0}},
		attr{attrUsername, []byte(username)},
		attr{attrRealm, []byte(realm)},
		attr{attrNonce, nonce},
	)
	if err := c.send(m2.encode(key)); err != nil {
		return nil, err
	}
	raw2, err := c.recv(3 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("no response to authenticated allocate: %w", err)
	}
	resp2, err := decode(raw2)
	if err != nil {
		return nil, err
	}
	if resp2.typ != allocateSuccess {
		code := "?"
		if ec := resp2.get(attrErrorCode); len(ec) >= 4 {
			code = fmt.Sprintf("%d%02d", ec[2], ec[3])
		}
		return nil, fmt.Errorf("allocate rejected: type=0x%04x code=%s", resp2.typ, code)
	}
	ip, port, err := xorAddr(resp2.get(attrXorRelayedAddress))
	if err != nil {
		return nil, err
	}
	s.relayIP, s.relayPrt = ip, port
	return s, nil
}

// CreatePermission authorizes a peer to reach the relayed address.
func (s *Session) CreatePermission(peerIP net.IP, peerPort int) error {
	key := LongTermKey(s.username, s.realm, s.password)
	m := &msg{typ: createPermReq, txID: newTxID()}
	m.attrs = append(m.attrs,
		attr{attrXorPeerAddress, encXorAddr(peerIP, peerPort)},
		attr{attrUsername, []byte(s.username)},
		attr{attrRealm, []byte(s.realm)},
		attr{attrNonce, s.nonce},
	)
	if err := s.c.send(m.encode(key)); err != nil {
		return err
	}
	raw, err := s.c.recv(3 * time.Second)
	if err != nil {
		return err
	}
	resp, err := decode(raw)
	if err != nil {
		return err
	}
	if resp.typ != createPermSuccess {
		return fmt.Errorf("create-permission rejected: type=0x%04x", resp.typ)
	}
	return nil
}

// SendTo relays a payload to a peer.
func (s *Session) SendTo(peerIP net.IP, peerPort int, data []byte) error {
	m := &msg{typ: sendIndication, txID: newTxID()}
	m.attrs = append(m.attrs,
		attr{attrXorPeerAddress, encXorAddr(peerIP, peerPort)},
		attr{attrData, data},
	)
	return s.c.send(m.encode(nil))
}

// AwaitData waits for a payload relayed from a peer.
func (s *Session) AwaitData(d time.Duration) ([]byte, int, error) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		raw, err := s.c.recv(time.Until(deadline))
		if err != nil {
			return nil, 0, err
		}
		m, err := decode(raw)
		if err != nil {
			continue
		}
		if m.typ != dataIndication {
			continue
		}
		_, port, _ := xorAddr(m.get(attrXorPeerAddress))
		return m.get(attrData), port, nil
	}
	return nil, 0, errors.New("timeout waiting for data indication")
}
