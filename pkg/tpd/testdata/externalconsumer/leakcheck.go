//go:build leakcheck

package externalconsumer

import "github.com/jgillich/tpd/pkg/tpd"

// Compiled only under -tags leakcheck. That build must fail: tpd.Spec was the
// pre-boundary alias for internal/runtime.Spec and no longer exists, so an
// external consumer cannot name the internal spec types.
var _ = tpd.Spec{}
