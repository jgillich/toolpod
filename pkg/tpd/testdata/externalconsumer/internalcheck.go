//go:build internalcheck

package externalconsumer

import "github.com/jgillich/tpd/internal/runtime"

// Compiled only under -tags internalcheck. That build must fail: the internal/
// rule forbids this import from outside the module, so no external consumer
// can reach the runtime types that the removed tpd.Spec alias referenced.
var _ = runtime.Spec{}
