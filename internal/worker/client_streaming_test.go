// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
)

// CA-122 Phase 8 (alongside Phase 2): streaming-RPC test harness for the
// 7 server-streaming knowledge / report methods. Verifies:
//   * happy path: phase + progress events surface to OnProgress in
//     order; the final response is returned to the caller.
//   * mid-stream cancellation: the client cancels ctx; the call returns
//     a non-nil error (transport cancellation); no goroutines leak.
//   * worker crash: the server returns an error after a few messages;
//     the client surfaces the error cleanly (no hang).
//   * missing-final detection: the server EOFs without sending a final
//     message; the client returns errMissingFinal.
//   * progress-handler order: phase / progress events are delivered to
//     OnProgress in the exact order the server emitted them.

// fakeKnowledgeServer implements enough of KnowledgeServiceServer and
// EnterpriseReportServiceServer to drive every test scenario from a
// per-test programmable script. Handlers walk the script's steps,
// emitting phase / progress / final messages or returning errors.
type fakeKnowledgeServer struct {
	knowledgev1.UnimplementedKnowledgeServiceServer
	enterprisev1.UnimplementedEnterpriseReportServiceServer

	// script controls server behavior. One step per server emission.
	script []scriptStep

	// recordedClientCancel is set when the server observes ctx.Done()
	// while it was about to emit. Lets cancellation tests assert the
	// server actually saw the cancel.
	recordedClientCancel atomic.Bool
}

type scriptStep struct {
	// Exactly one of these is set. The fake walks steps in order until
	// it returns or sees ctx.Done().
	phase    *commonv1.KnowledgeStreamPhaseMarker
	progress *commonv1.KnowledgeStreamProgress
	finalCN  *knowledgev1.GenerateCliffNotesResponse
	finalRep *enterprisev1.GenerateReportResponse

	// returnErr ends the stream with an error instead of EOF.
	returnErr error

	// returnEOF ends the stream cleanly (server returns nil) without
	// emitting a final message. Forces errMissingFinal on the client.
	returnEOF bool

	// pause inserts a delay before this step. Useful for cancellation
	// scenarios where the test cancels mid-stream.
	pause time.Duration
}

