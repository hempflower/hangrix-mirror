package domain

// ---- Error response ----

// ErrorResponse is the GitHub-style error envelope returned by every v1
// endpoint on failure.
type ErrorResponse struct {
	Message          string       `json:"message"`
	DocumentationURL string       `json:"documentation_url,omitempty"`
	Errors           []FieldError `json:"errors,omitempty"`
}

// FieldError describes a single field-level validation failure.
type FieldError struct {
	Resource string `json:"resource,omitempty"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message,omitempty"`
}

// NewErrorResponse builds an ErrorResponse with a message and optional
// field-level details.
func NewErrorResponse(msg string, fieldErrors ...FieldError) *ErrorResponse {
	return &ErrorResponse{
		Message:          msg,
		DocumentationURL: "https://hangrix.labx.ink/docs/api",
		Errors:           fieldErrors,
	}
}

// ---- Pagination ----

// ListOptions carries the standard page/per_page query parameters.
type ListOptions struct {
	Page    int `json:"-"`
	PerPage int `json:"-"`
}

// DefaultListOptions returns safe defaults when no query params are given.
func DefaultListOptions() ListOptions {
	return ListOptions{Page: 1, PerPage: 30}
}

// Pagination carries page metadata for collection responses.
type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalCount int `json:"total_count,omitempty"`
}

// ---- Links ----

// Link represents a single HATEOAS link.
type Link struct {
	Href string `json:"href"`
}

// Links is a bag of named hypermedia references embedded in resource
// responses.
type Links map[string]Link

// ---- Common resource shapes ----

// ListResponse is the standard envelope for GET collection endpoints.
type ListResponse struct {
	Items      any        `json:"items"`
	Pagination Pagination `json:"pagination,omitempty"`
	Links      Links      `json:"_links,omitempty"`
}

// SingletonResponse wraps a single resource with optional links.
type SingletonResponse struct {
	Data  any   `json:"data"`
	Links Links `json:"_links,omitempty"`
}

// ---- Identity / discovery ----

// MeResponse is the GET /me payload.
type MeResponse struct {
	SessionID     int64  `json:"session_id"`
	RoleKey       string `json:"role_key"`
	RepoID        *int64 `json:"repo_id"`
	IssueNumber   *int32 `json:"issue_number"`
	SessionStatus string `json:"session_status"`
	TokenActive   bool   `json:"token_active"`
}

// RootResponse is the GET / (API root) payload.
type RootResponse struct {
	Version     string `json:"version"`
	APIPrefix   string `json:"api_prefix"`
	CurrentUser any    `json:"current_user_url,omitempty"`
	Links       Links  `json:"_links"`
}

// ---- Questionnaires ----

// CreateQuestionnaireInput carries the payload for creating a questionnaire.
type CreateQuestionnaireInput struct {
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Questions   []CreateQuestionInput `json:"questions"`
}

// CreateQuestionInput is one question in the creation input.
type CreateQuestionInput struct {
	Type     string        `json:"type"`
	Text     string        `json:"text"`
	Required bool          `json:"required"`
	Options  []OptionInput `json:"options,omitempty"`
}

// OptionInput is one option label (server assigns the ID).
type OptionInput struct {
	Label string `json:"label"`
}
