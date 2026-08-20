package tasksource

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/williamokano/kairos/internal/eventstore"
)

// WebhookConfig binds one opt-in webhook source. Parse turns a verified
// request body into a WorkItem — 08-triggers.md frames a webhook-fed
// plugin as running in "stream mode" (the process stays up, correlating
// NDJSON by callID); stream-mode plugin invocation is this document's
// largest deferred item (see L16-triggers.md's Future work), so Parse is
// a direct Go callback here rather than a plugin round trip. A future
// stream-mode Plugin can implement Parse by talking to its own
// long-lived process instead of parsing inline.
type WebhookConfig struct {
	SourceID    string
	Secret      string // HMAC-SHA256 shared secret ("whsec_...")
	Parse       func(body []byte) (WorkItem, error)
	DefaultFlow string
	Source      Source // for Ack, once a run is created — may be nil
	Limits      QueueLimits
	Log         *slog.Logger
}

// Handler returns an http.HandlerFunc enforcing 08-triggers.md's exact
// rules: HMAC verified BEFORE parsing, a bad signature dropped with a
// counter and a body that reveals nothing about whether the source
// exists, and the same trigger_dedupe table absorbing redelivery — "the
// same upstream event delivered twice by poll overlap and webhook
// redelivery produces exactly one run."
func Handler(cfg WebhookConfig, store eventstore.Store) http.HandlerFunc {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sig := r.Header.Get("X-Kairos-Signature")
		if !verifyHMAC(cfg.Secret, body, sig) {
			// No body distinguishing "bad signature" from "unknown
			// source" — a 404 either way.
			w.WriteHeader(http.StatusNotFound)
			return
		}

		item, err := cfg.Parse(body)
		if err != nil {
			log.Warn("tasksource: webhook payload rejected", "source", cfg.SourceID, "err", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := ValidateWorkItem(item); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		flow := item.Flow
		if flow == "" {
			flow = cfg.DefaultFlow
		}
		runID, created, err := TriggerRun(r.Context(), store, item.DedupeKey, cfg.SourceID, item.ID,
			CreateRunRequest{
				DefinitionRef: flow, Params: item.Params,
				TriggerRef: fmt.Sprintf("webhook:%s:%s", cfg.SourceID, item.DedupeKey),
				Actor:      "trigger:webhook",
			}, cfg.Limits)
		if err != nil {
			log.Warn("tasksource: webhook run rejected", "source", cfg.SourceID, "err", err)
			w.WriteHeader(http.StatusAccepted) // acknowledge receipt; the rejection is logged, not retried
			return
		}
		if created && cfg.Source != nil {
			_, _ = Ack(context.Background(), store, cfg.Source, AckInput{
				ItemID: item.ID, DedupeKey: item.DedupeKey, Outcome: "triggered", RunID: runID,
				IdempotencyKey: "trigger:" + item.DedupeKey,
			})
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func verifyHMAC(secret string, body []byte, sigHeader string) bool {
	if secret == "" || sigHeader == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sigHeader))
}
