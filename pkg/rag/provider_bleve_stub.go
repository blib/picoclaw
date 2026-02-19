//go:build no_bleve

package rag

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newBleveProvider(_ string, _ config.RAGToolsConfig, _ string) (IndexProvider, error) {
	return nil, fmt.Errorf("bleve provider disabled (built with -tags no_bleve)")
}
