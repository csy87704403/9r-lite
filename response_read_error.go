package main

import (
	"errors"
	"net/http"
)

func reportUpstreamReadError(w http.ResponseWriter, err error) {
	if err == nil || errors.Is(err, errAutoStreamComplete) || errors.Is(err, errAutoStreamRejected) {
		return
	}
	if reporter, ok := w.(interface{ upstreamReadError(error) }); ok {
		reporter.upstreamReadError(err)
	}
}

func (w *autoRetryWriter) upstreamReadError(err error) { w.readErr = err }

func (w *modelFailureTrackingWriter) upstreamReadError(err error) {
	reportUpstreamReadError(w.target, err)
}

func (w *usageResponseWriter) upstreamReadError(err error) {
	w.readErr = err
	reportUpstreamReadError(w.target, err)
}
