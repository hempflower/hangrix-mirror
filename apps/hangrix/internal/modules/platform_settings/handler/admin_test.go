package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
)

// stubStore implements domain.Store with an in-memory map.
type stubStore struct {
	data map[string]domain.Setting
}

func newStubStore() *stubStore {
	return &stubStore{data: make(map[string]domain.Setting)}
}

func (s *stubStore) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := s.data[key]
	if !ok {
		return "", false, nil
	}
	return v.Value, true, nil
}

func (s *stubStore) GetDuration(_ context.Context, key string) (time.Duration, error) {
	v, ok := s.data[key]
	if !ok {
		return 0, nil
	}
	return time.ParseDuration(v.Value)
}

func (s *stubStore) Set(_ context.Context, key, value, description string) error {
	existing, ok := s.data[key]
	if !ok {
		s.data[key] = domain.Setting{
			Key:         key,
			Value:       value,
			Description: description,
			UpdatedAt:   time.Now(),
		}
	} else {
		existing.Value = value
		if description != "" {
			existing.Description = description
		}
		existing.UpdatedAt = time.Now()
		s.data[key] = existing
	}
	return nil
}

func (s *stubStore) List(_ context.Context) ([]domain.Setting, error) {
	out := make([]domain.Setting, 0, len(s.data))
	for _, v := range s.data {
		out = append(out, v)
	}
	return out, nil
}

// noopMiddleware satisfies authdomain.Middleware — passes through
// requests without auth, just like the agent_session handler tests.
type noopMiddleware struct{}

func (noopMiddleware) RequireAuth(next http.Handler) http.Handler  { return next }
func (noopMiddleware) RequireAdmin(next http.Handler) http.Handler { return next }

func newRouter(h *AdminHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func newTestHandler(store domain.Store, defs []domain.Definition) *AdminHandler {
	return NewAdminHandler(&AdminHandlerDeps{
		Store:      store,
		Registry:   domain.NewRegistry(defs),
		Middleware: noopMiddleware{},
	})
}

// --- Tests ---

func TestList_empty(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/platform-settings/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Items []settingDTO `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Items == nil {
		t.Fatal("items is nil, want empty slice")
	}
	if len(body.Items) != 0 {
		t.Fatalf("items len = %d, want 0", len(body.Items))
	}
}

func TestList_withItems(t *testing.T) {
	store := newStubStore()
	_ = store.Set(context.Background(), "key1", "val1", "desc1")
	_ = store.Set(context.Background(), "key2", "val2", "desc2")
	h := newTestHandler(store, nil)

	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/platform-settings/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Items []settingDTO `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(body.Items))
	}
}

func TestGet_existingKey(t *testing.T) {
	store := newStubStore()
	_ = store.Set(context.Background(), "mykey", "myval", "")
	h := newTestHandler(store, nil)

	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/platform-settings/mykey")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var dto settingDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Key != "mykey" || dto.Value != "myval" {
		t.Fatalf("got %+v, want key=mykey val=myval", dto)
	}
}

func TestGet_missingKey(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/platform-settings/nonexistent")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGet_fallsBackToRegistry(t *testing.T) {
	store := newStubStore()
	defs := []domain.Definition{
		{Key: "registered_key", Default: "default_val", Description: "test key"},
	}
	h := newTestHandler(store, defs)

	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/platform-settings/registered_key")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var dto settingDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Value != "default_val" {
		t.Fatalf("value = %q, want default_val", dto.Value)
	}
}

func TestGet_emptyKey(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	// Empty key path segment — the router won't match /{key} with an
	// empty segment; this should hit the list handler instead.
	resp, err := http.Get(srv.URL + "/api/admin/platform-settings/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (falls back to list)", resp.StatusCode)
	}
}

func TestPatchKey(t *testing.T) {
	store := newStubStore()
	h := newTestHandler(store, nil)

	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := bytes.NewReader([]byte(`{"value":"new_val"}`))
	resp, err := http.Post(srv.URL+"/api/admin/platform-settings/testkey", "application/json", body)
	if err != nil {
		t.Fatalf("PATCH via POST (method not allowed): %v", err)
	}
	_ = resp.Body.Close()
	// PATCH via GET/POST is 405.
}

func TestPatchKey_withMethod(t *testing.T) {
	store := newStubStore()
	h := newTestHandler(store, nil)

	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	// Use httptest directly to send PATCH.
	req := httptest.NewRequest("PATCH", srv.URL+"/api/admin/platform-settings/testkey",
		bytes.NewReader([]byte(`{"value":"patched_val"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var dto settingDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Key != "testkey" || dto.Value != "patched_val" {
		t.Fatalf("got %+v, want key=testkey val=patched_val", dto)
	}
}

func TestPatchKey_emptyValue(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)
	req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/k",
		bytes.NewReader([]byte(`{"value":""}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPatchKey_invalidJSON(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)
	req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/k",
		bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPatchKey_registeredKeyDurationValidation(t *testing.T) {
	store := newStubStore()
	defs := []domain.Definition{
		{Key: "duration_key", Default: "1h", Description: "duration key"},
	}
	h := newTestHandler(store, defs)

	t.Run("valid duration", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/duration_key",
			bytes.NewReader([]byte(`{"value":"30m"}`)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		newRouter(h).ServeHTTP(w, req)
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("invalid duration for registered key", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/duration_key",
			bytes.NewReader([]byte(`{"value":"not-a-duration"}`)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		newRouter(h).ServeHTTP(w, req)
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func TestPatch_bulk(t *testing.T) {
	store := newStubStore()
	h := newTestHandler(store, nil)

	body := map[string]any{
		"items": []map[string]string{
			{"key": "k1", "value": "v1"},
			{"key": "k2", "value": "v2"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Verify both keys stored.
	items, _ := store.List(context.Background())
	if len(items) != 2 {
		t.Fatalf("stored items = %d, want 2", len(items))
	}
}

func TestPatch_bulkEmpty(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)

	body := map[string]any{"items": []string{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPatch_bulkMissingKey(t *testing.T) {
	h := newTestHandler(newStubStore(), nil)

	body := map[string]any{
		"items": []map[string]string{
			{"key": "", "value": "v"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPatch_bulkRegisteredKeyDurationValidation(t *testing.T) {
	defs := []domain.Definition{
		{Key: "dur_key", Default: "1h", Description: "test"},
	}
	h := newTestHandler(newStubStore(), defs)

	body := map[string]any{
		"items": []map[string]string{
			{"key": "dur_key", "value": "invalid"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/admin/platform-settings/",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// Verify the handler satisfies the RouteProvider interface (compiled in
// module.go). This test won't compile if the signature changes.
var _ interface{ RegisterRoutes(chi.Router) } = (*AdminHandler)(nil)
