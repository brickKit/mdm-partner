package core

import "github.com/brickKit/be-sdk-go/v3/runtime"

// Base is what every module works with: the component's runtime (its database, events, audit, logger), and the
// deployment and implementation instance the source methods stamp their answers with (protocol 02).
type Base struct {
	RT         *runtime.Runtime
	Deployment string
	Instance   string
}
