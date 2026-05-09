// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package rest handler coverage for handleDiscussStream (CA-281).
//
// Covers:
//   - JSON validation errors → 400
//   - Worker unavailable (nil llmCaller) → 503
//   - Worker unavailable (nil worker field) → 503
//   - Flusher unsupported (ResponseRecorder is not an http.Flusher) → 500
//     (requires llmCaller + worker to both be available — uses an in-process gRPC server)
//   - SSE helper frame formatting (token / done / error frames)
//
// Note on streaming integration paths:
//   The "stream error" and "happy path streaming" cases require a live
//   gRPC AnswerQuestionStream RPC. These are exercised end-to-end in the
//   worker package streaming tests (internal/worker/client_streaming_test.go).
//   The handler-level tests here cover the guard paths and the SSE helper
//   functions that format every frame the handler writes.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// ---------------------------------------------------------------------------
// Fake WorkerLLM — satisfies llmcall.WorkerLLM for IsAvailable tests.
// AnswerQuestionStream is controlled per-test via the streamFunc field.
// ---------------------------------------------------------------------------

type fakeStreamWorker struct {
	streamFunc func(ctx context.Context, req *reasoningv1.AnswerQuestionStreamRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error)
}

func (f *fakeStreamWorker) AnswerQuestion(_ context.Context, _ *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	return &reasoningv1.AnswerQuestionResponse{}, nil
}
func (f *fakeStreamWorker) AnswerQuestionStream(ctx context.Context, req *reasoningv1.AnswerQuestionStreamRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	if f.streamFunc != nil {
		return f.streamFunc(ctx, req)
	}
	return nil, func() {}, fmt.Errorf("stream not configured")
}
func (f *fakeStreamWorker) AnswerQuestionWithTools(_ context.Context, _ *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	return &reasoningv1.AnswerQuestionWithToolsResponse{}, nil
}
func (f *fakeStreamWorker) ClassifyQuestion(_ context.Context, _ *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	return &reasoningv1.ClassifyQuestionResponse{}, nil
}
func (f *fakeStreamWorker) DecomposeQuestion(_ context.Context, _ *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error) {
	return &reasoningv1.DecomposeQuestionResponse{}, nil
}
func (f *fakeStreamWorker) SynthesizeDecomposedAnswer(_ context.Context, _ *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error) {
	return &reasoningv1.SynthesizeDecomposedAnswerResponse{}, nil
}
func (f *fakeStreamWorker) GetProviderCapabilities(_ context.Context) (*reasoningv1.GetProviderCapabilitiesResponse, error) {
	return &reasoningv1.GetProviderCapabilitiesResponse{}, nil
}
func (f *fakeStreamWorker) AnalyzeSymbol(_ context.Context, _ *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error) {
	return &reasoningv1.AnalyzeSymbolResponse{}, nil
}
func (f *fakeStreamWorker) ReviewFile(_ context.Context, _ *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	return &reasoningv1.ReviewFileResponse{}, nil
}
func (f *fakeStreamWorker) GenerateCliffNotes(_ context.Context, _ *knowledgev1.GenerateCliffNotesRequest, _ ...worker.CallOption) (*knowledgev1.GenerateCliffNotesResponse, error) {
	return &knowledgev1.GenerateCliffNotesResponse{}, nil
}
func (f *fakeStreamWorker) GenerateLearningPath(_ context.Context, _ *knowledgev1.GenerateLearningPathRequest, _ ...worker.CallOption) (*knowledgev1.GenerateLearningPathResponse, error) {
	return &knowledgev1.GenerateLearningPathResponse{}, nil
}
func (f *fakeStreamWorker) GenerateArchitectureDiagram(_ context.Context, _ *knowledgev1.GenerateArchitectureDiagramRequest, _ ...worker.CallOption) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	return &knowledgev1.GenerateArchitectureDiagramResponse{}, nil
}
func (f *fakeStreamWorker) GenerateWorkflowStory(_ context.Context, _ *knowledgev1.GenerateWorkflowStoryRequest, _ ...worker.CallOption) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	return &knowledgev1.GenerateWorkflowStoryResponse{}, nil
}
func (f *fakeStreamWorker) GenerateCodeTour(_ context.Context, _ *knowledgev1.GenerateCodeTourRequest, _ ...worker.CallOption) (*knowledgev1.GenerateCodeTourResponse, error) {
	return &knowledgev1.GenerateCodeTourResponse{}, nil
}
func (f *fakeStreamWorker) ExplainSystem(_ context.Context, _ *knowledgev1.ExplainSystemRequest, _ ...worker.CallOption) (*knowledgev1.ExplainSystemResponse, error) {
	return &knowledgev1.ExplainSystemResponse{}, nil
}
func (f *fakeStreamWorker) EnrichRequirement(_ context.Context, _ *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error) {
	return &requirementsv1.EnrichRequirementResponse{}, nil
}
func (f *fakeStreamWorker) ExtractSpecs(_ context.Context, _ *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error) {
	return &requirementsv1.ExtractSpecsResponse{}, nil
}
func (f *fakeStreamWorker) GenerateReport(_ context.Context, _ *enterprisev1.GenerateReportRequest, _ ...worker.CallOption) (*enterprisev1.GenerateReportResponse, error) {
	return &enterprisev1.GenerateReportResponse{}, nil
}

