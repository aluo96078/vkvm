package security

import "testing"

func TestIdentityRoundTripAndSignature(t *testing.T) {
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseIdentity(identity.PublicString(), identity.PrivateString())
	if err != nil || parsed.PublicString() != identity.PublicString() {
		t.Fatalf("identity round trip failed: %v", err)
	}
	message := []byte("pairing challenge")
	signature := identity.Sign(message)
	if !VerifyIdentity(identity.PublicString(), message, signature) || VerifyIdentity(identity.PublicString(), []byte("wrong"), signature) {
		t.Fatal("identity signature verification failed")
	}
}
