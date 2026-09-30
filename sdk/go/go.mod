module github.com/fleetlyrun/fleetly/sdk/go

go 1.26.6

require (
	github.com/fleetlyrun/fleetly/genproto v0.0.0
	google.golang.org/grpc v1.83.2
)

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/lynx-go/grpcapi/genproto v0.1.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260921155816-b14227669459 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/fleetlyrun/fleetly/genproto => ../../genproto
