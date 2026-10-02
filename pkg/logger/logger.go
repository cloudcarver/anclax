package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type LogAgent struct {
	name   string
	fileds []zap.Field
	logger *zap.Logger
}

var log *zap.Logger

func init() {
	logger, err := zap.NewProduction(zap.AddCaller(), zap.AddCallerSkip(1))
	if err != nil {
		panic(err)
	}
	log = logger
}

func NewLogAgent(name string) *LogAgent {
	return NewLogAgentWithLogger(name, log)
}

// NewLogAgentWithLogger creates a log agent backed by the provided logger.
// It is useful when an application needs to route a component's logs to a
// dedicated sink, and keeps log assertions deterministic in tests.
func NewLogAgentWithLogger(name string, logger *zap.Logger) *LogAgent {
	return &LogAgent{
		name:   name,
		fileds: []zap.Field{zap.String("module", name)},
		logger: logger,
	}
}

func (a *LogAgent) AppendFiled(field zap.Field) *LogAgent {
	a.fileds = append(a.fileds, field)
	return a
}

// provide basic observability
func (a *LogAgent) Info(msg string, fields ...zapcore.Field) {
	a.logger.Info(msg, append(a.fileds, fields...)...)
}

// expected situation but worth a look
func (a *LogAgent) Warn(msg string, fields ...zapcore.Field) {
	a.logger.Warn(msg, append(a.fileds, fields...)...)
}

// unexpected error causing broken connection
func (a *LogAgent) Error(msg string, fields ...zapcore.Field) {
	a.logger.Error(msg, append(a.fileds, fields...)...)
}

// fatal error causing application shutdown
func (a *LogAgent) Fatal(msg string, fields ...zapcore.Field) {
	a.logger.Fatal(msg, append(a.fileds, fields...)...)
}

// provide basic observability
func (a *LogAgent) Infof(msg string, args ...any) {
	a.logger.Info(fmt.Sprintf(msg, args...), a.fileds...)
}

// expected situation but worth a look
func (a *LogAgent) Warnf(msg string, args ...any) {
	a.logger.Warn(fmt.Sprintf(msg, args...), a.fileds...)
}

// unexpected error causing broken connection
func (a *LogAgent) Errorf(msg string, args ...any) {
	a.logger.Error(fmt.Sprintf(msg, args...), a.fileds...)
}
