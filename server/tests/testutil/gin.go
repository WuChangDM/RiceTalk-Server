package testutil

import (
	"github.com/gin-gonic/gin"
)

// NewTestGinEngine returns a Gin engine configured for testing.
func NewTestGinEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	return r
}
