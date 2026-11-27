package ops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	backupMagic   = "FROGBK02"
	saltSize      = 16
	scryptN       = 32768
	scryptR       = 8
	scryptP       = 1
	scryptKeyLen  = 32
)

// BackupManifest summarizes an encrypted backup or restore operation.
type BackupManifest struct {
	Files       int   `json:"files"`
	PlainBytes  int64 `json:"plain_bytes"`
	CipherBytes int64 `json:"cipher_bytes"`
}

// skipBrowserCacheDirs omits bloated ephemeral Chromium cache blobs while preserving
// Cookies, Local Storage, IndexedDB, Preferences, and session login state.
var skipBrowserCacheDirs = map[string]bool{
	"Cache":            true,
	"Code Cache":       true,
	"GPUCache":         true,
	"GrShaderCache":    true,
	"ShaderCache":      true,
	"Service Worker":   true,
	"Crashpad":         true,
	"BrowserMetrics":   true,
	"OptimizationGuide": true,
}

func shouldIncludeInBackup(rel string, d fs.DirEntry) bool {
	rel = filepath.ToSlash(rel)
	base := d.Name()
	if d.IsDir() {
		if base == "logs" || base == ".logs" || base == "node_modules" || base == ".git" {
			return false
		}
		if strings.Contains(rel, "browser_profile/") && skipBrowserCacheDirs[base] {
			return false
		}
		return true
	}
	if strings.HasPrefix(base, "tmp_") && strings.HasSuffix(base, ".txt") {
		return false
	}
	if strings.HasSuffix(base, ".sock") || strings.HasSuffix(base, ".lock") || base == "SingletonLock" || base == "SingletonSocket" {
		return false
	}
	if strings.HasSuffix(base, ".log") {
		return false
	}
	return true
}

// Backup packs vault.enc, master.key, host/bot configs, USER.md, SOUL.md, MEMORY.md,
// skills/, history.db, node_key.json, topology.json, and critical browser_profile login state
// into an AES-256-GCM + scrypt encrypted archive.
func Backup(dataRoot, outPath, passphrase string) (BackupManifest, error) {
	if len(passphrase) < 6 {
		return BackupManifest{}, errors.New("备份加密口令至少需要 6 位字符")
	}
	var tarBuf bytes.Buffer
	gzw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gzw)

	var manifest BackupManifest
	rootAbs, err := filepath.Abs(dataRoot)
	if err != nil {
		return BackupManifest{}, err
	}

	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == rootAbs {
			return nil
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return nil
		}
		if !shouldIncludeInBackup(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    filepath.ToSlash(rel),
			Mode:    int64(info.Mode().Perm()),
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
		manifest.Files++
		manifest.PlainBytes += int64(len(data))
		return nil
	})
	if err != nil {
		return BackupManifest{}, err
	}
	if err := tw.Close(); err != nil {
		return BackupManifest{}, err
	}
	if err := gzw.Close(); err != nil {
		return BackupManifest{}, err
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return BackupManifest{}, err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return BackupManifest{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return BackupManifest{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return BackupManifest{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return BackupManifest{}, err
	}
	ct := aead.Seal(nil, nonce, tarBuf.Bytes(), []byte(backupMagic))

	var out bytes.Buffer
	out.WriteString(backupMagic)
	out.Write(salt)
	out.Write(nonce)
	out.Write(ct)

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return BackupManifest{}, err
	}
	if err := os.WriteFile(outPath, out.Bytes(), 0o600); err != nil {
		return BackupManifest{}, err
	}
	manifest.CipherBytes = int64(out.Len())
	return manifest, nil
}

// Restore decrypts and unpacks a .frogbak archive into destRoot, rejecting tampered files
// or path traversal entries and restoring strict file permissions.
func Restore(archivePath, destRoot, passphrase string) (BackupManifest, error) {
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		return BackupManifest{}, err
	}
	minLen := len(backupMagic) + saltSize + 12 + 16
	if len(raw) < minLen || string(raw[:len(backupMagic)]) != backupMagic {
		return BackupManifest{}, errors.New("不是有效的 Frog Bot V2 加密备份文件")
	}
	offset := len(backupMagic)
	salt := raw[offset : offset+saltSize]
	offset += saltSize

	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return BackupManifest{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return BackupManifest{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return BackupManifest{}, err
	}
	nonce := raw[offset : offset+aead.NonceSize()]
	offset += aead.NonceSize()
	plainTarGz, err := aead.Open(nil, nonce, raw[offset:], []byte(backupMagic))
	if err != nil {
		return BackupManifest{}, errors.New("备份解密失败：口令错误或备份文件已损坏")
	}

	gzr, err := gzip.NewReader(bytes.NewReader(plainTarGz))
	if err != nil {
		return BackupManifest{}, err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	destAbs, err := filepath.Abs(destRoot)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := os.MkdirAll(destAbs, 0o750); err != nil {
		return BackupManifest{}, err
	}

	var manifest BackupManifest
	manifest.CipherBytes = int64(len(raw))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return BackupManifest{}, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		cleanRel := filepath.Clean(filepath.FromSlash(hdr.Name))
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			return BackupManifest{}, fmt.Errorf("拒绝越界路径: %s", hdr.Name)
		}
		target := filepath.Join(destAbs, cleanRel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return BackupManifest{}, err
		}
		data, err := io.ReadAll(io.LimitReader(tr, 256<<20))
		if err != nil {
			return BackupManifest{}, err
		}
		perm := os.FileMode(hdr.Mode) & 0o777
		if perm == 0 {
			perm = 0o600
		}
		if err := os.WriteFile(target, data, perm); err != nil {
			return BackupManifest{}, err
		}
		manifest.Files++
		manifest.PlainBytes += int64(len(data))
	}
	return manifest, nil
}
