// Package callprobe implements the synthetic call probe: an RTP-shaped
// UDP stream (small packets at a constant packet rate) sent to an
// off-network reflector that echoes each packet back. Unlike ICMP, this
// traffic is indistinguishable from a real video call to middleboxes, so
// the loss/jitter/freezes it measures are the ones calls actually suffer.
//
// Protocol v2 (anti-spoofing handshake):
//
//	sender → reflector  HELLO (172 bytes, padded)
//	reflector → sender  TOKEN (12 bytes, HMAC of the observed source)
//	sender → reflector  PROBE (172 bytes, carries the token) → echoed
//
// The token only ever reaches the true owner of the source address, so a
// spoofed source can never produce echoes: the reflector cannot be used
// to direct traffic at third parties. Replies are never larger than
// requests (no amplification).
package callprobe

import "encoding/binary"

// PacketSize mimics a 20ms G.711 RTP packet: 12 bytes RTP header +
// 160 bytes payload. Sent at 50pps this is ~9KB/s each way.
const PacketSize = 172

// TokenLen is the size of the per-source handshake token.
const TokenLen = 8

var (
	probeMagic = [4]byte{'P', 'W', 'C', 2}
	helloMagic = [4]byte{'P', 'W', 'H', 2}
	tokenMagic = [4]byte{'P', 'W', 'T', 2}
)

const probeHeaderSize = 4 + TokenLen + 4 + 8 // magic + token + seq + send-unixnano

// TokenReplySize is the size of the reflector's TOKEN reply — much
// smaller than the HELLO that solicits it, so the handshake attenuates
// rather than amplifies.
const TokenReplySize = 4 + TokenLen

// MarshalProbe fills buf (PacketSize bytes) with a probe packet.
func MarshalProbe(buf []byte, token [TokenLen]byte, seq uint32, sendNano int64) {
	copy(buf[0:4], probeMagic[:])
	copy(buf[4:4+TokenLen], token[:])
	binary.BigEndian.PutUint32(buf[12:16], seq)
	binary.BigEndian.PutUint64(buf[16:24], uint64(sendNano))
	for i := probeHeaderSize; i < PacketSize; i++ {
		buf[i] = 0
	}
}

// UnmarshalProbe validates and extracts a probe packet (sender side,
// parsing its own echoed packet — the token is not re-checked).
func UnmarshalProbe(b []byte) (seq uint32, sendNano int64, ok bool) {
	if !IsProbe(b) {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(b[12:16]), int64(binary.BigEndian.Uint64(b[16:24])), true
}

// IsProbe reports whether b is shaped like a v2 probe packet.
func IsProbe(b []byte) bool {
	return len(b) == PacketSize && [4]byte(b[0:4]) == probeMagic
}

// ProbeToken extracts the token from a probe packet (reflector side).
func ProbeToken(b []byte) []byte { return b[4 : 4+TokenLen] }

// MarshalHello fills buf (PacketSize bytes) with a HELLO. It is padded
// to full probe size so the TOKEN reply is strictly smaller.
func MarshalHello(buf []byte) {
	copy(buf[0:4], helloMagic[:])
	for i := 4; i < PacketSize; i++ {
		buf[i] = 0
	}
}

// IsHello reports whether b is a HELLO.
func IsHello(b []byte) bool {
	return len(b) == PacketSize && [4]byte(b[0:4]) == helloMagic
}

// MarshalToken builds the reflector's TOKEN reply.
func MarshalToken(token [TokenLen]byte) []byte {
	out := make([]byte, TokenReplySize)
	copy(out[0:4], tokenMagic[:])
	copy(out[4:], token[:])
	return out
}

// UnmarshalToken extracts the token from a TOKEN reply.
func UnmarshalToken(b []byte) (token [TokenLen]byte, ok bool) {
	if len(b) != TokenReplySize || [4]byte(b[0:4]) != tokenMagic {
		return token, false
	}
	copy(token[:], b[4:])
	return token, true
}
