package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// Identity is a persistent non-secret device identity plus its private signing
// key. The private value belongs in the 0600 config store and is never sent on
// the network; pairing records retain only PublicKey.
type Identity struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

func NewIdentity() (Identity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	return Identity{PublicKey: publicKey, PrivateKey: privateKey}, nil
}

func ParseIdentity(publicValue, privateValue string) (Identity, error) {
	publicKey, err := base64.RawStdEncoding.DecodeString(publicValue)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return Identity{}, errors.New("invalid identity public key")
	}
	privateKey, err := base64.RawStdEncoding.DecodeString(privateValue)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return Identity{}, errors.New("invalid identity private key")
	}
	return Identity{PublicKey: ed25519.PublicKey(publicKey), PrivateKey: ed25519.PrivateKey(privateKey)}, nil
}

func (i Identity) PublicString() string  { return base64.RawStdEncoding.EncodeToString(i.PublicKey) }
func (i Identity) PrivateString() string { return base64.RawStdEncoding.EncodeToString(i.PrivateKey) }

func (i Identity) Sign(message []byte) []byte {
	if len(i.PrivateKey) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.Sign(i.PrivateKey, message)
}

func VerifyIdentity(publicValue string, message, signature []byte) bool {
	publicKey, err := base64.RawStdEncoding.DecodeString(publicValue)
	return err == nil && len(publicKey) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(publicKey), message, signature)
}

func ValidPublicKey(value string) bool {
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	return err == nil && len(decoded) == ed25519.PublicKeySize
}