// ---------------------------------------------------------------------------
// startMinimalGRPCServer spins up an in-process gRPC server (no services
// registered) and returns a *worker.Client whose IsAvailable() returns true
// once the connection has been dialled.
//
// The approach: call worker.New to get the client, then trigger a dial by
// calling CheckHealth (which will fail with "unimplemented" — we ignore the
// error). After the TCP handshake the underlying grpc.ClientConn reaches
// connectivity.Ready, so IsAvailable() returns true.
// ---------------------------------------------------------------------------

func startMinimalGRPCServer(t *testing.T) *worker.Client {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	addr := lis.Addr().String()

	// worker.New is the public constructor. TLSConfig{} → insecure transport.
	wc, err := worker.New(addr, worker.TLSConfig{})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	t.Cleanup(func() { _ = wc.Close() })

	// Trigger an immediate dial by making a health check RPC. The RPC will
	// fail (server has no health service registered) but the TCP + gRPC
	// handshake succeed, moving the connection to READY.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = wc.CheckHealth(ctx) // error expected — we only care about side effects

	// Poll for READY state. The transition is asynchronous but typically <10ms.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if wc.IsAvailable() {
			return wc
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Skip("worker gRPC connection did not reach READY state within 2s — skipping streaming tests")
	return nil
}

// discussStreamServer builds a Server with a fake llmcall.Caller and the
// provided worker.Client. The llmCaller's inner is a fakeStreamWorker
// controlled by the returned pointer.
func discussStreamServer(t *testing.T, wc *worker.Client, fake *fakeStreamWorker) *Server {
	t.Helper()
	resolver := resolution.NewFrozenResolver(resolution.Snapshot{})
	caller := llmcall.New(fake, resolver, nil)
	cfg := &config.Config{}
	return &Server{
		cfg:       cfg,
		worker:    wc,
		llmCaller: caller,
	}
}

// discussStreamRequest encodes a valid request body.
func discussStreamJSON(t *testing.T, repoID, question string) *bytes.Reader {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"repository_id": repoID, "question": question})
	return bytes.NewReader(b)
}

// ---------------------------------------------------------------------------
// Validation tests — no worker required.
// ---------------------------------------------------------------------------

func TestHandleDiscussStream_InvalidJSON_Returns400(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	s.handleDiscussStream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if !strings.Contains(body["error"], "invalid JSON") {
		t.Errorf("error = %q, want to contain 'invalid JSON'", body["error"])
	}
}

func TestHandleDiscussStream_MissingRepositoryID_Returns400(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	body, _ := json.Marshal(map[string]string{"question": "what does foo do?"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleDiscussStream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	var errBody map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&errBody)
	if !strings.Contains(errBody["error"], "repository_id") {
		t.Errorf("error = %q, want mention of repository_id", errBody["error"])
	}
}

func TestHandleDiscussStream_EmptyQuestion_Returns400(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	body, _ := json.Marshal(map[string]string{"repository_id": "repo-1", "question": "   "})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleDiscussStream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Worker-unavailable tests — various nil-field combinations.
// ---------------------------------------------------------------------------

func TestHandleDiscussStream_NilLLMCaller_Returns503(t *testing.T) {
	s := &Server{cfg: &config.Config{}, llmCaller: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		discussStreamJSON(t, "repo-1", "what is this?"))
	rec := httptest.NewRecorder()
	s.handleDiscussStream(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rec.Code)
	}
	var errBody map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&errBody)
	if !strings.Contains(errBody["error"], "AI worker") {
		t.Errorf("error = %q, want mention of AI worker", errBody["error"])
	}
}

