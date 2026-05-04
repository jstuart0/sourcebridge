package graphql

// boolToMutationResult converts a (bool, error) result from an underlying
// mutation into the GraphQL MutationResult envelope used by the deprecated
// *Result wrapper mutations. Centralizes the pattern previously duplicated
// across RemoveRepositoryResult, UnlinkReposResult, DetectContractsResult,
// and DeleteModelCapabilitiesResult.
func boolToMutationResult(ok bool, err error) (*MutationResult, error) {
	if err != nil {
		msg := err.Error()
		return &MutationResult{Success: false, Error: &msg}, nil
	}
	return &MutationResult{Success: ok}, nil
}
