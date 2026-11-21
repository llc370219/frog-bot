//go:build darwin

package remote

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQRDecodesWithAppleVision(t *testing.T) {
	if _, err := exec.LookPath("swift"); err != nil {
		t.Skip("swift not found")
	}
	dir := t.TempDir()
	samples := []string{
		"frogpair://connect?u=https%3A%2F%2Fexample.trycloudflare.com%2Fr%2Fabcdef0123456789abcdef01&t=0123456789abcdef0123456789abcdef&n=MacBook",
		"frogpair://connect?u=https%3A%2F%2F203.0.113.88%3A8443%2Ft%2F0123456789abcdef01234567%2Fr%2Ffedcba9876543210fedcba98&t=abcdef0123456789abcdef0123456789&fp=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855&n=%E5%AE%B6%E9%87%8CMac",
	}

	var pngPaths []string
	for idx, s := range samples {
		grid, err := EncodeQRMatrix([]byte(s))
		if err != nil {
			t.Fatalf("sample %d encode failed: %v", idx, err)
		}
		const scale = 8
		const quiet = 4
		n := len(grid)
		dim := (n + quiet*2) * scale
		img := image.NewGray(image.Rect(0, 0, dim, dim))
		for y := 0; y < dim; y++ {
			for x := 0; x < dim; x++ {
				img.SetGray(x, y, color.Gray{Y: 255})
			}
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				if grid[y][x] {
					for dy := 0; dy < scale; dy++ {
						for dx := 0; dx < scale; dx++ {
							img.SetGray((x+quiet)*scale+dx, (y+quiet)*scale+dy, color.Gray{Y: 0})
						}
					}
				}
			}
		}
		p := filepath.Join(dir, "qr_"+string(rune('a'+idx))+".png")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
		pngPaths = append(pngPaths, p)
	}

	swiftSrc := `import CoreImage
import Foundation
let det = CIDetector(ofType: CIDetectorTypeQRCode, context: nil, options: [CIDetectorAccuracy: CIDetectorAccuracyHigh])!
for path in CommandLine.arguments.dropFirst() {
    let url = URL(fileURLWithPath: path)
    guard let ci = CIImage(contentsOf: url) else { print("ERR:load"); continue }
    let feats = det.features(in: ci) as? [CIQRCodeFeature] ?? []
    print(feats.first?.messageString ?? "NONE")
}
`
	script := filepath.Join(dir, "decode.swift")
	if err := os.WriteFile(script, []byte(swiftSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{script}, pngPaths...)
	out, err := exec.Command("swift", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("swift CIDetector failed: %v\n%s", err, string(out))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != len(samples) {
		t.Fatalf("unexpected lines: %q", string(out))
	}
	for i, want := range samples {
		if strings.TrimSpace(lines[i]) != want {
			t.Fatalf("sample %d mismatch:\ngot:  %q\nwant: %q", i, lines[i], want)
		}
	}
}
