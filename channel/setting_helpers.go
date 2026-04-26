package channel

// MissingRegistryKeys returns keys from CLIRuntimeSettingKeys that are absent
// from the given handler map. Used by both serverapp and CLI to verify
// that every runtime setting key has a registered handler.
func MissingRegistryKeys[T any](handlers map[string]T) []string {
	var missing []string
	for _, k := range CLIRuntimeSettingKeys {
		if _, ok := handlers[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}
