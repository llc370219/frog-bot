package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureCert 读取或自动生成自签名 ECDSA P-256 TLS 证书，并返回证书与 DER SHA-256 十六进制指纹。
func EnsureCert(certPath, keyPath string) (tls.Certificate, string, error) {
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && len(cert.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil && time.Now().Add(7*24*time.Hour).Before(leaf.NotAfter) {
			sum := sha256.Sum256(cert.Certificate[0])
			return cert, hex.EncodeToString(sum[:]), nil
		}
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("生成密钥失败: %w", err)
	}
	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "frog-remote"},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "frog.local"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("签发自签名证书失败: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	return cert, hex.EncodeToString(sum[:]), nil
}

// NewPinnedTLSConfig 生成仅信任指定叶证书 SHA-256 指纹的 TLS 1.3 客户端配置（防中间人攻击，无需公网域名与 CA）。
func NewPinnedTLSConfig(pinHex string, nextProtos []string) (*tls.Config, error) {
	want, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(pinHex)))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("无效的 TLS 证书指纹")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // 由下方 VerifyConnection 强制比对叶证书 SHA-256 指纹
		NextProtos:         nextProtos,
		ClientSessionCache: tls.NewLRUClientSessionCache(32),
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("对端未提供 TLS 证书")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return errors.New("TLS 证书指纹不匹配，已拦截可疑连接")
			}
			return nil
		},
	}, nil
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// detectAdvertiseIP 尝试探测本机对外通信的首选 IP（用于直连模式或中转服务启动提示）。
func detectAdvertiseIP() string {
	conn, err := net.DialTimeout("udp", "1.1.1.1:80", 800*time.Millisecond)
	if err == nil {
		defer conn.Close()
		if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok && udp.IP != nil && !udp.IP.IsLoopback() {
			return udp.IP.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
