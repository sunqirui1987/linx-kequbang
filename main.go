package main

import (
	"linx-kequbang/api"
	"linx-kequbang/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func main() {
	// 初始化日志系统 (Initialize Logger)
	logger.Init()
	defer logger.Log.Sync() // 确保缓冲日志刷入磁盘

	logger.Info("正在启动服务...")

	// 设置 Gin 模式
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())
	
	// 自定义 Logger 中间件
	r.Use(func(c *gin.Context) {
		start := logger.Log.With(zap.String("path", c.Request.URL.Path), zap.String("method", c.Request.Method))
		c.Next()
		start.Info("请求处理完成", zap.Int("status", c.Writer.Status()))
	})

	v1 := r.Group("/v1")
	{
		v1.POST("/chat/completions", api.ChatCompletions)
	}

	logger.Info("服务启动成功", zap.String("port", "8080"))
	if err := r.Run(":8080"); err != nil {
		logger.Error("服务启动失败", zap.Error(err))
	}
}
