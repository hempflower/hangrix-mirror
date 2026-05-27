package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

// RegisterV1Routes mounts every /api/v1 endpoint on the given chi router.
// The caller is responsible for scoping the router to /api/v1 before
// calling this.
func RegisterV1Routes(r chi.Router, api AgentAPI) {
	// Discovery / identity
	r.Get("/", v1Root)
	r.Get("/me", v1Me(api))

	// Dual-addressing: both explicit and current-issue forms.
	// /issues/current — the session-scoped issue (requires issue scope)
	// /issues/{number} — explicit issue lookup
	r.Route("/issues", func(r chi.Router) {
		// Current-issue convenience form
		r.Route("/current", func(r chi.Router) {
			r.Get("/", v1ReadIssue(api))
			r.Patch("/", v1EditIssue(api))
			r.Post("/comments", v1CreateComment(api))
			r.Get("/comments/{commentID}", v1GetComment(api))
			r.Get("/children", v1ListChildren(api))
			r.Get("/checks", v1ListChecks(api))
			r.Get("/todos", v1ListTodos(api))
			r.Post("/todos", v1CreateTodo(api))
			r.Patch("/todos/{todoID}", v1UpdateTodo(api))
			r.Get("/contributions", v1ListContributions(api))
			r.Get("/contributions/{id}", v1ReadContribution(api))
			r.Patch("/contributions/{id}", v1SetContributionMeta(api))
			r.Post("/contributions/{id}/apply", v1ApplyContribution(api))
			r.Post("/contributions/{id}/close", v1CloseContribution(api))
			r.Post("/reviews", v1CreateReview(api))
			r.Get("/mergeability", v1Mergeability(api))
			r.Post("/merge", v1MergeIssue(api))
			r.Post("/close", v1CloseIssue(api))
			r.Get("/sessions", v1ListSessions(api))
			r.Post("/sessions/{sessionID}/recover", v1RecoverSession(api))
			r.Post("/attachments", v1UploadAttachment(api))

			// Questionnaires
			r.Post("/questionnaires", v1CreateQuestionnaire(api))
			r.Get("/questionnaires", v1ListQuestionnaires(api))
			r.Get("/questionnaires/{id}", v1GetQuestionnaire(api))
			r.Get("/questionnaires/{id}/results", v1GetQuestionnaireResult(api))
			r.Post("/questionnaires/{id}/close", v1CloseQuestionnaire(api))
		})

		// Explicit issue-by-number form
		r.Get("/{issueNumber}", v1ReadIssueByNumber(api))
		r.Post("/{issueNumber}/comments", v1CreateCrossIssueComment(api))
	})

	// Releases
	r.Route("/releases", func(r chi.Router) {
		r.Post("/", v1CreateRelease(api))
		r.Patch("/{id}", v1UpdateRelease(api))
		r.Delete("/{id}", v1DeleteRelease(api))
		r.Post("/{id}/publish", v1PublishRelease(api))
		r.Post("/{id}/assets", v1UploadReleaseAsset(api))
	})

	// Repo-scoped issue creation
	r.Post("/repos/{repoID}/issues", v1CreateIssue(api))
}

// v1Root returns the API root with top-level links.
func v1Root(w http.ResponseWriter, r *http.Request) {
	WriteOK(w, &apidomain.RootResponse{
		Version:   "v1",
		APIPrefix: "/api/v1",
		Links: apidomain.Links{
			"self":          {Href: "/api/v1/"},
			"me":            {Href: "/api/v1/me"},
			"issues":        {Href: "/api/v1/issues"},
			"releases":      {Href: "/api/v1/releases"},
			"contributions": {Href: "/api/v1/issues/current/contributions"},
		},
	})
}
