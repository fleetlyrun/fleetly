module github.com/edgesets/edgefleet/sdk/go

go 1.26.6

replace github.com/edgesets/edgefleet/genproto => ../../genproto

require (
	github.com/edgesets/edgefleet/genproto v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.2
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260729162451-8efbd57d26e0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
