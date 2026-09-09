package log

// Log level constants
const (
	DEBUG    = "DEBUG"
	INFO     = "INFO"
	WARNING  = "WARNING"
	ERROR    = "ERROR"
	CRITICAL = "CRITICAL"
)

// FORMAT 日志格式
const FORMAT = "{time:YYYY-MM-DD HH:mm:ss} | {level} | {file.path}:{line} | {message}"

// LogConfig 日志配置结构体
type LogConfig struct {
	Rotation    int    // Size limit for log files in MB
	Retention   int    // Days to retain log files
	Compression string // Compression format
	Dir         string // Log directory
	Filename    string // Log filename
	Format      string // Log format
	Level       string // Log level
}

// NewLogConfig 创建默认的日志配置
func NewLogConfig() *LogConfig {
	return &LogConfig{
		Rotation:    100,
		Retention:   7,
		Compression: "zip",
		Dir:         "aicc_log",
		Filename:    "aicc.log",
		Format:      FORMAT,
		Level:       INFO,
	}
}