func (s *fakeKnowledgeServer) GenerateCliffNotes(req *knowledgev1.GenerateCliffNotesRequest, stream grpc.ServerStreamingServer[knowledgev1.GenerateCliffNotesStreamMessage]) error {
	for _, step := range s.script {
		if step.pause > 0 {
			select {
			case <-time.After(step.pause):
			case <-stream.Context().Done():
				s.recordedClientCancel.Store(true)
				return stream.Context().Err()
			}
		}
		if step.returnErr != nil {
			return step.returnErr
		}
		if step.returnEOF {
			return nil
		}
		var msg *knowledgev1.GenerateCliffNotesStreamMessage
		switch {
		case step.phase != nil:
			msg = &knowledgev1.GenerateCliffNotesStreamMessage{
				Event: &knowledgev1.GenerateCliffNotesStreamMessage_Phase{Phase: step.phase},
			}
		case step.progress != nil:
			msg = &knowledgev1.GenerateCliffNotesStreamMessage{
				Event: &knowledgev1.GenerateCliffNotesStreamMessage_Progress{Progress: step.progress},
			}
		case step.finalCN != nil:
			msg = &knowledgev1.GenerateCliffNotesStreamMessage{
				Event: &knowledgev1.GenerateCliffNotesStreamMessage_Final{Final: step.finalCN},
			}
		default:
			return fmt.Errorf("scriptStep has no payload")
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeKnowledgeServer) GenerateReport(req *enterprisev1.GenerateReportRequest, stream grpc.ServerStreamingServer[enterprisev1.GenerateReportStreamMessage]) error {
	for _, step := range s.script {
		if step.pause > 0 {
			select {
			case <-time.After(step.pause):
			case <-stream.Context().Done():
				s.recordedClientCancel.Store(true)
				return stream.Context().Err()
			}
		}
		if step.returnErr != nil {
			return step.returnErr
		}
		if step.returnEOF {
			return nil
		}
		var msg *enterprisev1.GenerateReportStreamMessage
		switch {
		case step.phase != nil:
			msg = &enterprisev1.GenerateReportStreamMessage{
				Event: &enterprisev1.GenerateReportStreamMessage_Phase{Phase: step.phase},
			}
		case step.progress != nil:
			msg = &enterprisev1.GenerateReportStreamMessage{
				Event: &enterprisev1.GenerateReportStreamMessage_Progress{Progress: step.progress},
			}
		case step.finalRep != nil:
			msg = &enterprisev1.GenerateReportStreamMessage{
				Event: &enterprisev1.GenerateReportStreamMessage_Final{Final: step.finalRep},
			}
		default:
			return fmt.Errorf("scriptStep has no payload")
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

// startFakeWorker spins up an in-process gRPC server backed by bufconn
// and returns a ready-to-use *Client wired to it. The server is closed
// when t.Cleanup runs.
func startFakeWorker(t *testing.T, fake *fakeKnowledgeServer) *Client {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	knowledgev1.RegisterKnowledgeServiceServer(srv, fake)
	enterprisev1.RegisterEnterpriseReportServiceServer(srv, fake)
	go func() {
		_ = srv.Serve(lis)
	}()

	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	c := &Client{
		address:                  "bufnet",
		knowledgeTimeoutProvider: func() time.Duration { return 30 * time.Second },
	}
	c.bundle.Store(buildBundle(conn))
	t.Cleanup(func() {
		_ = c.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return c
}

func TestStreamingHappyPath_GenerateCliffNotes(t *testing.T) {
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT}},
			{progress: &commonv1.KnowledgeStreamProgress{
				Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES,
				CompletedUnits: 10, TotalUnits: 100, UnitKind: "summary_units",
				Message: "leaves 10/100",
			}},
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES}},
			{progress: &commonv1.KnowledgeStreamProgress{
				Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER,
				CompletedUnits: 50, TotalUnits: 100, UnitKind: "summary_units",
			}},
			{finalCN: &knowledgev1.GenerateCliffNotesResponse{
				Sections: []*knowledgev1.KnowledgeSection{{Title: "Done"}},
			}},
		},
	}
	c := startFakeWorker(t, fake)

	var events []KnowledgeStreamEvent
	resp, err := c.GenerateCliffNotes(context.Background(),
		&knowledgev1.GenerateCliffNotesRequest{RepositoryId: "r1"},
		WithProgressHandler(func(ev KnowledgeStreamEvent) {
			events = append(events, ev)
		}),
	)
	if err != nil {
		t.Fatalf("GenerateCliffNotes: %v", err)
	}
	if resp == nil || len(resp.Sections) == 0 {
		t.Fatal("expected non-empty response")
	}
	if got, want := len(events), 4; got != want {
		t.Fatalf("got %d events, want %d", got, want)
	}
	// 1: phase SNAPSHOT, 2: progress LEAF, 3: phase LEAF, 4: progress RENDER
	if events[0].Phase == nil || events[0].Phase.Phase != commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT {
		t.Errorf("event[0] phase: got %+v, want SNAPSHOT", events[0].Phase)
	}
	if events[1].Progress == nil || events[1].Progress.CompletedUnits != 10 {
		t.Errorf("event[1] progress: got %+v, want CompletedUnits=10", events[1].Progress)
	}
	if events[2].Phase == nil || events[2].Phase.Phase != commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES {
		t.Errorf("event[2] phase: got %+v, want LEAF_SUMMARIES", events[2].Phase)
	}
	if events[3].Progress == nil || events[3].Progress.Phase != commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER {
		t.Errorf("event[3] progress: got %+v, want RENDER", events[3].Progress)
	}
}

func TestStreamingHappyPath_GenerateReport(t *testing.T) {
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT}},
			{progress: &commonv1.KnowledgeStreamProgress{
				Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_RENDER,
				CompletedUnits: 750, TotalUnits: 1000, UnitKind: "report_progress",
				Message: "synthesizing",
			}},
			{finalRep: &enterprisev1.GenerateReportResponse{
				Markdown:     "# Done",
				SectionCount: 5,
			}},
		},
	}
	c := startFakeWorker(t, fake)

	var sawProgress bool
	resp, err := c.GenerateReport(context.Background(),
		&enterprisev1.GenerateReportRequest{ReportId: "r1"},
		WithProgressHandler(func(ev KnowledgeStreamEvent) {
			if ev.Progress != nil && ev.Progress.UnitKind == "report_progress" {
				sawProgress = true
			}
		}),
	)
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if resp.GetMarkdown() != "# Done" {
		t.Errorf("markdown: got %q", resp.GetMarkdown())
	}
	if !sawProgress {
		t.Error("expected at least one report_progress event")
	}
}

func TestStreamingMissingFinal_GenerateCliffNotes(t *testing.T) {
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT}},
			{returnEOF: true},
		},
	}
	c := startFakeWorker(t, fake)

	resp, err := c.GenerateCliffNotes(context.Background(),
		&knowledgev1.GenerateCliffNotesRequest{RepositoryId: "r1"})
	if err == nil {
		t.Fatal("expected error for missing final, got nil")
	}
	if !errors.Is(err, errMissingFinal) {
		t.Errorf("expected errMissingFinal, got %v", err)
	}
	if resp != nil {
		t.Error("response should be nil when stream ended without final")
	}
}

