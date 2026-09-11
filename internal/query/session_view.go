package query

// SessionView keeps the original Web endpoint routed through the new-format
// query contract. Old per-session files are intentionally not interpreted.
func (service *Service) SessionView(projectID, provider, sessionID string) (Detail, error) {
	return service.SessionDetail(projectID, provider, sessionID, Filter{})
}
