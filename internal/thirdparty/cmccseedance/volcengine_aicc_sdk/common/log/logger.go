package log

import (
	"fmt"
	"os"
	"path/filepath"
)

// Logger 日志记录器
type Logger struct {
	config *LogConfig
}

// NewLogger 创建新的日志记录器
func NewLogger() *Logger {
	return &Logger{
		config: NewLogConfig(),
	}
}

// InitLogConfig 初始化日志配置
func (l *Logger) InitLogConfig(configs ...*LogConfig) {
	// 清理现有的日志配置

	// 从环境变量获取日志级别
	logLevel := INFO
	if level := os.Getenv("log_level"); level != "" {
		logLevel = level
	}
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		logLevel = level
	}

	// 输出到标准输出
	fmt.Printf("Log level: %s\n", logLevel)

	// 处理每个配置
	for _, config := range configs {
		if config.Dir == "" {
			config.Dir = "aicc_log"
		}

		if config.Filename == "" {
			config.Filename = "aicc.log"
		}

		// 创建目录
		err := os.MkdirAll(config.Dir, 0755)
		if err != nil {
			fmt.Printf("Failed to create log directory: %v\n", err)
			continue
		}

		fullpath := filepath.Join(config.Dir, config.Filename)

		if config.Rotation <= 0 {
			config.Rotation = 100
		}

		if config.Retention <= 0 {
			config.Retention = 7
		}

		// 这里可以添加文件日志输出的实现
		fmt.Printf("Log file: %s, Rotation: %d MB, Retention: %d days\n",
			fullpath, config.Rotation, config.Retention)
	}
}

// msgProc 处理消息
func msgProc(message string) string {
	if message != "" {
		return fmt.Sprintf("[aicc]:%s", message)
	}
	return message
}

// Debug 记录调试日志
func (l *Logger) Debug(message string, args ...interface{}) {
	fmt.Printf("DEBUG: %s\n", fmt.Sprintf(msgProc(message), args...))
}

// Info 记录信息日志
func (l *Logger) Info(message string, args ...interface{}) {
	fmt.Printf("INFO: %s\n", fmt.Sprintf(msgProc(message), args...))
}

// Warning 记录警告日志
func (l *Logger) Warning(message string, args ...interface{}) {
	fmt.Printf("WARNING: %s\n", fmt.Sprintf(msgProc(message), args...))
}

// Error 记录错误日志
func (l *Logger) Error(message string, args ...interface{}) {
	fmt.Printf("ERROR: %s\n", fmt.Sprintf(msgProc(message), args...))
}

// Critical 记录严重错误日志
func (l *Logger) Critical(message string, args ...interface{}) {
	fmt.Printf("CRITICAL: %s\n", fmt.Sprintf(msgProc(message), args...))
}

// 全局日志记录器
var globalLogger = NewLogger()

// InitLogConfig 初始化全局日志配置
func InitLogConfig(configs ...*LogConfig) {
	globalLogger.InitLogConfig(configs...)
}

// Debug 记录调试日志
func Debug(message string, args ...interface{}) {
	globalLogger.Debug(message, args...)
}

// Info 记录信息日志
func Info(message string, args ...interface{}) {
	globalLogger.Info(message, args...)
}

// Warning 记录警告日志
func Warning(message string, args ...interface{}) {
	globalLogger.Warning(message, args...)
}

// Error 记录错误日志
func Error(message string, args ...interface{}) {
	globalLogger.Error(message, args...)
}

// Critical 记录严重错误日志
func Critical(message string, args ...interface{}) {
	globalLogger.Critical(message, args...)
}
