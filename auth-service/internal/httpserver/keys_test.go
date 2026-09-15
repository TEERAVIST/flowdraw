package httpserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/go-jose/go-jose/v3"
	"os"
	"path/filepath"
	"testing"
)

func TestSigningKeyOverlap(t *testing.T) {
	dir := t.TempDir()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	old, _ := rsa.GenerateKey(rand.Reader, 2048)
	overlap := filepath.Join(dir, "previous.json")
	data, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &old.PublicKey, KeyID: "old", Algorithm: "RS256", Use: "sig"}}})
	_ = os.WriteFile(overlap, data, 0600)
	keys, err := LoadKeys(path, "new", overlap)
	if err != nil || len(keys.Public.Keys) != 2 {
		t.Fatal(err)
	}
	for _, k := range keys.Public.Keys {
		if !k.IsPublic() {
			t.Fatal("private JWKS")
		}
	}
	if _, err = LoadKeys(path, "old", overlap); err == nil {
		t.Fatal("duplicate kid accepted")
	}
}
