package runtime

const OwnershipLabel = "tpd.managed"

func OwnershipLabels() map[string]string {
	return map[string]string{OwnershipLabel: "true"}
}
