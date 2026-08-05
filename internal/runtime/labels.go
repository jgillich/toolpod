package runtime

const OwnershipLabel = "tpd.managed"

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
