package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opscatalog"
	"github.com/gin-gonic/gin"
)

// GetOpsCatalogStatus shows the version and read state of the OPS models and discounts copies.
func GetOpsCatalogStatus(c *gin.Context) {
	rows, err := opscatalog.Current(model.DB)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "读取运营平台目录状态失败"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}
