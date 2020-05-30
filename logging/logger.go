package logging

import (
	"fmt"
)

type Logger interface {
	Infof(format string, args ...interface{})
}

type StdLogger struct {
}

func (l *StdLogger) Infof(format string, args ...interface{}) {
	_, err := fmt.Printf(format+"\n", args...)
	if err != nil {
		panic(err)
	}
}

type NullLogger struct {
}

func (l *NullLogger) Infof(format string, args ...interface{}) {
}
