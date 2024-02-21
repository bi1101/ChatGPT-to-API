package common

import (
	"freechatgpt/pkg/logger"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/gin-gonic/gin"
)

type ContextProcessor[T any] interface {
	SetContext(conversation T)
	GetContext() T
	ProcessMethod()
}

func Do[T any](p ContextProcessor[T], conversation T) {
	p.SetContext(conversation)
	p.ProcessMethod()
}

func CopyResponseHeaders(response *fhttp.Response, ctx *gin.Context) {
	logger.Log.Debug("CopyResponseHeaders")
	if response == nil {
		ctx.JSON(400, gin.H{"error": "response is empty"})
		logger.Log.Warning("response is empty")
	}
	skipHeaders := map[string]bool{"Content-Encoding": true, "Content-Length": true, "transfer-encoding": true, "connection": true}
	for name, values := range response.Header {
		if !skipHeaders[name] {
			for _, value := range values {
				ctx.Writer.Header().Set(name, value)
			}
		}
	}
}
