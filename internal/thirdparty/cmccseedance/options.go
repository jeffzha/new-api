package maas_seedance_sdk

// MaasSeedanceClientOption defines a functional option for MaasSeedanceClient.
type MaasSeedanceClientOption func(*MaasSeedanceClient)

// WithMaasServiceVersion sets the service version, which is passed as the service-version header.
func WithMaasServiceVersion(version string) MaasSeedanceClientOption {
	return func(c *MaasSeedanceClient) {
		c.serviceVersion = version
	}
}
