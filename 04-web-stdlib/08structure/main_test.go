package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// Wiring test: real store + real service + real handler, through a real (in-memory) connection.
// It guards the composition root; the detailed behaviour is tested per layer.
func TestWiredAppEndToEnd(t *testing.T) {
	ts := httptest.NewTestServer(t, newRouter(slog.New(slog.DiscardHandler)))
	client := ts.Client() // call Client() first: it also sets ts.URL

	post := func(body string) int {
		resp, err := client.Post(ts.URL+"/users", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if got := post(`{"email":"a@b.co","name":"A"}`); got != 201 {
		t.Fatalf("first register = %d", got)
	}
	if got := post(`{"email":"A@B.CO","name":"B"}`); got != 409 {
		t.Errorf("duplicate register = %d, want 409 (store error must travel up to the handler)", got)
	}
	if got := post(`{"email":"bad","name":""}`); got != 422 {
		t.Errorf("invalid register = %d, want 422", got)
	}

	resp, err := client.Get(ts.URL + "/users")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct{ Users []map[string]any }
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list.Users) != 1 {
		t.Errorf("list = %+v (%v)", list, err)
	}
}
