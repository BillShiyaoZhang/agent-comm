package v2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientPreservesPlatformErrorIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "v2 message ID conflict", http.StatusConflict)
	}))
	defer server.Close()
	client := &HTTPClient{BaseURL: server.URL}
	err := client.doJSON(context.Background(), http.MethodPost, "/api/v2/mq/store", map[string]any{"probe": true}, nil, false)
	var platformErr *HTTPError
	if !errors.As(err, &platformErr) || platformErr.Path != "/api/v2/mq/store" ||
		platformErr.StatusCode != http.StatusConflict || platformErr.Body != "v2 message ID conflict\n" {
		t.Fatalf("Platform conflict identity lost: %v", err)
	}
}
