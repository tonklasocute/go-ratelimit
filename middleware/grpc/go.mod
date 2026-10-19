module github.com/tonklasocute/go-ratelimit/middleware/grpc

go 1.25.0

require (
	github.com/tonklasocute/go-ratelimit v0.1.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

replace github.com/tonklasocute/go-ratelimit => ../..
