// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// discussStreamRequest mirrors the DiscussCodeInput GraphQL type plus
// one optional field. Everything here is what the existing GraphQL
// mutation accepts — the streaming endpoint is a wire-compatible
// delivery variant, not a new protocol.
type discussStreamRequest struct {
	RepositoryID  string `json:"repository_id"`
	Question      string `json:"question"`
	FilePath      string `json:"file_path,omitempty"`
	Code          string `json:"code,omitempty"`
	Language      string `json:"language,omitempty"`
	RequirementID string `json:"requirement_id,omitempty"`
	ArtifactID    string `json:"artifact_id,omitempty"`
}

// handleDiscussStream is the SSE delivery path for discuss_code. It
// runs the same underlying AnswerQuestionStream RPC that the MCP
// path uses, flattening each AnswerDelta into an SSE `event: token`
// frame. The terminal frame is `event: done` with the resolved
// references + usage, so consumers always know when to stop reading.
//
// Browser fetch clients can read this endpoint via the Streams API
// with `text/event-stream`; simple consumers can also treat the body
// as newline-terminated events.
//
// On errors we emit an `event: error` frame and close — this is
// friendlier than an abrupt connection reset because the web UI can
// surface a toast with the reason.
func (s *Server) handleDiscussStream(w http.ResponseWriter, r *http.Request) {
	var req discussStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDiscussJSONErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.RepositoryID == "" {
		writeDiscussJSONErr(w, http.StatusBadRequest, "repository_id is required")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeDiscussJSONErr(w, http.StatusBadRequest, "question is required")
		return
	}

	// Worker availability: fail fast with JSON rather than opening an
	// SSE stream we can't populate.
	if s.llmCaller == nil || !s.llmCaller.IsAvailable() || s.worker == nil || !s.worker.IsAvailable() {
		writeDiscussJSONErr(w, http.StatusServiceUnavailable, "AI worker not reachable")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeDiscussJSONErr(w, http.StatusInternalServerError, "streaming not supported by this HTTP server")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The ask prompt format mirrors what the GraphQL discussCode
	// resolver builds: the user question, a fenced code block with the
	// caller-supplied snippet (if any). Symbols / requirement grounding
	// aren't surfaced over this endpoint yet — those stay on the unary
	// GraphQL path for the web's grounded-answer flows.
	question := req.Question
	if req.Code != "" {
		question = fmt.Sprintf("%s\n\n```\n%s\n```", req.Question, req.Code)
	}

	stream, cancel, err := s.llmCaller.AnswerQuestionStream(
		r.Context(),
		req.RepositoryID,
		resolution.OpDiscussStream,
		&reasoningv1.AnswerQuestionRequest{
			Question:     question,
			RepositoryId: req.RepositoryID,
			FilePath:     req.FilePath,
		},
	)
	if err != nil {
		writeSSEErrorFrame(w, flusher, fmt.Sprintf("worker stream open failed: %v", err))
		return
	}
	defer cancel()

	start := time.Now()
	var total strings.Builder
	for {
		delta, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			writeSSEDoneFrame(w, flusher, total.String(), nil, nil, time.Since(start))
			return
		}
		if recvErr != nil {
			writeSSEErrorFrame(w, flusher, fmt.Sprintf("worker stream error: %v", recvErr))
			return
		}
		if delta.GetFinished() {
			refs := []string{}
			for _, s := range delta.GetReferencedSymbols() {
				if s.GetQualifiedName() != "" {
					refs = append(refs, s.GetQualifiedName())
				}
			}
			writeSSEDoneFrame(w, flusher, total.String(), refs, delta.GetUsage(), time.Since(start))
			return
		}
		chunk := delta.GetContentDelta()
		if chunk == "" {
			continue
		}
		total.WriteString(chunk)
		writeSSETokenFrame(w, flusher, chunk)
	}
}

func writeSSETokenFrame(w http.ResponseWriter, flusher http.Flusher, delta string) {
	payload, _ := json.Marshal(map[string]string{"delta": delta})
	_, _ = fmt.Fprintf(w, "event: token\ndata: %s\n\n", payload)
	flusher.Flush()
}

func writeSSEDoneFrame(
	w http.ResponseWriter,
	flusher http.Flusher,
	full string,
	refs []string,
	usage interface{},
	elapsed time.Duration,
) {
	payload, _ := json.Marshal(map[string]interface{}{
		"answer":     full,
		"references": refs,
		"usage":      usage,
		"elapsed_ms": elapsed.Milliseconds(),
	})
	_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", payload)
	flusher.Flush()
}

func writeSSEErrorFrame(w http.ResponseWriter, flusher http.Flusher, msg string) {
	slog.Warn("discuss stream error", "error", msg)
	payload, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	flusher.Flush()
}

// writeDiscussJSONErr writes a standard JSON error body before any
// SSE framing has started. Once we've emitted the stream-start
// headers we can't switch back to a JSON error body, so callers must
// use writeSSEErrorFrame after that point.
func writeDiscussJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
