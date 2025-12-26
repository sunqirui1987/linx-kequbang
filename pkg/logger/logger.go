package logger

import (
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// Log 是全局 Logger 实例
	Log *zap.Logger
	once sync.Once
)

// Init 初始化日志系统
func Init() {
	once.Do(func() {
		encoderConfig := zap.NewProductionEncoderConfig()
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder   // ISO8601 时间格式
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder // 大写日志级别

		// 控制台输出
		consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
		
		core := zapcore.NewCore(
			consoleEncoder,
			zapcore.AddSync(os.Stdout),
			zapcore.InfoLevel, // 默认日志级别为 Info
		)

		// 开启调用栈信息和调用者信息
		Log = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	})
}

// Info 记录 Info 级别日志
func Info(msg string, fields ...zap.Field) {
	Log.Info(msg, fields...)
}

// Error 记录 Error 级别日志
func Error(msg string, fields ...zap.Field) {
	Log.Error(msg, fields...)
}

// Debug 记录 Debug 级别日志
func Debug(msg string, fields ...zap.Field) {
	Log.Debug(msg, fields...)
}

// Warn 记录 Warn 级别日志
func Warn(msg string, fields ...zap.Field) {
	Log.Warn(msg, fields...)
}
