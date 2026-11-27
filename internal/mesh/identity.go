// Package mesh implements FrogLink Mesh: Ed25519 one-click cross-server pairing,
// public-node automatic NAT WebSocket relay with end-to-end X25519/AES-256-GCM envelopes,
// cache-safe static peer directory vs RAM-only dynamic heartbeats, and taint-preserving
// `bot@node` cross-node dispatch.
package mesh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frogbot/internal/atomicfile"
)

// NodeIdentity stores a Host node's persistent Ed25519 (signing) and X25519 (E2EE) keys.
type NodeIdentity struct {
	NodeID     string `json:"node_id"`
	Ed25519Pub string `json:"ed25519_pub"`
	Ed25519Key string `json:"ed25519_key"`
	X25519Pub  string `json:"x25519_pub"`
	X25519Key  string `json:"x25519_key"`

	edPriv ed25519.PrivateKey
	edPub  ed25519.PublicKey
	xPriv  *ecdh.PrivateKey
	xPub   *ecdh.PublicKey
}

// PeerRecord stores static trusted peer metadata in /data/host/topology.json.
type PeerRecord struct {
	NodeID     string    `json:"node_id"`
	Name       string    `json:"name"`
	NetworkTag string    `json:"network_tag,omitempty"` // e.g. "[家庭宽带IP]"
	PublicURL  string    `json:"public_url,omitempty"`  // non-empty for public relay-capable nodes
	Ed25519Pub string    `json:"ed25519_pub"`
	X25519Pub  string    `json:"x25519_pub"`
	Bots       []string  `json:"bots,omitempty"`
	RelayVia   string    `json:"relay_via,omitempty"`
	PairedAt   time.Time `json:"paired_at"`
}

// EncryptedEnvelope carries an end-to-end encrypted, Ed25519-signed cross-node message.
// Relay nodes can inspect FromNode/ToNode for routing but NEVER decrypt Ciphertext.
type EncryptedEnvelope struct {
	ID         string `json:"id"`
	FromNode   string `json:"from_node"`
	ToNode     string `json:"to_node"`
	Kind       string `json:"kind"` // "peer_message" | "gateway_proxy"
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	Signature  string `json:"signature"`
	Timestamp  int64  `json:"timestamp"`
}

