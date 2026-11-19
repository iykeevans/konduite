package version

import "testing"

func TestDefaultsWithoutLdflags(t *testing.T) {
	want := Info{Version: "dev", Commit: "none", Date: "unknown"}
	if got := Get(); got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}
}

func TestString(t *testing.T) {
	got := Info{Version: "1.2.3", Commit: "abc1234", Date: "2026-11-19"}.String()
	if want := "1.2.3 (commit abc1234, built 2026-11-19)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
