package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"booksmith/internal/store"
)

func TestValidSetting(t *testing.T) {
	cases := []struct {
		k, v string
		want bool
	}{
		{"concurrency", "-5", false},
		{"concurrency", "8", true},
		{"concurrency", "abc", false},
		{"theme", "hacker", false},
		{"theme", "dark", true},
		{"defaultFormats", "exe,bat", false},
		{"defaultFormats", "txt,epub", true},
		{"probeBatchSize", "12", true},
		{"probeBatchSize", "0", false},
		{"probeBatchSize", "21", false},
		{"probeMaxSources", "36", true},
		{"probeMaxSources", "101", false},
		{"probeStopHealthy", "3", true},
		{"probeStopHealthy", "21", false},
	}
	for _, c := range cases {
		if got := validSetting(c.k, c.v); got != c.want {
			t.Errorf("validSetting(%q, %q) = %v, want %v", c.k, c.v, got, c.want)
		}
	}
}

func TestPutSettingsRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	app := &App{Store: st, DataDir: dir}
	srv := New(app)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/settings", srv.handlePutSettings)

	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"concurrency":"-5","theme":"hacker"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if got, _ := st.KVGet("setting_concurrency"); got == "-5" {
		t.Errorf("concurrency=-5 被写入")
	}
	if got, _ := st.KVGet("setting_theme"); got == "hacker" {
		t.Errorf("theme=hacker 被写入")
	}
}
