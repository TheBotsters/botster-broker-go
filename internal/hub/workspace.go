package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TheBotsters/botster-broker-go/internal/workspace"
)

// workspaceFileOps are capabilities eligible for broker-local handling.
var workspaceFileOps = map[string]bool{
	"read":  true,
	"write": true,
	"edit":  true,
}

// workspaceExecutorCache stores FileExecutor instances per workspace root path.
// This avoids re-creating executors (and re-resolving symlinks) on every command.
type workspaceExecutorCache struct {
	mu    sync.RWMutex
	cache map[string]*workspace.FileExecutor
}

var executorCache = &workspaceExecutorCache{
	cache: make(map[string]*workspace.FileExecutor),
}

func (c *workspaceExecutorCache) get(rootPath string) (*workspace.FileExecutor, error) {
	c.mu.RLock()
	fe, ok := c.cache[rootPath]
	c.mu.RUnlock()
	if ok {
		return fe, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check after acquiring write lock.
	if fe, ok := c.cache[rootPath]; ok {
		return fe, nil
	}
	fe, err := workspace.NewFileExecutor(rootPath)
	if err != nil {
		return nil, err
	}
	c.cache[rootPath] = fe
	return fe, nil
}

// readPayload matches the command payload for a read operation.
type readPayload struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// writePayload matches the command payload for a write operation.
type writePayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// editPayload matches the command payload for an edit operation.
type editPayload struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// extractFilePath extracts the file path from a command payload for read/write/edit ops.
func extractFilePath(capability string, payload json.RawMessage) (string, error) {
	var pathHolder struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(payload, &pathHolder); err != nil {
		return "", fmt.Errorf("unmarshal payload path: %w", err)
	}
	if pathHolder.Path == "" {
		return "", fmt.Errorf("empty path in %s payload", capability)
	}
	return pathHolder.Path, nil
}

// isWorkspacePath checks whether a file path targets the agent's workspace.
// It handles both absolute paths (containing the workspace root) and relative paths.
func isWorkspacePath(filePath, workspaceRoot string) bool {
	// Relative paths are always within the workspace when the executor is rooted there.
	if !filepath.IsAbs(filePath) {
		return true
	}

	// For absolute paths, check if they're within the workspace root.
	// Clean both paths for consistent comparison.
	cleanFile := filepath.Clean(filePath)
	cleanRoot := filepath.Clean(workspaceRoot)

	// Check with HasPrefix after ensuring trailing separator handling.
	return cleanFile == cleanRoot || strings.HasPrefix(cleanFile, cleanRoot+string(filepath.Separator))
}

// tryHandleWorkspaceOp attempts to handle a command locally if it's a file op
// targeting the agent's workspace. Returns (result, handled).
// If handled is true, the result should be sent back to the brain.
// If handled is false, the command should be routed to an actuator normally.
func (h *Hub) tryHandleWorkspaceOp(cmd commandRequest) (*WSMessage, bool) {
	// Only file operations are candidates.
	if !workspaceFileOps[cmd.msg.Capability] {
		return nil, false
	}

	// Look up the agent to get workspace_root.
	agent, err := h.db.GetAgentByID(cmd.agentID)
	if err != nil || agent == nil {
		return nil, false
	}
	if !agent.WorkspaceRoot.Valid || agent.WorkspaceRoot.String == "" {
		return nil, false
	}
	workspaceRoot := agent.WorkspaceRoot.String

	// Extract file path from payload.
	filePath, err := extractFilePath(cmd.msg.Capability, cmd.msg.Payload)
	if err != nil {
		log.Printf("[hub:workspace] Failed to extract path from %s payload: %v", cmd.msg.Capability, err)
		return nil, false // Fall through to actuator routing.
	}

	// Check if the path targets the workspace.
	if !isWorkspacePath(filePath, workspaceRoot) {
		return nil, false // Outside workspace — route to actuator.
	}

	// Get or create the FileExecutor for this workspace.
	fe, err := executorCache.get(workspaceRoot)
	if err != nil {
		log.Printf("[hub:workspace] Failed to create FileExecutor for %s: %v", workspaceRoot, err)
		return nil, false
	}

	// For absolute paths, make them relative to workspace root so the executor
	// can validate them properly.
	relativePath := filePath
	if filepath.IsAbs(filePath) {
		rel, err := filepath.Rel(workspaceRoot, filepath.Clean(filePath))
		if err != nil {
			log.Printf("[hub:workspace] Failed to make path relative: %v", err)
			return nil, false
		}
		relativePath = rel
	}

	// Execute the file operation.
	var result *WSMessage
	switch cmd.msg.Capability {
	case "read":
		result = h.handleWorkspaceRead(cmd.msg.ID, fe, relativePath, cmd.msg.Payload)
	case "write":
		result = h.handleWorkspaceWrite(cmd.msg.ID, fe, relativePath, cmd.msg.Payload)
	case "edit":
		result = h.handleWorkspaceEdit(cmd.msg.ID, fe, relativePath, cmd.msg.Payload)
	default:
		return nil, false
	}

	// Audit log: actuator_id is nil since the broker handled this directly.
	_ = h.db.LogAudit(&cmd.accountID, &cmd.agentID, nil, "workspace."+cmd.msg.Capability, relativePath)
	log.Printf("[hub:workspace] Handled %s locally for agent %s: %s", cmd.msg.Capability, cmd.agentID, relativePath)

	return result, true
}

func (h *Hub) handleWorkspaceRead(cmdID string, fe *workspace.FileExecutor, path string, payload json.RawMessage) *WSMessage {
	var p readPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusFailed, Error: "invalid read payload"}
	}

	result, err := fe.Read(path, p.Offset, p.Limit)
	if err != nil {
		return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusFailed, Error: err.Error()}
	}

	resultJSON, _ := json.Marshal(result)
	return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusCompleted, Result: resultJSON}
}

func (h *Hub) handleWorkspaceWrite(cmdID string, fe *workspace.FileExecutor, path string, payload json.RawMessage) *WSMessage {
	var p writePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusFailed, Error: "invalid write payload"}
	}

	if err := fe.Write(path, p.Content); err != nil {
		return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusFailed, Error: err.Error()}
	}

	resultJSON, _ := json.Marshal(map[string]bool{"success": true})
	return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusCompleted, Result: resultJSON}
}

func (h *Hub) handleWorkspaceEdit(cmdID string, fe *workspace.FileExecutor, path string, payload json.RawMessage) *WSMessage {
	var p editPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusFailed, Error: "invalid edit payload"}
	}

	if err := fe.Edit(path, p.OldText, p.NewText); err != nil {
		return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusFailed, Error: err.Error()}
	}

	resultJSON, _ := json.Marshal(map[string]bool{"success": true})
	return &WSMessage{Type: TypeCommandResult, ID: cmdID, Status: StatusCompleted, Result: resultJSON}
}
