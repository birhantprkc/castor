package hls

import (
	"crypto/rand"
	"os"
	"path/filepath"

	"github.com/stupside/castor/e2e/origin"
)

// AES128 encrypts TS segments under one key behind a tokenised URI; ffmpeg refuses to encrypt fMP4.
type AES128 struct{}

func (AES128) Name() string                 { return "hls-ts-aes128" }
func (AES128) Supports(origin.Layout) error { return nil }
func (AES128) Package(dir string, l origin.Layout) origin.Output {
	key, info := filepath.Join(dir, "key.bin"), filepath.Join(dir, "keyinfo")
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	if err := os.WriteFile(key, secret, 0o600); err != nil {
		panic(err)
	}
	// With no IV line ffmpeg derives one itself and writes it into EXT-X-KEY.
	if err := os.WriteFile(info, []byte("key.bin?token=e2e\n"+key+"\n"), 0o600); err != nil {
		panic(err)
	}
	out := pack(dir, l, segments{ext: ".ts", mime: "video/mp2t", args: []string{"-hls_key_info_file", info}})
	out.Types[".bin"] = "application/octet-stream"
	return out
}
