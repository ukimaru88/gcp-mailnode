package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

var (
	summaryFile *os.File
	detailFile  *os.File
	mu          sync.Mutex
	logDir      string
	emitter     func(level, msg string)
)

func SetEmitter(fn func(level, msg string)) {
	mu.Lock()
	defer mu.Unlock()
	emitter = fn
}

func Init() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir = filepath.Join(home, "Desktop", "gcp-mailnode-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	ts := time.Now().Format("2006-01-02_15-04-05")
	summaryPath := filepath.Join(logDir, fmt.Sprintf("摘要_%s.txt", ts))
	detailPath := filepath.Join(logDir, fmt.Sprintf("详细_%s.txt", ts))

	sf, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	df, err := os.OpenFile(detailPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		sf.Close()
		return err
	}
	summaryFile = sf
	detailFile = df

	Info("========================================")
	Info("GCP MailNode 启动")
	Info("时间: %s", time.Now().Format("2006-01-02 15:04:05"))
	Info("摘要日志: %s", summaryPath)
	Info("详细日志: %s", detailPath)
	Info("========================================")

	return nil
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if summaryFile != nil {
		summaryFile.Close()
		summaryFile = nil
	}
	if detailFile != nil {
		detailFile.Close()
		detailFile = nil
	}
	emitter = nil
}

func Info(format string, args ...interface{})  { write("INFO", true, true, true, format, args...) }
func Error(format string, args ...interface{}) { write("ERROR", true, true, true, format, args...) }
func Warn(format string, args ...interface{})  { write("WARN", true, true, true, format, args...) }
func Debug(format string, args ...interface{}) { write("DEBUG", false, true, true, format, args...) }
func Detail(format string, args ...interface{}) {
	write("DETAIL", false, true, false, format, args...)
}

func Fatal(format string, args ...interface{}) {
	write("FATAL", true, true, true, format, args...)
	mu.Lock()
	if summaryFile != nil {
		summaryFile.Sync()
	}
	if detailFile != nil {
		detailFile.Sync()
	}
	mu.Unlock()
}

func write(level string, toSummary, toDetail, toFrontend bool, format string, args ...interface{}) {
	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] [%s] %s\n", ts, level, msg)

	mu.Lock()
	if toSummary && summaryFile != nil {
		summaryFile.WriteString(line)
	}
	if toDetail && detailFile != nil {
		detailFile.WriteString(line)
	}
	fn := emitter
	mu.Unlock()

	if toFrontend && fn != nil {
		func() {
			defer func() { recover() }()
			fn(level, fmt.Sprintf("[%s] %s", ts, msg))
		}()
	}
}

func LogPanic(r interface{}) {
	buf := make([]byte, 8192)
	n := runtime.Stack(buf, false)
	stack := string(buf[:n])
	Fatal("PANIC: %v\n%s", r, stack)
}

func Dir() string { return logDir }
