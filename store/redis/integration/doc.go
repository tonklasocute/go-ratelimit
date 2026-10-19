// Package integration tests the Redis store against a real Redis server.
//
// Set REDIS_ADDR (host:port) to use an existing server; otherwise a
// redis:7-alpine container is started with testcontainers-go. Tests are
// skipped when neither is available. It is a separate module so that the
// store itself does not depend on Docker libraries.
package integration