func TestHandleDiscussStream_NilWorker_Returns503(t *testing.T) {
	// llmCaller is non-nil (so IsAvailable returns true), but worker is nil.
	fake := &fakeStreamWorker{}
	resolver := resolution.NewFrozenResolver(resolution.Snapshot{})
	caller := llmcall.New(fake, resolver, nil)
	s := &Server{cfg: &config.Config{}, llmCaller: caller, worker: nil}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		discussStreamJSON(t, "repo-1", "what is this?"))
	rec := httptest.NewRecorder()
	s.handleDiscussStream(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Flusher unsupported — requires both llmCaller and worker to be available.
// We use a thin non-flushing wrapper around a ResponseRecorder so the
// http.Flusher assertion in the handler fails.
// ---------------------------------------------------------------------------

// noFlushWriter wraps an http.ResponseWriter and intentionally does NOT
// embed http.Flusher. The type-assertion in handleDiscussStream then
// fails and the handler returns 500.
type noFlushWriter struct {
	w http.ResponseWriter
}

func (nf noFlushWriter) Header() http.Header                { return nf.w.Header() }
func (nf noFlushWriter) Write(b []byte) (int, error)        { return nf.w.Write(b) }
func (nf noFlushWriter) WriteHeader(statusCode int)         { nf.w.WriteHeader(statusCode) }

func TestHandleDiscussStream_FlusherUnsupported_Returns500(t *testing.T) {
	wc := startMinimalGRPCServer(t)
	if wc == nil {
		return // t.Skip called inside helper
	}
	fake := &fakeStreamWorker{}
	s := discussStreamServer(t, wc, fake)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		discussStreamJSON(t, "repo-1", "what is this?"))
	// Wrap the recorder so it does NOT implement http.Flusher.
	rec := httptest.NewRecorder()
	s.handleDiscussStream(noFlushWriter{w: rec}, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500 (no Flusher); body: %s", rec.Code, rec.Body.String())
	}
	var errBody map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&errBody)
	if !strings.Contains(errBody["error"], "streaming not supported") {
		t.Errorf("error = %q, want 'streaming not supported'", errBody["error"])
	}
}

// ---------------------------------------------------------------------------
// SSE helper function coverage — token / done / error frames.
// These helpers are called on every response the handler writes, so
// verifying their output format here prevents silent regressions.
// ---------------------------------------------------------------------------

// sseCapture is a minimal ResponseWriter + Flusher that captures all writes.
type sseCapture struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func newSSECapture() *sseCapture { return &sseCapture{header: make(http.Header)} }
func (w *sseCapture) Header() http.Header     { return w.header }
func (w *sseCapture) WriteHeader(code int)    { w.code = code }
func (w *sseCapture) Write(b []byte) (int, error) { return w.buf.Write(b) }
func (w *sseCapture) Flush()                  {}

