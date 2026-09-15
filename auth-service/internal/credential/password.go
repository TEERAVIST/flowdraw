package credential

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

type Parameters struct {
	Version uint32
	Memory  uint32
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

var DefaultParameters = Parameters{Version: 1, Memory: 64 * 1024, Time: 3, Threads: 2, SaltLen: 16, KeyLen: 32}

func Hash(password string, p Parameters) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must contain 12 to 1024 bytes")
	}
	if p.Version != 1 || p.Time < 1 || p.Time > 10 || p.Threads < 1 || p.Threads > 16 || p.Memory < 8*uint32(p.Threads) || p.Memory > 256*1024 || p.SaltLen < 16 || p.SaltLen > 64 || p.KeyLen < 32 || p.KeyLen > 64 {
		return "", errors.New("invalid password parameters")
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$app=%d$m=%d,t=%d,p=%d$%s$%s", p.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func Verify(encoded, password string) (bool, error) {
	if len(encoded) > 512 {
		return false, errors.New("invalid password hash")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 7 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "app=1" {
		return false, errors.New("invalid password hash")
	}
	var p Parameters
	if _, err := fmt.Sscanf(parts[3], "app=%d", &p.Version); err != nil {
		return false, err
	}
	var threads uint64
	fields := strings.Split(parts[4], ",")
	if len(fields) != 3 || !strings.HasPrefix(fields[0], "m=") || !strings.HasPrefix(fields[1], "t=") || !strings.HasPrefix(fields[2], "p=") {
		return false, errors.New("invalid password parameters")
	}
	memory, err := strconv.ParseUint(strings.TrimPrefix(fields[0], "m="), 10, 32)
	if err != nil {
		return false, err
	}
	timeCost, err := strconv.ParseUint(strings.TrimPrefix(fields[1], "t="), 10, 32)
	if err != nil {
		return false, err
	}
	threads, err = strconv.ParseUint(strings.TrimPrefix(fields[2], "p="), 10, 8)
	if err != nil {
		return false, err
	}
	if p.Version != 1 || memory < 8*threads || memory > 256*1024 || timeCost < 1 || timeCost > 10 || threads < 1 || threads > 16 || len(password) > 1024 {
		return false, errors.New("password parameters exceed limits")
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	expected, err := b64.DecodeString(parts[6])
	if err != nil {
		return false, err
	}
	if len(salt) < 16 || len(salt) > 64 || len(expected) < 32 || len(expected) > 64 {
		return false, errors.New("invalid salt or key length")
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(timeCost), uint32(memory), uint8(threads), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func NeedsRehash(encoded string) bool {
	p := DefaultParameters
	return !strings.HasPrefix(encoded, fmt.Sprintf("$argon2id$v=19$app=%d$m=%d,t=%d,p=%d$", p.Version, p.Memory, p.Time, p.Threads))
}
