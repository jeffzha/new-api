package mobilecloudseedance

import (
	"fmt"

	cmccseedance "github.com/QuantumNous/new-api/internal/thirdparty/cmccseedance"
)

func newOfficialSDKClient(baseURL, apiKey, model string) (client sdkClient, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			client = nil
			err = fmt.Errorf("initialize Mobile Cloud Seedance secure client: SDK panic")
		}
	}()

	officialClient, err := cmccseedance.NewMaasSeedanceClient(
		baseURL,
		apiKey,
		model,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize Mobile Cloud Seedance secure client: %w", err)
	}
	return officialClient, nil
}
