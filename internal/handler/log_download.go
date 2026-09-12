package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
)

const maxConcurrentLogDownloads = 2

var fallbackLogDownloadSlots = make(chan struct{}, maxConcurrentLogDownloads)

func (h *Handler) acquireLogDownload(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	slots := h.logDownloads
	if slots == nil {
		slots = fallbackLogDownloadSlots
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *Handler) writeLogDownload(w http.ResponseWriter, stream io.ReadCloser, filename string, operation string) {
	defer func() {
		if err := stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
			h.logger.Error("close log download", "operation", operation, "error", err)
		}
	}()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, stream); err != nil {
		h.logger.Error("download logs", "operation", operation, "error", err)
	}
}
