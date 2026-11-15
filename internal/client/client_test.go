package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerRedirectsNeverForwardCredentials(t *testing.T) {
	visits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visits++; t.Error("redirect target was contacted") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	for _, kind := range []string{"admin"} {
		for _, custom := range []bool{false, true} {
			api := Client{BaseURL: source.URL, AdminToken: "private-admin"}
			var configured *http.Client
			if custom {
				configured = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
				api.HTTPClient = configured
			}
			status, err := api.DoJSON(context.Background(), "GET", "/v1/nodes", kind, nil, nil)
			if status != 307 || err == nil {
				t.Fatal(status, err)
			}
			if custom && configured.CheckRedirect(nil, nil) != nil {
				t.Fatal("caller client was mutated")
			}
		}
	}
	if visits != 0 {
		t.Fatal(visits)
	}
}
