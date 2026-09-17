package checkpoint

import (
	"context"
	"os"
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

func TestWithMaxCheckpoints_InMemory(t *testing.T) {
	store := NewInMemoryCheckpointStore(WithMaxCheckpoints(3))
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		cp := &types.Checkpoint{ID: "ckpt_" + string(rune('0'+i))}
		if err := store.Save(ctx, cp); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	ids, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("List len = %d, want 3 (oldest evicted): %v", len(ids), ids)
	}
	// Oldest two (ckpt_1, ckpt_2) must be gone; newest three remain.
	for _, want := range []string{"ckpt_3", "ckpt_4", "ckpt_5"} {
		if _, err := store.Load(ctx, want); err != nil {
			t.Errorf("%s should survive: %v", want, err)
		}
	}
	for _, gone := range []string{"ckpt_1", "ckpt_2"} {
		if _, err := store.Load(ctx, gone); err == nil {
			t.Errorf("%s should have been evicted", gone)
		}
	}
}

func TestWithMaxCheckpoints_UnlimitedByDefault(t *testing.T) {
	store := NewInMemoryCheckpointStore()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		cp := &types.Checkpoint{ID: "ckpt_unlim_" + string(rune('0'+i))}
		if err := store.Save(ctx, cp); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	ids, _ := store.List(ctx)
	if len(ids) != 10 {
		t.Fatalf("default store must not evict: got %d, want 10", len(ids))
	}
}

func TestWithMaxCheckpoints_FileSystem(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSystemCheckpointStore(dir, WithMaxCheckpoints(2))
	if err != nil {
		t.Fatalf("NewFileSystemCheckpointStore: %v", err)
	}
	ctx := context.Background()

	for i := 1; i <= 4; i++ {
		cp := &types.Checkpoint{ID: "fs_ckpt_" + string(rune('0'+i))}
		if err := store.Save(ctx, cp); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	ids, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("List len = %d, want 2 (oldest evicted): %v", len(ids), ids)
	}
	// Files of evicted checkpoints must be removed from disk.
	for _, gone := range []string{"fs_ckpt_1", "fs_ckpt_2"} {
		if _, err := os.Stat(dir + "/" + gone + ".ckpt"); err == nil {
			t.Errorf("%s file should have been removed", gone)
		}
	}
}

func TestCheckpointStores(t *testing.T) {
	t.Run("InMemory_Save_Load_Delete_List_roundtrip", func(t *testing.T) {
		store := NewInMemoryCheckpointStore()
		ctx := context.Background()

		cp := &types.Checkpoint{ID: "ckpt_1", SessionID: "sess_1"}
		err := store.Save(ctx, cp)
		if err != nil {
			t.Fatalf("Save error: %v", err)
		}

		loaded, err := store.Load(ctx, "ckpt_1")
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if loaded.ID != "ckpt_1" {
			t.Fatalf("ID = %q, want %q", loaded.ID, "ckpt_1")
		}

		ids, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(ids) != 1 || ids[0] != "ckpt_1" {
			t.Fatalf("List = %v, want ['ckpt_1']", ids)
		}

		err = store.Delete(ctx, "ckpt_1")
		if err != nil {
			t.Fatalf("Delete error: %v", err)
		}

		_, err = store.Load(ctx, "ckpt_1")
		if err == nil {
			t.Fatal("expected error loading deleted checkpoint")
		}

		ids, _ = store.List(ctx)
		if len(ids) != 0 {
			t.Fatalf("List after delete = %v, want []", ids)
		}
	})

	t.Run("InMemory_Load_returns_error_for_unknown_ID", func(t *testing.T) {
		store := NewInMemoryCheckpointStore()
		_, err := store.Load(context.Background(), "nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown ID")
		}
	})

	t.Run("FileSystem_Save_Load_Delete_List_roundtrip", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileSystemCheckpointStore(dir)
		if err != nil {
			t.Fatalf("NewFileSystemCheckpointStore: %v", err)
		}
		ctx := context.Background()

		cp := &types.Checkpoint{
			ID:       "fs_ckpt_1",
			Messages: []*types.Message{types.NewUserMessage("hi")},
		}
		err = store.Save(ctx, cp)
		if err != nil {
			t.Fatalf("Save error: %v", err)
		}

		loaded, err := store.Load(ctx, "fs_ckpt_1")
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if loaded.ID != "fs_ckpt_1" {
			t.Fatalf("ID = %q, want %q", loaded.ID, "fs_ckpt_1")
		}
		if len(loaded.Messages) != 1 {
			t.Fatalf("Messages len = %d, want 1", len(loaded.Messages))
		}
		if loaded.Messages[0].Content != "hi" {
			t.Fatalf("Content = %q, want %q", loaded.Messages[0].Content, "hi")
		}

		ids, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(ids) != 1 || ids[0] != "fs_ckpt_1" {
			t.Fatalf("List = %v, want ['fs_ckpt_1']", ids)
		}

		err = store.Delete(ctx, "fs_ckpt_1")
		if err != nil {
			t.Fatalf("Delete error: %v", err)
		}

		_, err = store.Load(ctx, "fs_ckpt_1")
		if err == nil {
			t.Fatal("expected error after delete")
		}

		ids, _ = store.List(ctx)
		if len(ids) != 0 {
			t.Fatalf("List after delete = %v, want []", ids)
		}
	})

	t.Run("FileSystem_List_empty_when_no_files", func(t *testing.T) {
		store, err := NewFileSystemCheckpointStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewFileSystemCheckpointStore: %v", err)
		}
		ids, err := store.List(context.Background())
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("List = %v, want []", ids)
		}
	})

	t.Run("FileSystem_List_non_existent_dir_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileSystemCheckpointStore(dir)
		if err != nil {
			t.Fatalf("NewFileSystemCheckpointStore: %v", err)
		}
		os.RemoveAll(dir)
		ids, err := store.List(context.Background())
		if err != nil {
			t.Fatalf("List error for non-existent dir: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("List = %v, want []", ids)
		}
	})
}
