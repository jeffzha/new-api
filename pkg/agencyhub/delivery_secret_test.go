package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDeliverySecretAADBindsOperationAgencyAndAccount(t *testing.T) {
	app := newAgencyTestApp(t)
	t.Setenv("AGENCY_HUB_DELIVERY_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	ciphertext, _, err := app.encryptDeliverySecret(
		"operation-a",
		"temporary-password",
		deliveryAAD("operation-a", 10, 20, deliveryAADSchema),
	)
	require.NoError(t, err)
	delivery := model.AgencyDeliverySecret{
		OperationID:       "operation-a",
		AgencyID:          10,
		OperatorAccountID: 20,
		AADSchema:         deliveryAADSchema,
		Ciphertext:        ciphertext,
	}
	plain, err := app.decryptDeliverySecret(delivery)
	require.NoError(t, err)
	require.Equal(t, "temporary-password", plain)

	tampered := delivery
	tampered.AgencyID = 11
	_, err = app.decryptDeliverySecret(tampered)
	require.Error(t, err)

	tampered = delivery
	tampered.OperatorAccountID = 21
	_, err = app.decryptDeliverySecret(tampered)
	require.Error(t, err)

	tampered = delivery
	tampered.OperationID = "operation-b"
	_, err = app.decryptDeliverySecret(tampered)
	require.Error(t, err)
}

func TestCleanupExpiredDeliverySecretsDestroysCiphertextAndKeepsTombstone(t *testing.T) {
	app := newAgencyTestApp(t)
	now := time.Now().Unix()
	delivery := model.AgencyDeliverySecret{
		OperationID:   "expired-delivery",
		CreatorRootID: 1,
		Ciphertext:    "ciphertext",
		KeyID:         "agency-delivery-v2",
		AADSchema:     deliveryAADSchema,
		ExpiresAt:     now - 1,
		CreatedAt:     now - 10,
	}
	require.NoError(t, app.db.Create(&delivery).Error)

	require.NoError(t, app.CleanupExpiredDeliverySecrets(now))
	var stored model.AgencyDeliverySecret
	require.NoError(t, app.db.First(&stored, delivery.ID).Error)
	require.Empty(t, stored.Ciphertext)
	require.Nil(t, stored.DeliveredAt)
	require.NotNil(t, stored.ExpiredAt)
	require.Equal(t, now, *stored.ExpiredAt)

	// Cleanup is idempotent and never turns an expired tombstone back into a
	// usable delivery.
	require.NoError(t, app.CleanupExpiredDeliverySecrets(now+60))
	require.NoError(t, app.db.First(&stored, delivery.ID).Error)
	require.Empty(t, stored.Ciphertext)
}

func TestDeliveryBindingRequiresOriginalRootOperationAndSourceSession(t *testing.T) {
	app := newAgencyTestApp(t)
	require.NoError(t, app.db.Create(&model.AgencyIdempotencyRecord{
		ScopeHash:  "scope-delivery",
		ActorType:  ActorTypeRoot,
		ActorID:    7,
		Action:     "/agency/api/v1/root/agencies",
		BodyHash:   "body-hash",
		ResourceID: "create-agency-key",
		ResultCode: 201,
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
		CreatedAt:  time.Now().Unix(),
	}).Error)
	delivery := model.AgencyDeliverySecret{
		OperationID:    "scope-delivery",
		CreatorRootID:  7,
		SourceSID:      "source-session-a",
		Action:         "/agency/api/v1/root/agencies",
		BodyHash:       "body-hash",
		IdempotencyKey: "create-agency-key",
	}
	identity := &Identity{ActorType: ActorTypeRoot, ActorID: 7, SourceSID: "source-session-a"}
	require.NoError(t, app.validateDeliveryBinding(app.db, delivery, identity))

	identity.SourceSID = "source-session-b"
	require.ErrorIs(t, app.validateDeliveryBinding(app.db, delivery, identity), errDeliveryForbidden)

	identity.SourceSID = "source-session-a"
	delivery.OperationID = "unknown-scope"
	require.ErrorIs(t, app.validateDeliveryBinding(app.db, delivery, identity), errDeliveryBinding)
}

func TestDeliveryAckProofIsBoundToDeliveryObject(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/agency/api/v1/root/deliveries/42/ack", nil)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	context.Params = gin.Params{{Key: "delivery_id", Value: "42"}}

	identity := &Identity{ActorType: ActorTypeRoot, ActorID: 7}
	require.True(t, proofScopeMatchesRequest(context, identity, "delivery.ack", "delivery:42"))
	require.False(t, proofScopeMatchesRequest(context, identity, "delivery.ack", "delivery:43"))
	require.False(t, proofScopeMatchesRequest(context, identity, "agency.create", "agency:new"))
}

func TestRedactIdempotencySecretsTraversesArrays(t *testing.T) {
	response := map[string]any{
		"data": []any{
			map[string]any{"temporary_password": "secret"},
			map[string]any{"nested": []any{map[string]any{"password": "secret2"}}},
		},
	}
	redactIdempotencySecrets(response)
	data := response["data"].([]any)
	require.Equal(t, "[redacted]", data[0].(map[string]any)["temporary_password"])
	nested := data[1].(map[string]any)["nested"].([]any)
	require.Equal(t, "[redacted]", nested[0].(map[string]any)["password"])
}
