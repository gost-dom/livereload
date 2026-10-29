package livereload

import (
	"fmt"
	"log/slog"
	"os"
)

type logger slog.Logger

func (l *logger) LogError(msg string, e error) {
	if l != nil {
		sl := (*slog.Logger)(l)
		if e == nil {
			sl.Error(msg)
		} else {
			sl.Error(msg, "err", e)
		}
	} else {
		if e == nil {
			fmt.Fprintf(os.Stderr, msg)
		} else {
			fmt.Fprintf(os.Stderr, "%s: %v", msg, e)
		}
	}
}
