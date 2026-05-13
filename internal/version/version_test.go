package version

import "testing"

func TestString(t *testing.T) {
	got := String()
	want := "govirta 0.1.0-dev"

	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
