package common

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protodelim"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// LogHandler is a callback function invoked for each successfully parsed log entry.
type LogHandler func(*auditpb.AuditLogEntry) error

// IngestBinProtoStream reads a binary proto file containing serialized, length-delimited
// AuditLogEntry protos and invokes the handler for each entry.
func IngestBinProtoStream(ctx context.Context, path string, handler LogHandler) error {
	if handler == nil {
		return fmt.Errorf("%w: handler cannot be nil", ErrInvalidArgument)
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: file %q not found: %v", ErrNotFound, path, err)
		}
		return fmt.Errorf("failed to open file %q: %w", path, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		entry := &auditpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(reader, entry)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: failed to unmarshal log entry: %v", ErrInvalidArgument, err)
		}
		if err := handler(entry); err != nil {
			return err
		}
	}

	return nil
}

// ResolvePath resolves a potentially relative path against BUILD_WORKSPACE_DIRECTORY if set.
func ResolvePath(path string) string {
	resolvedPath := path
	if !filepath.IsAbs(resolvedPath) {
		if workspaceDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); workspaceDir != "" {
			resolvedPath = filepath.Join(workspaceDir, resolvedPath)
		}
	}
	return resolvedPath
}
