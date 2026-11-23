// Package api exposes the engine over JSON HTTP.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"agentic-qr-payment/internal/engine"
)

const maxBody = 64 << 10

func Register(mux *http.ServeMux, e *engine.Engine) {
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		b, err := e.StateJSON()
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	mux.Handle("POST /api/topup/quote", handle(e.QuoteTopup))
	mux.Handle("POST /api/topup", handle(e.Topup))
	mux.Handle("POST /api/withdraw", handle(noResult(e.Withdraw)))
	mux.Handle("POST /api/session", handle(noResult(e.SetSession)))
	mux.Handle("POST /api/wallet/connect", handle(noResult(e.ConnectWallet)))
	mux.Handle("POST /api/wallet/disconnect", handle(noResult(e.DisconnectWallet)))
	mux.Handle("POST /api/policy", handle(noResult(e.SetPolicy)))
	mux.Handle("POST /api/qr", handle(e.CreateQR))
	mux.Handle("POST /api/advice", handle(e.Advise))
	mux.Handle("POST /api/qr/verify", handle(e.VerifyQR))
	mux.Handle("POST /api/pay/preview", handle(e.PreviewPay))
	mux.Handle("POST /api/pay", handle(e.Pay))
	mux.Handle("POST /api/refund", handle(noResult(e.Refund)))
	mux.Handle("POST /api/merchants/status", handle(noResult(e.ToggleMerchant)))
}

func noResult[T any](fn func(T) error) func(T) (any, error) {
	return func(req T) (any, error) {
		if err := fn(req); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}
}

func handle[T, R any](fn func(T) (R, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req T
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": engine.Error{Code: "ERR_BAD_REQUEST", Message: err.Error()}})
			return
		}
		res, err := fn(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

func writeErr(w http.ResponseWriter, err error) {
	var x *engine.Error
	if errors.As(err, &x) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": x})
		return
	}
	log.Printf("internal error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": engine.Error{Code: "ERR_INTERNAL", Message: "internal error"}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