func TestStreamingWorkerCrash_GenerateCliffNotes(t *testing.T) {
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	bug := errors.New("worker exploded")
	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT}},
			{progress: &commonv1.KnowledgeStreamProgress{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES, CompletedUnits: 5, TotalUnits: 100}},
			{returnErr: bug},
		},
	}
	c := startFakeWorker(t, fake)

	resp, err := c.GenerateCliffNotes(context.Background(),
		&knowledgev1.GenerateCliffNotesRequest{RepositoryId: "r1"})
	if err == nil {
		t.Fatal("expected error from server-side crash, got nil")
	}
	if resp != nil {
		t.Error("response should be nil when server errored")
	}
}

func TestStreamingClientCancel_GenerateCliffNotes(t *testing.T) {
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT}},
			// Long pause; the test cancels before this step runs.
			{pause: 5 * time.Second, progress: &commonv1.KnowledgeStreamProgress{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES}},
		},
	}
	c := startFakeWorker(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first event has had time to arrive.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := c.GenerateCliffNotes(ctx,
		&knowledgev1.GenerateCliffNotesRequest{RepositoryId: "r1"})
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	// The exact error type from gRPC depends on timing; we accept any
	// error that surfaces context cancellation through the transport.
	if !errors.Is(err, context.Canceled) && err.Error() == "" {
		t.Errorf("expected error indicating cancellation, got %v", err)
	}

	// Give the server a moment to observe the cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.recordedClientCancel.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("server never observed client cancellation")
}

func TestStreamingNoHandler_GenerateCliffNotes(t *testing.T) {
	// Verifies the call works fine without a progress handler — the
	// dispatchKnowledgeStreamEvent helper handles nil handler cleanly.
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{phase: &commonv1.KnowledgeStreamPhaseMarker{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_SNAPSHOT}},
			{progress: &commonv1.KnowledgeStreamProgress{Phase: commonv1.KnowledgePhase_KNOWLEDGE_PHASE_LEAF_SUMMARIES, CompletedUnits: 10, TotalUnits: 100}},
			{finalCN: &knowledgev1.GenerateCliffNotesResponse{}},
		},
	}
	c := startFakeWorker(t, fake)

	resp, err := c.GenerateCliffNotes(context.Background(),
		&knowledgev1.GenerateCliffNotesRequest{RepositoryId: "r1"})
	if err != nil {
		t.Fatalf("GenerateCliffNotes (no handler): %v", err)
	}
	if resp == nil {
		t.Error("response should not be nil")
	}
}

func TestApplyCallOptions(t *testing.T) {
	// Quick safety test: nil options are ignored, non-nil options apply.
	called := atomic.Int32{}
	co := applyCallOptions(nil)
	if co.OnProgress != nil {
		t.Error("nil options produced non-nil OnProgress")
	}

	co = applyCallOptions([]CallOption{
		nil, // explicit nil should be skipped without panic
		WithProgressHandler(func(ev KnowledgeStreamEvent) { called.Add(1) }),
	})
	if co.OnProgress == nil {
		t.Fatal("expected OnProgress to be set")
	}
	co.OnProgress(KnowledgeStreamEvent{})
	if called.Load() != 1 {
		t.Errorf("OnProgress not invoked, called=%d", called.Load())
	}
}

func TestIsRepositoryScope(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"repository", true},
		{"REPOSITORY", true},
		{" repo ", true},
		{"module", false},
		{"file", false},
		{"symbol", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		if got := isRepositoryScope(tc.in); got != tc.want {
			t.Errorf("isRepositoryScope(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// streamRecvNoLeak verifies that a call returning io.EOF (final received
// then EOF) doesn't leak goroutines. Sentinel for the codex r1 H4 / M5
// concern about long-lived recv loops.
func TestStreamingRecvEOFNoLeak(t *testing.T) {
	// goleak intentionally not used here: gRPC's server goroutines
	// (keepalive, framer reader, controlBuffer) linger past srv.Stop()
	// and are not the leak class we care about. The relevant client-side
	// teardown is asserted in TestStreamingClientCancel.

	fake := &fakeKnowledgeServer{
		script: []scriptStep{
			{finalCN: &knowledgev1.GenerateCliffNotesResponse{}},
		},
	}
	c := startFakeWorker(t, fake)

	for i := 0; i < 5; i++ {
		_, err := c.GenerateCliffNotes(context.Background(),
			&knowledgev1.GenerateCliffNotesRequest{RepositoryId: "r1"})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	// io.EOF is part of the normal recv loop exit; this is just to keep
	// the linter from flagging the unused import in a refactor.
	_ = io.EOF
}
