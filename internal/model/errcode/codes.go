package errcode

import "google.golang.org/grpc/codes"

// builtins 是首发码集。纪律：只增不复用——新码必须带 Source 锚点并在
// 生产代码出现（usage 反扫），预留码进 usage_test.go 豁免清单并在此注明。
var builtins = []Code{
	{
		ID:         "E_INTERNAL",
		Summary:    "Internal server error.",
		Suggestion: "Check fleetlyd logs for the matching error_id. If the error persists, report it with the error_id attached.",
		Source:     "added during implementation",
		GRPC:       codes.Internal,
	},
	{
		ID:         "E_NOT_FOUND",
		Summary:    "The requested resource does not exist.",
		Suggestion: "Check the resource id and list existing resources to confirm the target.",
		Source:     "added during implementation",
		GRPC:       codes.NotFound,
	},
	{
		ID:         "E_ALREADY_EXISTS",
		Summary:    "A resource with the same unique key already exists.",
		Suggestion: "Choose a different name, or fetch the existing resource instead of creating a new one.",
		Source:     "added during implementation",
		GRPC:       codes.AlreadyExists,
	},
	{
		ID:         "E_INVALID_ARGUMENT",
		Summary:    "The request payload is invalid.",
		Suggestion: "Fix the flagged field and retry. Field paths point at the exact input location.",
		Source:     "added during implementation",
		GRPC:       codes.InvalidArgument,
	},
	{
		ID:         "E_QUEUE_FULL",
		Summary:    "The deployment admission queue is full for this app.",
		Suggestion: "Retry after in-flight deployments finish, or cancel a queued deployment to free a slot.",
		Source:     "added during implementation",
		GRPC:       codes.ResourceExhausted,
	},
	{
		ID:         "E_NOT_CANCELLABLE",
		Summary:    "The deployment has reached a terminal state and can no longer be cancelled.",
		Suggestion: "Terminal states are facts, not actions; deploy a new revision instead.",
		Source:     "added during implementation",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_NO_BASELINE",
		Summary:    "The app has no successful deployment to roll back to.",
		Suggestion: "Deploy at least one revision successfully before rolling back.",
		Source:     "added during implementation",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_SECRET_UNAVAILABLE",
		Summary:    "The secret facility is unavailable or the referenced secret is missing.",
		Suggestion: "Create the missing secret or restore the master key under the data root keys/ directory.",
		Source:     "added during implementation",
		GRPC:       codes.FailedPrecondition,
	},
	{
		ID:         "E_UNAUTHENTICATED",
		Summary:    "The request carries no valid token.",
		Suggestion: "Log in with a valid token ('fleetly login'), or check that the token has not been revoked.",
		Source:     "added during implementation",
		GRPC:       codes.Unauthenticated,
	},
	{
		ID:         "E_FORBIDDEN",
		Summary:    "The token lacks the scope required by this method.",
		Suggestion: "Use a token whose role grants the required resource:action scope, or ask an admin for one.",
		Source:     "added during implementation",
		GRPC:       codes.PermissionDenied,
	},
}
