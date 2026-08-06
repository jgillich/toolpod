package runtime

import "strings"

const OwnershipLabel = "tpd.managed"

const (
	ServiceNetworkName  = "tpd-services"
	NetworkRoleLabel    = "tpd.network-role"
	NetworkRoleServices = "services"
)

const (
	ServiceLabel       = "tpd.service"
	ServiceRoleLabel   = "tpd.service-role"
	ServiceRoleSidecar = "sidecar"
	ServiceHashLabel   = "tpd.service-hash"
	UsesServiceLabel   = "tpd.uses-service"
)

func OwnershipLabels() map[string]string {
	return map[string]string{OwnershipLabel: "true"}
}

// ServiceNetworkAlias is the DNS alias a service answers on the shared
// network. Names are DNS-safe by profile grammar, so aliases cannot collide.
func ServiceNetworkAlias(serviceName string) string {
	return "tpd-svc-" + serviceName
}

// ServiceHostEnvName is the consumer-side variable exposing a service's
// network alias to the main container.
func ServiceHostEnvName(serviceName string) string {
	upper := strings.ToUpper(strings.ReplaceAll(serviceName, "-", "_"))
	return "TPD_SERVICE_" + upper + "_HOST"
}
