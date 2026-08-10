package notify

import "testing"

func TestEscapeAppleScriptString(t *testing.T) {
	got := EscapeAppleScriptString(`quote " slash \\` + "\n" + "line")
	want := `quote \" slash \\\\` + `\n` + `line`
	if got != want {
		t.Fatalf("EscapeAppleScriptString() = %q, want %q", got, want)
	}
}
