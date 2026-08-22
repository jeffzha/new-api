package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionRejectsIncompleteSeedanceVideoCNYPrices(t *testing.T) {
	originalDB := model.DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = originalDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	incomplete := `{"doubao-seedance-2-0-260128":{"720p":{"without_video":46,"with_video":28}}}`
	requestBody, err := common.Marshal(OptionUpdateRequest{
		Key:   seedancepricing.OptionKey,
		Value: incomplete,
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/option", strings.NewReader(string(requestBody)))
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateOption(ctx)

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "exactly 3 supported models")
	var count int64
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", seedancepricing.OptionKey).Count(&count).Error)
	assert.Zero(t, count)
}
