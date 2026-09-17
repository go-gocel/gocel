package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

func callHook(o *SessionObserver, ctx context.Context, info *kernel.RunInfo) {
	o.OnRunComplete(ctx, info)
}

// 鈹€鈹€ Session 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestNewSession(t *testing.T) {
	s := NewSession("test-agent")
	if s == nil {
		t.Fatal("session is nil")
	}
	if s.ID() == "" {
		t.Error("ID should not be empty")
	}
	if s.AgentName() != "test-agent" {
		t.Errorf("AgentName = %q, want %q", s.AgentName(), "test-agent")
	}
	if s.Status() != "active" {
		t.Errorf("Status = %q, want %q", s.Status(), "active")
	}
	if s.CreatedAt().IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if s.UsedTokens() != 0 {
		t.Errorf("UsedTokens = %d, want 0", s.UsedTokens())
	}
}

func TestSession_AddMessage(t *testing.T) {
	s := NewSession("test")
	s.AddMessage(types.NewUserMessage("hello"))
	msgs := s.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "hello")
	}
}

func TestSession_AddMessages(t *testing.T) {
	s := NewSession("test")
	msgs := []*types.Message{
		types.NewUserMessage("msg1"),
		types.NewAssistantMessage("resp1"),
	}
	s.AddMessages(msgs)
	got := s.GetMessages()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
}

func TestSession_AddTokenUsage(t *testing.T) {
	s := NewSession("test")
	s.AddTokenUsage(100)
	s.AddTokenUsage(50)
	if got := s.UsedTokens(); got != 150 {
		t.Errorf("UsedTokens = %d, want 150", got)
	}
}

func TestSession_SetStatus(t *testing.T) {
	s := NewSession("test")
	s.SetStatus("completed")
	if got := s.Status(); got != "completed" {
		t.Errorf("Status = %q, want %q", got, "completed")
	}
}

func TestSession_SetAgentName(t *testing.T) {
	s := NewSession("old-name")
	s.SetAgentName("new-name")
	if got := s.AgentName(); got != "new-name" {
		t.Errorf("AgentName = %q, want %q", got, "new-name")
	}
}

func TestSession_Meta(t *testing.T) {
	s := NewSession("test")
	meta := s.Meta()
	if meta == nil {
		t.Fatal("Meta() returned nil")
	}
	// Meta should be a copy, not the internal map
	meta["custom"] = "value"
	meta2 := s.Meta()
	if _, exists := meta2["custom"]; exists {
		t.Error("Meta should return a copy, but second call contains modified key")
	}
}

func TestSession_GetMessages_Copy(t *testing.T) {
	s := NewSession("test")
	s.AddMessage(types.NewUserMessage("hello"))
	msgs := s.GetMessages()
	// Modifying returned slice should not affect internal state
	msgs[0].Content = "modified"
	original := s.GetMessages()
	if original[0].Content != "hello" {
		t.Error("GetMessages should return a copy")
	}
}

func TestSession_TrimMessages(t *testing.T) {
	s := NewSession("test")
	original := []*types.Message{
		types.NewUserMessage("msg1"),
		types.NewAssistantMessage("msg2"),
	}
	s.AddMessages(original)
	// Trim to only the second message
	s.TrimMessages(original[1:])
	got := s.GetMessages()
	if len(got) != 1 {
		t.Fatalf("got %d messages after trim, want 1", len(got))
	}
	if got[0].Content != "msg2" {
		t.Errorf("content = %q, want %q", got[0].Content, "msg2")
	}
}

func TestSession_Concurrency(t *testing.T) {
	s := NewSession("test")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.AddMessage(types.NewUserMessage("hi"))
			s.AddTokenUsage(1)
			_ = s.Status()
			_ = s.UsedTokens()
			_ = s.GetMessages()
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			s.AddMessage(types.NewAssistantMessage("ho"))
			s.SetStatus("running")
			_ = s.ID()
			_ = s.Meta()
			_ = s.CreatedAt()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	if s.UsedTokens() != 100 {
		t.Errorf("UsedTokens = %d, want 100 (concurrent access)", s.UsedTokens())
	}
}

// 鈹€鈹€ InMemorySessionService 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestInMemorySessionService_Create(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	sess, err := svc.Create(ctx, "agent1", "user1", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess == nil {
		t.Fatal("session is nil")
	}
	if sess.AgentName() != "agent1" {
		t.Errorf("AgentName = %q, want %q", sess.AgentName(), "agent1")
	}
}

