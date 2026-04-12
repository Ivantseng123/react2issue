package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisAttachmentStore_PrepareAndResolve(t *testing.T) {
	client := testRedisClient(t)

	store := NewRedisAttachmentStore(client)

	ctx := context.Background()
	jobID := "job-attach-1"

	attachments := []AttachmentMeta{
		{SlackFileID: "F001", Filename: "screenshot.png", Size: 1024, MimeType: "image/png", DownloadURL: "https://files.slack.com/F001"},
		{SlackFileID: "F002", Filename: "log.txt", Size: 512, MimeType: "text/plain", DownloadURL: "https://files.slack.com/F002"},
	}

	if err := store.Prepare(ctx, jobID, attachments); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	if results[0].Filename != "screenshot.png" {
		t.Errorf("results[0].Filename = %q, want %q", results[0].Filename, "screenshot.png")
	}
	if results[1].Filename != "log.txt" {
		t.Errorf("results[1].Filename = %q, want %q", results[1].Filename, "log.txt")
	}
}

func TestRedisAttachmentStore_ResolveBeforePrepare(t *testing.T) {
	client := testRedisClient(t)

	store := NewRedisAttachmentStore(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	jobID := "job-attach-2"

	attachments := []AttachmentMeta{
		{SlackFileID: "F003", Filename: "data.csv", Size: 2048, MimeType: "text/csv", DownloadURL: "https://files.slack.com/F003"},
	}

	// Prepare after 500ms in a goroutine.
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := store.Prepare(context.Background(), jobID, attachments); err != nil {
			t.Errorf("Prepare in goroutine failed: %v", err)
		}
	}()

	// Resolve should poll and eventually return.
	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Filename != "data.csv" {
		t.Errorf("results[0].Filename = %q, want %q", results[0].Filename, "data.csv")
	}
}

func TestRedisAttachmentStore_Cleanup(t *testing.T) {
	client := testRedisClient(t)

	store := NewRedisAttachmentStore(client)

	ctx := context.Background()
	jobID := "job-attach-3"

	attachments := []AttachmentMeta{
		{SlackFileID: "F004", Filename: "report.pdf", Size: 4096, MimeType: "application/pdf", DownloadURL: "https://files.slack.com/F004"},
	}

	if err := store.Prepare(ctx, jobID, attachments); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if err := store.Cleanup(ctx, jobID); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Verify key is deleted.
	key := keyPrefix + "jobs:attachments:" + jobID
	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Exists check failed: %v", err)
	}
	if exists != 0 {
		t.Errorf("key %q still exists after Cleanup", key)
	}
}
