// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type gateway struct {
	cfg           appConfig
	stdout        io.Writer
	stderr        io.Writer
	conversations *conversationStore
}

func runGateway(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	gw := newGateway(cfg, stdout, stderr)
	server := &http.Server{
		Addr:              cfg.GatewayAddr,
		Handler:           gw.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(stderr, "assistant local gateway listening on %s\n", cfg.GatewayAddr)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func newGateway(cfg appConfig, stdout, stderr io.Writer) *gateway {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &gateway{cfg: cfg, stdout: stdout, stderr: stderr, conversations: newConversationStore()}
}

func (g *gateway) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(r, g.cfg.GatewayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := g.handleGeneric(r.Context(), w, r); err != nil {
			writeGatewayError(w, err)
		}
	})
	mux.HandleFunc("GET /usage", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(r, g.cfg.GatewayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		store, err := usageStoreFor(g.cfg)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, store.Snapshot())
	})
	return mux
}

func (g *gateway) authorized(r *http.Request, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	got := strings.TrimPrefix(auth, prefix)
	return hmac.Equal([]byte(got), []byte(token))
}

func (g *gateway) handleGeneric(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Channel string          `json:"channel"`
		UserID  string          `json:"user_id"`
		Thread  string          `json:"thread_id"`
		Text    string          `json:"text"`
		Raw     json.RawMessage `json:"raw,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return err
	}
	reply, err := replyToInbound(ctx, g.cfg, inboundMessage{
		Channel: firstNonEmpty(in.Channel, "generic"),
		UserID:  in.UserID,
		Thread:  in.Thread,
		Text:    in.Text,
		Raw:     in.Raw,
	}, g.stdout, g.stderr, g.conversations)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
	return nil
}

func writeGatewayError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
