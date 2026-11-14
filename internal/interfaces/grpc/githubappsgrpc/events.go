package githubappsgrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记 GitHub App 上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg(EventGitHubAppConnected, GitHubAppEventPayload{})
	reg(EventGitHubAppDeleted, GitHubAppEventPayload{})
}
