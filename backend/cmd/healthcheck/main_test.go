package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"ok", http.StatusOK, false},
		{"no content", http.StatusNoContent, false},
		{"unavailable", http.StatusServiceUnavailable, true},
		{"not found", http.StatusNotFound, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			if err := check(srv.URL); (err != nil) != tt.wantErr {
				t.Errorf("check() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if err := check(url); err == nil {
		t.Error("check() = nil for a closed server, want error")
	}
}