func TestWriteSSETokenFrame_FormatsCorrectly(t *testing.T) {
	w := newSSECapture()
	writeSSETokenFrame(w, w, "hello world")
	body := w.buf.String()
	if !strings.HasPrefix(body, "event: token\n") {
		t.Errorf("expected 'event: token' prefix, got: %q", body)
	}
	if !strings.Contains(body, `"delta":"hello world"`) {
		t.Errorf("expected delta field, got: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("expected double-newline suffix, got: %q", body)
	}
}

func TestWriteSSEDoneFrame_FormatsCorrectly(t *testing.T) {
	w := newSSECapture()
	writeSSEDoneFrame(w, w, "full answer", []string{"Foo.Bar"}, nil, 100*time.Millisecond)
	body := w.buf.String()
	if !strings.HasPrefix(body, "event: done\n") {
		t.Errorf("expected 'event: done' prefix, got: %q", body)
	}
	if !strings.Contains(body, `"answer":"full answer"`) {
		t.Errorf("expected answer field, got: %q", body)
	}
	if !strings.Contains(body, `"Foo.Bar"`) {
		t.Errorf("expected reference symbol, got: %q", body)
	}
	if !strings.Contains(body, `"elapsed_ms":`) {
		t.Errorf("expected elapsed_ms field, got: %q", body)
	}
}

func TestWriteSSEErrorFrame_FormatsCorrectly(t *testing.T) {
	w := newSSECapture()
	writeSSEErrorFrame(w, w, "something broke")
	body := w.buf.String()
	if !strings.HasPrefix(body, "event: error\n") {
		t.Errorf("expected 'event: error' prefix, got: %q", body)
	}
	if !strings.Contains(body, `"error":"something broke"`) {
		t.Errorf("expected error field, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Stream-error path — worker returns an error after stream opens.
// ---------------------------------------------------------------------------

// fakeErrStream implements ReasoningService_AnswerQuestionStreamClient and
// returns a fixed error on the first Recv call.
type fakeErrStream struct {
	grpc.ClientStream
	err error
}

func (f *fakeErrStream) Recv() (*reasoningv1.AnswerQuestionStreamResponse, error) {
	return nil, f.err
}

func TestHandleDiscussStream_StreamError_WritesSSEErrorFrame(t *testing.T) {
	wc := startMinimalGRPCServer(t)
	if wc == nil {
		return
	}
	streamErr := fmt.Errorf("worker unavailable mid-stream")
	fake := &fakeStreamWorker{
		streamFunc: func(_ context.Context, _ *reasoningv1.AnswerQuestionStreamRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
			return &fakeErrStream{err: streamErr}, func() {}, nil
		},
	}
	s := discussStreamServer(t, wc, fake)

	pr, pw := io.Pipe()
	cap := &ssePipeWriter{pw: pw, header: make(http.Header)}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		discussStreamJSON(t, "repo-1", "what is this?"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleDiscussStream(cap, req)
		pw.Close()
	}()

	var rawBody strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			rawBody.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	<-done

	body := rawBody.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("expected SSE error frame, got: %q", body)
	}
	if !strings.Contains(body, "worker stream error") {
		t.Errorf("expected 'worker stream error' in body, got: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Happy path — worker streams tokens then a "finished" delta.
// ---------------------------------------------------------------------------

// fakeHappyStream returns two token deltas then a finished delta.
type fakeHappyStream struct {
	grpc.ClientStream
	steps []*reasoningv1.AnswerQuestionStreamResponse
	pos   int
}

func (f *fakeHappyStream) Recv() (*reasoningv1.AnswerQuestionStreamResponse, error) {
	if f.pos >= len(f.steps) {
		return nil, io.EOF
	}
	resp := f.steps[f.pos]
	f.pos++
	return resp, nil
}

func TestHandleDiscussStream_HappyPath_WritesTokenAndDoneFrames(t *testing.T) {
	wc := startMinimalGRPCServer(t)
	if wc == nil {
		return
	}
	fake := &fakeStreamWorker{
		streamFunc: func(_ context.Context, _ *reasoningv1.AnswerQuestionStreamRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
			return &fakeHappyStream{
				steps: []*reasoningv1.AnswerQuestionStreamResponse{
					{ContentDelta: "hello "},
					{ContentDelta: "world"},
				},
			}, func() {}, nil
		},
	}
	s := discussStreamServer(t, wc, fake)

	pr, pw := io.Pipe()
	cap := &ssePipeWriter{pw: pw, header: make(http.Header)}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discuss/stream",
		discussStreamJSON(t, "repo-1", "what is this?"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleDiscussStream(cap, req)
		pw.Close()
	}()

	var rawBody strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			rawBody.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	<-done

	body := rawBody.String()
	if !strings.Contains(body, "event: token") {
		t.Errorf("expected token frames, got: %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("expected done frame, got: %q", body)
	}
	if !strings.Contains(body, "hello ") {
		t.Errorf("expected 'hello ' in token frames, got: %q", body)
	}

	// Verify done frame contains the assembled answer.
	if !strings.Contains(body, `"answer":"hello world"`) {
		t.Errorf("expected assembled answer in done frame, got: %q", body)
	}
}

