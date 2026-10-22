// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

// sealing.go keeps webhook signing secrets encrypted at rest.
//
// A signing secret is a credential: whoever reads it can forge deliveries a
// receiver will trust. So the server seals it with AES-256-GCM before the
// repository stores it, and opens it only to sign. The key is configuration
// ([webhooks] secret_key), never a column beside the secret.
//
// A sealed value reads "enc:v1:<key id>:<base64 nonce+ciphertext>". The key id
// is the first eight hex digits of the key's SHA-256, so a server holding the
// current key and the ones it replaced ([webhooks] previous_secret_keys) opens
// whichever sealed a value. A value without the prefix is a secret stored
// before sealing existed, and is used as it is until the webhook's next
// rotation seals it.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// sealedPrefix starts every sealed secret.
const sealedPrefix = "enc:v1:"

// Sealer encrypts and decrypts webhook signing secrets. A nil Sealer stores
// them as they are.
type Sealer struct {
	keys []sealKey // the first seals; all open
}

type sealKey struct {
	id   string
	aead cipher.AEAD
}

// NewSealer reads the current key and the keys it replaced, each the base64
// of 32 random bytes (openssl rand -base64 32). No current key is no sealer.
func NewSealer(current string, previous []string) (*Sealer, error) {
	if current == "" {
		if len(previous) > 0 {
			return nil, errors.New("webhooks: previous_secret_keys is set but secret_key is not; set secret_key to the key that seals from now on")
		}
		return nil, nil
	}
	s := &Sealer{}
	for i, enc := range append([]string{current}, previous...) {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
		if err != nil || len(raw) != 32 {
			what := "secret_key"
			if i > 0 {
				what = fmt.Sprintf("previous_secret_keys[%d]", i-1)
			}
			return nil, fmt.Errorf("webhooks: %s must be the base64 of 32 bytes (openssl rand -base64 32)", what)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		s.keys = append(s.keys, sealKey{id: hex.EncodeToString(sum[:4]), aead: aead})
	}
	return s, nil
}

// Seal encrypts secret under the current key.
func (s *Sealer) Seal(secret string) (string, error) {
	if s == nil || secret == "" {
		return secret, nil
	}
	k := s.keys[0]
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	box := k.aead.Seal(nonce, nonce, []byte(secret), nil)
	return sealedPrefix + k.id + ":" + base64.StdEncoding.EncodeToString(box), nil
}

// Open decrypts a stored secret. One stored before sealing comes back as it
// is.
func (s *Sealer) Open(stored string) (string, error) {
	rest, ok := strings.CutPrefix(stored, sealedPrefix)
	if !ok {
		return stored, nil
	}
	id, enc, ok := strings.Cut(rest, ":")
	if !ok {
		return "", errors.New("the webhook's signing secret is malformed")
	}
	if s == nil {
		return "", errors.New("the webhook's signing secret is sealed, and this server has no [webhooks] secret_key to open it")
	}
	box, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", errors.New("the webhook's signing secret is malformed")
	}
	for _, k := range s.keys {
		if k.id != id {
			continue
		}
		n := k.aead.NonceSize()
		if len(box) < n {
			return "", errors.New("the webhook's signing secret is malformed")
		}
		plain, err := k.aead.Open(nil, box[:n], box[n:], nil)
		if err != nil {
			return "", errors.New("the webhook's signing secret does not open under its key")
		}
		return string(plain), nil
	}
	return "", fmt.Errorf("the webhook's signing secret was sealed with key %s, which is neither [webhooks] secret_key nor in previous_secret_keys", id)
}
