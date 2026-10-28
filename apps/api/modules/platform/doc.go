// Package platform declares the module boundary for platform concerns:
// configuration, structured logging, the HTTP transport, health/readiness
// reporting, and graceful shutdown that every other module depends on.
//
// M0 foundation only. This package defines the boundary and exposes no domain
// behavior. The platform implementation lives under internal/ and is consumed
// by cmd/api.
package platform

// Name is the stable identifier of the platform module boundary.
const Name = "platform"
