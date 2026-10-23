package avatar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestProcessBuildsSquareAvatarThenUpdatesUser(t *testing.T) {
	var paths []string
	var update map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/image/upload":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("filename") != "req-7-face.png" {
				t.Fatalf("filename = %q", r.FormValue("filename"))
			}
			io.WriteString(w, `{"ok":true,"data":{"id":"uploaded-1"},"metadata":{}}`)
		case "/v1/image/smart_crop":
			assertJSONFields(t, r, map[string]any{"image": "uploaded-1", "aspect": "1:1"})
			io.WriteString(w, `{"ok":true,"data":{"image":"cropped-1"},"metadata":{}}`)
		case "/v1/image/resize":
			var body map[string]any
			decodeJSON(t, r, &body)
			if body["image"] != "cropped-1" || body["format"] != "webp" || body["store"] != false {
				t.Fatalf("resize body = %#v", body)
			}
			io.WriteString(w, `{"ok":true,"data":{"url":"https://cdn.example/avatar.webp"},"metadata":{}}`)
		case "/v1/auth/user/update/user-42":
			decodeJSON(t, r, &update)
			io.WriteString(w, `{"ok":true,"data":{"user_id":"user-42"},"metadata":{}}`)
		default:
			http.NotFound(w, r)
		}
	})
	upstream := httptest.NewServer(handler)
	defer upstream.Close()

	client, err := NewClient(upstream.URL, "test-key", upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Process(context.Background(), "user-42", "req-7", "face.png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"POST /v1/image/upload",
		"POST /v1/image/smart_crop",
		"POST /v1/image/resize",
		"PATCH /v1/auth/user/update/user-42",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("calls = %#v, want %#v", paths, wantPaths)
	}
	if result.Width != 512 || result.Height != 512 || result.Avatar != "https://cdn.example/avatar.webp" {
		t.Fatalf("result = %#v", result)
	}
	if update["user_id"] != "user-42" || update["idempotency_key"] != "req-7" {
		t.Fatalf("update = %#v", update)
	}
	metadata := update["metadata"].(map[string]any)
	if metadata["avatar"] != result.Avatar {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestEnvelopeErrorPreservesClientStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"data":null,"error":{"code":"BAD_IMAGE","message":"image rejected"},"metadata":{}}`)
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, "test-key", upstream.Client())

	err := client.doJSON(context.Background(), http.MethodPost, "/v1/image/smart_crop", map[string]any{"image": "x", "aspect": "1:1"}, nil)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.HTTPStatus != http.StatusBadRequest || apiErr.Code != "BAD_IMAGE" {
		t.Fatalf("error = %#v", err)
	}
}

func TestRetryAfterOnRateLimit(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"ok":false,"error":{"code":"RATE_LIMITED","message":"retry later"}}`)
			return
		}
		io.WriteString(w, `{"ok":true,"data":{"image":"ready"}}`)
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, "test-key", upstream.Client())
	var waited time.Duration
	client.sleep = func(_ context.Context, delay time.Duration) error { waited = delay; return nil }

	got, err := client.imageCall(context.Background(), "/v1/image/smart_crop", map[string]any{"image": "x", "aspect": "1:1"})
	if err != nil || got != "ready" || attempts != 2 || waited != 2*time.Second {
		t.Fatalf("got=%q err=%v attempts=%d waited=%s", got, err, attempts, waited)
	}
}

func assertJSONFields(t *testing.T, r *http.Request, want map[string]any) {
	t.Helper()
	var got map[string]any
	decodeJSON(t, r, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %#v, want %#v", got, want)
	}
}

func decodeJSON(t *testing.T, r *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
