package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

// ---- v1 JSON response helpers ----

// WriteJSON writes v as a JSON response with the given status code.
// Sets Content-Type: application/json.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes a GitHub-style error response:
//
//	{"message": "...", "documentation_url": "..."}
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, apidomain.NewErrorResponse(msg))
}

// WriteFieldError writes an error response with field-level details.
func WriteFieldError(w http.ResponseWriter, status int, msg string, fieldErrors ...apidomain.FieldError) {
	WriteJSON(w, status, apidomain.NewErrorResponse(msg, fieldErrors...))
}

// WriteOK writes a 200 with the given payload wrapped in a singleton
// response envelope.
func WriteOK(w http.ResponseWriter, data any) {
	WriteJSON(w, http.StatusOK, &apidomain.SingletonResponse{Data: data})
}

// WriteCreated writes a 201 with the given payload.
func WriteCreated(w http.ResponseWriter, data any) {
	WriteJSON(w, http.StatusCreated, &apidomain.SingletonResponse{Data: data})
}

// WriteNoContent writes a 204 with no body.
func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// WriteList writes a paginated collection response. If total is negative
// the total_count field is omitted.
func WriteList(w http.ResponseWriter, items any, page, perPage, total int) {
	resp := &apidomain.ListResponse{
		Items: items,
		Pagination: apidomain.Pagination{
			Page:    page,
			PerPage: perPage,
		},
	}
	if total >= 0 {
		resp.Pagination.TotalCount = total
	}
	WriteJSON(w, http.StatusOK, resp)
}

// WriteListWithLinks writes a paginated collection response with
// RFC 5988 Link headers for self/next/prev.
func WriteListWithLinks(w http.ResponseWriter, r *http.Request, items any, page, perPage, total int) {
	baseURL := *r.URL
	q := baseURL.Query()

	// Build Link header.
	var links []string

	// self
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))
	baseURL.RawQuery = q.Encode()
	links = append(links, fmt.Sprintf(`<%s>; rel="self"`, baseURL.String()))

	// next
	if page*perPage < total || total < 0 {
		q.Set("page", strconv.Itoa(page+1))
		baseURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf(`<%s>; rel="next"`, baseURL.String()))
	}

	// prev
	if page > 1 {
		q.Set("page", strconv.Itoa(page-1))
		baseURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf(`<%s>; rel="prev"`, baseURL.String()))
	}

	// Write the Link header.
	for i, l := range links {
		if i == 0 {
			w.Header().Set("Link", l)
		} else {
			w.Header().Add("Link", l)
		}
	}

	WriteList(w, items, page, perPage, total)
}

// parsePagination extracts page/per_page from query params with defaults.
func parsePagination(r *http.Request) apidomain.ListOptions {
	opts := apidomain.DefaultListOptions()
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			opts.Page = n
		}
	}
	if pp := r.URL.Query().Get("per_page"); pp != "" {
		if n, err := strconv.Atoi(pp); err == nil && n > 0 && n <= 100 {
			opts.PerPage = n
		}
	}
	return opts
}

// parseBoolQuery extracts a boolean query parameter.
func parseBoolQuery(r *http.Request, key string) bool {
	v := r.URL.Query().Get(key)
	return v == "true" || v == "1"
}

// parseIDParam parses a chi URL parameter as a positive int64 and writes
// a 400 error on failure. Returns (id, true) on success.
func parseIDParam(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid id: "+raw)
		return 0, false
	}
	return id, true
}
