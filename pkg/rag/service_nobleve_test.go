//go:build no_bleve

package rag

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuildIndexBleveDisabledReturnsClearError(t *testing.T) {
	workspace := t.TempDir()
	rcfg := config.DefaultConfig().Tools.RAG
	rcfg.IndexProvider = "bleve"

	svc := NewService(workspace, rcfg)
	_, err := svc.BuildIndex(context.Background())
	if err == nil {
		t.Fatalf("expected bleve disabled error")
	}
	if !strings.Contains(err.Error(), "bleve provider disabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}