func TestInMemorySessionService_CreateWithMeta(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	meta := map[string]any{"source": "test"}
	sess, err := svc.Create(ctx, "agent1", "user1", meta)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Meta()["source"] != "test" {
		t.Errorf("meta source = %v, want %q", sess.Meta()["source"], "test")
	}
	if sess.Meta()["user_id"] != "user1" {
		t.Errorf("meta user_id = %v, want %q", sess.Meta()["user_id"], "user1")
	}
}

func TestInMemorySessionService_Get(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	created, _ := svc.Create(ctx, "agent1", "user1", nil)
	got, err := svc.Get(ctx, created.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.ID() != created.ID() {
		t.Errorf("ID = %q, want %q", got.ID(), created.ID())
	}
}

func TestInMemorySessionService_Get_NotFound(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	got, err := svc.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get(nonexistent): %v", err)
	}
	if got != nil {
		t.Error("Get should return nil for nonexistent session")
	}
}

func TestInMemorySessionService_Save(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	created, _ := svc.Create(ctx, "agent1", "user1", nil)
	created.AddMessage(types.NewUserMessage("saved message"))
	err := svc.Save(ctx, created)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := svc.Get(ctx, created.ID())
	if loaded == nil {
		t.Fatal("loaded session is nil")
	}
	msgs := loaded.GetMessages()
	if len(msgs) != 1 {
		t.Errorf("got %d messages, want 1", len(msgs))
	}
}

func TestInMemorySessionService_Delete(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	created, _ := svc.Create(ctx, "agent1", "user1", nil)
	err := svc.Delete(ctx, created.ID())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := svc.Get(ctx, created.ID())
	if got != nil {
		t.Error("session should be deleted")
	}
}

func TestInMemorySessionService_List(t *testing.T) {
	svc := NewInMemorySessionService()
	ctx := context.Background()
	svc.Create(ctx, "a1", "user1", nil)
	svc.Create(ctx, "a2", "user1", nil)
	svc.Create(ctx, "a3", "user2", nil)
	sessions, err := svc.List(ctx, "user1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("got %d sessions for user1, want 2", len(sessions))
	}
	sessions2, _ := svc.List(ctx, "user2")
	if len(sessions2) != 1 {
		t.Errorf("got %d sessions for user2, want 1", len(sessions2))
	}
	sessions3, _ := svc.List(ctx, "nobody")
	if len(sessions3) != 0 {
		t.Errorf("got %d sessions for nobody, want 0", len(sessions3))
	}
}

// 鈹€鈹€ FileSystemSessionService 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestFileSystemSessionService_CreateAndGet(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSystemSessionService(dir)
	ctx := context.Background()
	sess, err := svc.Create(ctx, "agent1", "user1", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Check the JSON file was created
	path := filepath.Join(dir, sess.ID()+".json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("session file not created at %s", path)
	}
	// Get and verify
	loaded, err := svc.Get(ctx, sess.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded == nil || loaded.ID() != sess.ID() {
		t.Errorf("loaded session ID mismatch")
	}
}

func TestFileSystemSessionService_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSystemSessionService(dir)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, "agent1", "user1", nil)
	sess.AddMessage(types.NewUserMessage("persisted message"))
	sess.AddTokenUsage(42)
	sess.SetStatus("completed")
	svc.Save(ctx, sess)
	// Reload from scratch
	svc2 := NewFileSystemSessionService(dir)
	loaded, _ := svc2.Get(ctx, sess.ID())
	if loaded == nil {
		t.Fatal("loaded session is nil after reload")
	}
	msgs := loaded.GetMessages()
	if len(msgs) != 1 {
		t.Errorf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Content != "persisted message" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "persisted message")
	}
	if loaded.UsedTokens() != 42 {
		t.Errorf("UsedTokens = %d, want 42", loaded.UsedTokens())
	}
	if loaded.Status() != "completed" {
		t.Errorf("Status = %q, want %q", loaded.Status(), "completed")
	}
}

func TestFileSystemSessionService_Delete(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSystemSessionService(dir)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, "agent1", "user1", nil)
	id := sess.ID()
	svc.Delete(ctx, id)
	path := filepath.Join(dir, id+".json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("session file should be deleted: %s", path)
	}
}

func TestFileSystemSessionService_List(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSystemSessionService(dir)
	ctx := context.Background()
	svc.Create(ctx, "a1", "user1", nil)
	svc.Create(ctx, "a2", "user1", nil)
	svc.Create(ctx, "a3", "user2", nil)
	sessions, err := svc.List(ctx, "user1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("got %d sessions, want 2", len(sessions))
	}
}

func TestFileSystemSessionService_Get_NotFound(t *testing.T) {
	dir := t.TempDir()
	svc := NewFileSystemSessionService(dir)
	ctx := context.Background()
	sess, err := svc.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sess != nil {
		t.Error("Get should return nil for nonexistent")
	}
}

