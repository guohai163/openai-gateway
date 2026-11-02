package gateway

func validRoutingPool(mode string, ids []string) bool {
	if mode != "round_robin" && mode != "primary_backup" {
		return false
	}
	if mode == "primary_backup" && len(ids) < 2 || len(ids) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
