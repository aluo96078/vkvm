package protocol

import "testing"

func TestMessageMACBindsSessionAndPayload(t *testing.T) {
	key := DeriveSessionKey("token", "device-a", "session-a")
	msg := Message{Type: TypeInput, Payload: InputPayload{Type: "key", KeyCode: 30, Pressed: true}}
	if err := SignMessageInPlace(&msg, key); err != nil {
		t.Fatal(err)
	}
	if !VerifyMessage(msg, key) {
		t.Fatal("signed message did not verify")
	}
	msg.Payload = InputPayload{Type: "key", KeyCode: 30, Pressed: false}
	if VerifyMessage(msg, key) {
		t.Fatal("payload mutation was not detected")
	}
	if VerifyMessage(Message{Type: TypeInput, Payload: InputPayload{Type: "key"}, MAC: msg.MAC}, key) {
		t.Fatal("invalid message unexpectedly verified")
	}
}

func TestMessageMACIsSessionBound(t *testing.T) {
	msg := Message{Type: TypeSyncRequest}
	keyA := DeriveSessionKey("token", "device-a", "session-a")
	keyB := DeriveSessionKey("token", "device-a", "session-b")
	if err := SignMessageInPlace(&msg, keyA); err != nil {
		t.Fatal(err)
	}
	if VerifyMessage(msg, keyB) {
		t.Fatal("message verified with a different session key")
	}
}

func TestMessageMACSupportsDecodedMapAndKeyRotation(t *testing.T) {
	keyOld := []byte("old-session")
	keyNew := []byte("new-session")
	msg := Message{Type: TypeInput, Payload: map[string]interface{}{"type": "mouse_move", "dx": float64(3)}}
	if err := SignMessageInPlace(&msg, keyOld); err != nil {
		t.Fatal(err)
	}
	if got, ok := VerifyMessageAny(msg, [][]byte{keyNew, keyOld}); !ok || string(got) != string(keyOld) {
		t.Fatalf("rotation verification key=%q ok=%v", got, ok)
	}
	if _, ok := VerifyMessageAny(msg, nil); ok {
		t.Fatal("message verified with an empty key ring")
	}
	if err := SignMessageInPlace(nil, keyOld); err != nil {
		t.Fatal("nil message should be a no-op")
	}
}

func TestMessageMACRejectsMalformedAndEmptyCredentials(t *testing.T) {
	msg := Message{Type: TypePing}
	if mac, err := SignMessage(msg, nil); err != nil || mac != "" {
		t.Fatalf("empty-key signature = %q err=%v", mac, err)
	}
	if VerifyMessage(Message{Type: TypePing, MAC: "zz"}, []byte("key")) {
		t.Fatal("malformed MAC was accepted")
	}
}