// 鈹€鈹€ SessionObserver 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestSessionObserver_NilSafe(t *testing.T) {
	// Should not panic with nil receiver or nil service
	var o *SessionObserver
	ctx := context.Background()
	callHook(o, ctx, &kernel.RunInfo{})
}

func TestSessionObserver_OnRunComplete_CreatesSession(t *testing.T) {
	svc := NewInMemorySessionService()
	o := NewSessionObserver(svc)
	ctx := context.Background()
	info := &kernel.RunInfo{
		AgentName: "test-agent",
		Input: &types.AgentInput{
			Messages: []*types.Message{types.NewUserMessage("hello")},
		},
		AllMsgs: []*types.Message{
			types.NewUserMessage("hello"),
			types.NewAssistantMessage("world"),
		},
		Result: &kernel.Result{Content: "world", TokenUsage: &types.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
	}
	callHook(o, ctx, info)
	// Should have created a session
	sessions, _ := svc.List(ctx, "")
	if len(sessions) == 0 {
		t.Fatal("no sessions created")
	}
	s := sessions[0]
	if s.Status() != "completed" {
		t.Errorf("Status = %q, want %q", s.Status(), "completed")
	}
	if s.UsedTokens() != 8 {
		t.Errorf("UsedTokens = %d, want 8", s.UsedTokens())
	}
}

func TestSessionObserver_OnRunComplete_ErrorStatus(t *testing.T) {
	svc := NewInMemorySessionService()
	o := NewSessionObserver(svc)
	ctx := context.Background()
	info := &kernel.RunInfo{
		AgentName: "test-agent",
		Input:     &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}},
		Err:       kernel.ErrMaxStepsReached,
	}
	callHook(o, ctx, info)
	sessions, _ := svc.List(ctx, "")
	if len(sessions) == 0 {
		t.Fatal("no sessions created")
	}
	if sessions[0].Status() != "error" {
		t.Errorf("Status = %q, want %q", sessions[0].Status(), "error")
	}
}

func TestSessionObserver_OnRunComplete_ExistingSession(t *testing.T) {
	svc := NewInMemorySessionService()
	o := NewSessionObserver(svc)
	ctx := context.Background()
	// First create a session
	sess, _ := svc.Create(ctx, "test-agent", "user", map[string]any{
		"session_id": "pre-existing-id",
	})
	info := &kernel.RunInfo{
		AgentName: "test-agent",
		Input: &types.AgentInput{
			Messages: []*types.Message{types.NewUserMessage("hello")},
			Meta:     map[string]any{"session_id": sess.ID()},
		},
		AllMsgs: []*types.Message{
			types.NewUserMessage("hello"),
			types.NewAssistantMessage("response"),
		},
		Result: &kernel.Result{Content: "response"},
	}
	callHook(o, ctx, info)
	// Should reuse existing session
	loaded, _ := svc.Get(ctx, sess.ID())
	if loaded == nil {
		t.Fatal("existing session not found")
	}
	msgs := loaded.GetMessages()
	if len(msgs) != 1 {
		t.Errorf("got %d messages, want 1 (new messages only)", len(msgs))
	}
}

func TestSessionObserver_OnRunComplete_NilInput(t *testing.T) {
	svc := NewInMemorySessionService()
	o := NewSessionObserver(svc)
	ctx := context.Background()
	info := &kernel.RunInfo{
		AgentName: "test-agent",
		Input:     nil,
	}
	callHook(o, ctx, info)
	sessions, _ := svc.List(ctx, "")
	if len(sessions) != 1 {
		t.Errorf("got %d sessions, want 1", len(sessions))
	}
}

func TestNewSessionObserver_ImplementsInterface(t *testing.T) {
	svc := NewInMemorySessionService()
	o := NewSessionObserver(svc)
	var _ kernel.Module = o
}

// 鈹€鈹€ Session interface compliance 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestSession_ImplementsInterface(t *testing.T) {
	var _ Session = NewSession("test")
}

func TestInMemorySessionService_ImplementsInterface(t *testing.T) {
	var _ SessionService = NewInMemorySessionService()
}

func TestFileSystemSessionService_ImplementsInterface(t *testing.T) {
	dir := t.TempDir()
	var _ SessionService = NewFileSystemSessionService(dir)
}

// 鈹€鈹€ Session ID uniqueness 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestSessionID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := types.SessionID()
		if ids[id] {
			t.Fatalf("duplicate session ID: %s", id)
		}
		ids[id] = true
	}
}
