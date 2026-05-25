// Package domain declares the dashboard aggregation types —
// the response shape returned by GET /api/admin/dashboard.
// No persistence interfaces live here; the infra layer owns
// the sqlc-generated queries directly.
package domain

import "time"

// DashboardResponse is the top-level JSON envelope for the admin dashboard.
type DashboardResponse struct {
	Summary        Summary        `json:"summary"`
	Timeseries     Timeseries     `json:"timeseries"`
	Providers      []ProviderStat `json:"providers"`
	Health         Health         `json:"health"`
	RecentFailures []FailureItem  `json:"recent_failures"`
}

// Summary carries the aggregate KPI values for the selected time range.
type Summary struct {
	TotalCalls     int64 `json:"total_calls"`
	TotalTokens    int64 `json:"total_tokens"`
	ActiveSessions int64 `json:"active_sessions"`
	OnlineRunners  int64 `json:"online_runners"`
	TotalRunners   int64 `json:"total_runners"`
	FailedCalls    int64 `json:"failed_calls"`
}

// Timeseries holds the day-level bucketed data for charts.
type Timeseries struct {
	DailyCalls  []DailyCalls  `json:"daily_calls"`
	DailyTokens []DailyTokens `json:"daily_tokens"`
}

// DailyCalls is one data point for the daily request-count line chart.
type DailyCalls struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

// DailyTokens is one data point for the daily token-usage line chart.
type DailyTokens struct {
	Date             string `json:"date"`
	TotalTokens      int64  `json:"total_tokens"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// ProviderStat is the per-provider aggregation row for the provider leaderboard.
type ProviderStat struct {
	ProviderName string `json:"provider_name"`
	Calls        int64  `json:"calls"`
	TotalTokens  int64  `json:"total_tokens"`
}

// Health captures runner and session liveness for the health summary block.
type Health struct {
	OnlineRunners   int64 `json:"online_runners"`
	OfflineRunners  int64 `json:"offline_runners"`
	DisabledRunners int64 `json:"disabled_runners"`
	LiveSessions    int64 `json:"live_sessions"`
}

// FailureItem is one row in the recent-failures list.
type FailureItem struct {
	ID           int64     `json:"id"`
	ProviderName string    `json:"provider_name"`
	Model        string    `json:"model"`
	StatusCode   int32     `json:"status_code"`
	ErrorMessage string    `json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
	SessionID    *int64    `json:"session_id,omitempty"`
}
