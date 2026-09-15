package httpserver

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/go-jose/go-jose/v3"
	"os"
)

type Keys struct {
	Active jose.JSONWebKey
	Public jose.JSONWebKeySet
}

// LoadKeys retains only public overlap keys, never exposing private material in JWKS.
func LoadKeys(path, kid, overlapPath string) (Keys, error) {
	var keys Keys
	if kid == "" {
		return keys, errors.New("signing kid is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return keys, err
	}
	block, rest := pem.Decode(data)
	if block == nil || len(rest) != 0 {
		return keys, errors.New("expected one PEM private key")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return keys, errors.New("RSA signing key must be at least 2048 bits")
	}
	if err = key.Validate(); err != nil {
		return keys, err
	}
	keys.Active = jose.JSONWebKey{Key: key, KeyID: kid, Algorithm: "RS256", Use: "sig"}
	keys.Public.Keys = append(keys.Public.Keys, keys.Active.Public())
	if overlapPath != "" {
		data, err = os.ReadFile(overlapPath)
		if err != nil {
			return keys, err
		}
		var previous jose.JSONWebKeySet
		if err = json.Unmarshal(data, &previous); err != nil {
			return keys, err
		}
		seen := map[string]bool{kid: true}
		for _, k := range previous.Keys {
			public, ok := k.Key.(*rsa.PublicKey)
			if !ok || public.N.BitLen() < 2048 || k.KeyID == "" || seen[k.KeyID] || k.Algorithm != "RS256" || k.Use != "sig" {
				return keys, errors.New("invalid or duplicate overlap public key")
			}
			seen[k.KeyID] = true
			keys.Public.Keys = append(keys.Public.Keys, k)
		}
	}
	return keys, nil
}
