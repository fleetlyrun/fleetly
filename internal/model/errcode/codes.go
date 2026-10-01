package errcode

import "google.golang.org/grpc/codes"

// builtins 是首发码集。纪律：只增不复用——新码必须带 Source 锚点并在
// 生产代码出现（usage 反扫），预留码进 usage_test.go 豁免清单并在此注明。
var builtins = []Code{
	{
		ID:         "E_INTERNAL",
		Summary:    "Internal server error.",
		Suggestion: "Check fleetlyd logs for the matching error_id. If the error persists, report it with the error_id attached.",
		Source:     "internal/api/fleetlygrpc/mapping.go mapStateError (fallback)",
		GRPC:       codes.Internal,
	},
	{
		ID:         "E_NOT_FOUND",
		Summary:    "The requested resource does not exist.",
		Suggestion: "Check the resource id and list existing resources to confirm the target.",
		Source:     "internal/api/fleetlygrpc/mapping.go mapStateError",
		GRPC:       codes.NotFound,
	},
	{
		ID:         "E_ALREADY_EXISTS",
		Summary:    "A resource with the same unique key already exists.",
		Suggestion: "Choose a different name, or fetch the existing resource instead of creating a new one.",
		Source:     "internal/api/fleetlygrpc/mapping.go mapStateError (state.ErrAlreadyExists; producer restored in the 2026-10-01 batch-0 review after the REST 409 regression)",
		GRPC:       codes.AlreadyExists,
	},
	{
		ID:         "E_INVALID_ARGUMENT",
		Summary:    "The request payload is invalid.",
		Suggestion: "Fix the flagged field and retry. Field paths point at the exact input location.",
		Source:     "internal/spec spec.ValidationError → internal/api/fleetlygrpc/mapping.go mapValidationError",
		GRPC:       codes.InvalidArgument,
	},
	{
		ID:         "E_QUEUE_FULL",
		Summary:    "The deployment admission queue is full for this app.",
		Suggestion: "Retry after in-flight deployments finish, or cancel a queued deployment to free a slot.",
		Source:     "internal/engine/admission.go Submit (ADR-0016)",
		GRPC:       codes.ResourceExhausted,
	},
	{
		ID:         "E_NOT_CANCELLABLE",
		Summary:    "The deployment has reached a terminal state and can no longer be cancelled.",
		Suggestion: "Terminal states are facts, not actions; deploy a new revision instead.",
		Source:     "internal/engine/admission.go Cancel",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_NO_BASELINE",
		Summary:    "The app has no successful deployment to roll back to.",
		Suggestion: "Deploy at least one revision successfully before rolling back.",
		Source:     "internal/engine/rollback.go Rollback",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_CONFLICT",
		Summary:    "The operation conflicts with the resource's current state.",
		Suggestion: "Resolve the conflicting state named in the cause (cancel or wait for active work, remove referencing resources, or refresh after a concurrent change) and retry.",
		Source:     "internal/api/fleetlygrpc/mapping.go mapStateError (state.ErrConflict: CAS mismatch / FK RESTRICT); structure.go DeleteApp/DeleteProject (ADR-0023); REST maps 409 via assembly errcodeToHTTP",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_QUOTA_EXCEEDED",
		Summary:    "A project-level quota has been reached.",
		Suggestion: "Remove unused entries of the quoted resource, or split the workload across projects.",
		Source:     "internal/api/fleetlygrpc/structure.go PutConfig (F0.17 quota)",
		GRPC:       codes.ResourceExhausted,
	},
	{
		ID:         "E_SECRET_UNAVAILABLE",
		Summary:    "The secret facility is unavailable or the referenced secret is missing.",
		Suggestion: "Create the missing secret or restore the master key under the data root keys/ directory.",
		Source:     "internal/api/fleetlygrpc/structure.go PutSecret / resolveMaterials",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_UNAUTHENTICATED",
		Summary:    "The request carries no valid token.",
		Suggestion: "Log in with a valid token ('fleetly login'), or check that the token has not been revoked.",
		Source:     "internal/authn/interceptor.go guard",
		GRPC:       codes.Unauthenticated,
	},
	{
		ID:         "E_FORBIDDEN",
		Summary:    "The token lacks the scope required by this method.",
		Suggestion: "Use a token whose role grants the required resource:action scope, or ask an admin for one.",
		Source:     "internal/authn/interceptor.go guard (scopeSet.Satisfies)",
		GRPC:       codes.PermissionDenied,
	},
	{
		ID:         "E_INVALID_INVITATION",
		Summary:    "The invitation token is invalid, already used, or expired.",
		Suggestion: "Ask an admin for a fresh invitation; invitation tokens are single-use and time-boxed.",
		Source:     "internal/api/fleetlygrpc/invitations.go AcceptInvitation",
		GRPC:       codes.PermissionDenied,
	},
	{
		ID:         "E_INVALID_SIGNATURE",
		Summary:    "The webhook signature verification failed.",
		Suggestion: "Ensure the GitHub webhook secret matches the hook secret shown once by 'fleetly hooks set/rotate' (rotate the hook and update GitHub if the secret is lost).",
		Source:     "internal/api/fleetlygrpc/webhook.go ReceiveWebhook",
		GRPC:       codes.Unauthenticated,
	},
}
