package follow

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"testing"
)

func TestAKeyThatIsNotTheOneThatSealedItIsCaught(t *testing.T) {
	secret, iv := bytes.Repeat([]byte{7}, 16), bytes.Repeat([]byte{9}, 16)
	block, _ := aes.NewCipher(secret)
	sealed := make([]byte, 32)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(sealed, append([]byte("sixteen bytes!!!"), bytes.Repeat([]byte{16}, 16)...))
	if _, err := decrypted(sealed, bytes.Repeat([]byte{8}, 16), fmt.Sprintf("0x%x", iv)); err == nil {
		t.Error("decrypted with the wrong key as if it were right")
	}
	if clear, err := decrypted(sealed, secret, fmt.Sprintf("0x%x", iv)); err != nil || string(clear) != "sixteen bytes!!!" {
		t.Errorf("decrypted = %q (%v), want the sealed text", clear, err)
	}
}
