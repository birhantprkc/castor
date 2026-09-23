package whisper

// Routes whisper logging to slog; filters noise.

// #include <whisper.h>
// void castorInstallLogBridge(void); // defined in nativelog.c
import "C"

import (
	"context"
	"log/slog"
	"strings"
)

//export castorNativeLog
func castorNativeLog(level C.int, text *C.char) {
	msg := strings.TrimRight(C.GoString(text), "\r\n ")
	if msg == "" {
		return
	}
	// Global callback on arbitrary threads; no request context.
	if level == 4 { // GGML_LOG_LEVEL_ERROR
		slog.ErrorContext(context.Background(), "whisper native", "text", msg)
	} else {
		slog.WarnContext(context.Background(), "whisper native", "text", msg)
	}
}

func init() { C.castorInstallLogBridge() }