// LoadOrCreateIdentity loads <hostDir>/node_key.json or generates a fresh Ed25519+X25519 identity.
func LoadOrCreateIdentity(path, preferredName string) (*NodeIdentity, error) {
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		var id NodeIdentity
		if err := json.Unmarshal(raw, &id); err != nil {
			return nil, fmt.Errorf("parse node_key.json: %w", err)
		}
		if err := id.hydrate(); err != nil {
			return nil, err
		}
		return &id, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	xPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	slug := sanitizeNodeSlug(preferredName)
	if slug == "" {
		sum := sha256.Sum256(edPub)
		slug = "node-" + hex.EncodeToString(sum[:4])
	}
	id := &NodeIdentity{
		NodeID:     slug,
		Ed25519Pub: base64.RawStdEncoding.EncodeToString(edPub),
		Ed25519Key: base64.RawStdEncoding.EncodeToString(edPriv),
		X25519Pub:  base64.RawStdEncoding.EncodeToString(xPriv.PublicKey().Bytes()),
		X25519Key:  base64.RawStdEncoding.EncodeToString(xPriv.Bytes()),
		edPriv:     edPriv,
		edPub:      edPub,
		xPriv:      xPriv,
		xPub:       xPriv.PublicKey(),
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

func (id *NodeIdentity) hydrate() error {
	edPrivB, err := base64.RawStdEncoding.DecodeString(id.Ed25519Key)
	if err != nil || len(edPrivB) != ed25519.PrivateKeySize {
		return errors.New("无效的 Ed25519 私钥")
	}
	edPubB, err := base64.RawStdEncoding.DecodeString(id.Ed25519Pub)
	if err != nil || len(edPubB) != ed25519.PublicKeySize {
		return errors.New("无效的 Ed25519 公钥")
	}
	xPrivB, err := base64.RawStdEncoding.DecodeString(id.X25519Key)
	if err != nil {
		return errors.New("无效的 X25519 私钥")
	}
	xPriv, err := ecdh.X25519().NewPrivateKey(xPrivB)
	if err != nil {
		return err
	}
	id.edPriv = ed25519.PrivateKey(edPrivB)
	id.edPub = ed25519.PublicKey(edPubB)
	id.xPriv = xPriv
	id.xPub = xPriv.PublicKey()
	return nil
}

// Sign produces a base64 Ed25519 signature over message.
func (id *NodeIdentity) Sign(msg []byte) string {
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(id.edPriv, msg))
}

// VerifyPeerSignature verifies a base64 Ed25519 signature against peerPubB64.
func VerifyPeerSignature(peerPubB64 string, msg []byte, sigB64 string) bool {
	pub, err := base64.RawStdEncoding.DecodeString(peerPubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.RawStdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

func (id *NodeIdentity) derivePairKey(peerX25519PubB64 string) ([]byte, error) {
	pubB, err := base64.RawStdEncoding.DecodeString(peerX25519PubB64)
	if err != nil {
		return nil, err
	}
	peerPub, err := ecdh.X25519().NewPublicKey(pubB)
	if err != nil {
		return nil, err
	}
	shared, err := id.xPriv.ECDH(peerPub)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(append([]byte("froglink-v2-e2ee:"), shared...))
	return h[:], nil
}

// SealEnvelope encrypts plaintext JSON for peer using X25519+AES-256-GCM and signs it with Ed25519.
func (id *NodeIdentity) SealEnvelope(peer PeerRecord, kind string, plaintext []byte) (EncryptedEnvelope, error) {
	key, err := id.derivePairKey(peer.X25519Pub)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	var idB [6]byte
	_, _ = rand.Read(idB[:])
	envID := "env_" + hex.EncodeToString(idB[:])
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return EncryptedEnvelope{}, err
	}
	aad := []byte(fmt.Sprintf("%s->%s:%s:%s", id.NodeID, peer.NodeID, kind, envID))
	ct := aead.Seal(nil, nonce, plaintext, aad)
	env := EncryptedEnvelope{
		ID:         envID,
		FromNode:   id.NodeID,
		ToNode:     peer.NodeID,
		Kind:       kind,
		Nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ct),
		Timestamp:  time.Now().Unix(),
	}
	env.Signature = id.Sign(envelopeSignPayload(env))
	return env, nil
}

// OpenEnvelope verifies the sender's Ed25519 signature and decrypts the E2EE payload.
func (id *NodeIdentity) OpenEnvelope(sender PeerRecord, env EncryptedEnvelope) ([]byte, error) {
	if env.FromNode != sender.NodeID || env.ToNode != id.NodeID {
		return nil, errors.New("信封收发节点不匹配")
	}
	if !VerifyPeerSignature(sender.Ed25519Pub, envelopeSignPayload(env), env.Signature) {
		return nil, errors.New("信封 Ed25519 签名校验失败")
	}
	key, err := id.derivePairKey(sender.X25519Pub)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.RawStdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, err
	}
	ct, err := base64.RawStdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, err
	}
	aad := []byte(fmt.Sprintf("%s->%s:%s:%s", env.FromNode, env.ToNode, env.Kind, env.ID))
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errors.New("信封 AES-GCM 解密校验失败")
	}
	return pt, nil
}

func envelopeSignPayload(e EncryptedEnvelope) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d", e.ID, e.FromNode, e.ToNode, e.Kind, e.Nonce, e.Ciphertext, e.Timestamp))
}

func sanitizeNodeSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else if r == '.' || r == ' ' {
			sb.WriteByte('-')
		}
		if sb.Len() >= 24 {
			break
		}
	}
	return strings.Trim(sb.String(), "-_")
}
