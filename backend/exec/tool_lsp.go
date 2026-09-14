package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"kanban/backend/git"
)

// The LSP tools. Each one resolves a symbol by text search in the task's own file access, then asks
// the language server about that position.

// handleLSPSymbolRequest resolves a symbol name to a position via text search,
// then calls the given LSP method at that position. ctx bounds the language-server
// round trip, so stopping the task stops the tool. The file is read through the task's
// own file access, so a sandboxed task resolves its symbol in the container.
func (r *toolRunner) handleLSPSymbolRequest(ctx context.Context, args map[string]any, worktreePath string, method string, extraParams map[string]any, read readFunc) (string, error) {
	mgr := r.lspMgr()
	if mgr == nil {
		return "", fmt.Errorf("LSP manager not available")
	}
	path, _ := args["path"].(string)
	symbol, _ := args["symbol"].(string)
	if path == "" || symbol == "" {
		return "", fmt.Errorf("path and symbol are required")
	}

	// Check if tool calls are enabled for the LSP extension that handles this file
	if !mgr.IsToolCallsEnabledForFile(path) {
		return "", fmt.Errorf("LSP tool calls are disabled for this file type")
	}

	absPath := filepath.Join(worktreePath, path)
	absWorkspace, err := filepath.Abs(worktreePath)
	if err != nil {
		absWorkspace = worktreePath
	}

	// Read the file and find the symbol by text search
	content, err := read(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	position := findSymbolInText(string(content), symbol)
	if position == nil {
		return fmt.Sprintf("Symbol %q not found in %s. Use read_file to inspect the file.", symbol, path), nil
	}

	// Build the LSP request with the resolved position
	uri := "file://" + absPath
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
	}
	for k, v := range extraParams {
		params[k] = v
	}

	response, err := mgr.LSPRequest(ctx, absWorkspace, path, method, params)
	if err != nil {
		return "", err
	}

	return formatLSPResponse(response, method, absWorkspace), nil
}

// handleLSPRename renames a symbol across files with textDocument/rename, validating with
// prepareRename first and then applying the returned WorkspaceEdit to every target file (all
// must be inside the workspace). Writes go through the same sandbox-aware callbacks as
// write_file, so a rename lands as one checkpoint's worth of changes in the task diff.
func (r *toolRunner) handleLSPRename(ctx context.Context, args map[string]any, worktreePath string, read readFunc, write writeFunc) (string, error) {
	mgr := r.lspMgr()
	if mgr == nil {
		return "", fmt.Errorf("LSP manager not available")
	}
	path, _ := args["path"].(string)
	symbol, _ := args["symbol"].(string)
	newName, _ := args["new_name"].(string)
	if path == "" || symbol == "" || newName == "" {
		return "", fmt.Errorf("path, symbol, and new_name are required")
	}
	if !mgr.IsToolCallsEnabledForFile(path) {
		return "", fmt.Errorf("LSP tool calls are disabled for this file type")
	}

	absPath := filepath.Join(worktreePath, path)
	absWorkspace, err := filepath.Abs(worktreePath)
	if err != nil {
		absWorkspace = worktreePath
	}

	// Resolve the symbol position by text search (same as the other LSP tools).
	content, err := read(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	position := findSymbolInText(string(content), symbol)
	if position == nil {
		return fmt.Sprintf("Symbol %q not found in %s. Use read_file to inspect the file.", symbol, path), nil
	}

	uri := "file://" + absPath
	baseParams := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
	}

	// prepareRename: a null result means the symbol cannot be renamed.
	prepareResp, err := mgr.LSPRequest(ctx, absWorkspace, path, "textDocument/prepareRename", baseParams)
	if err != nil {
		return "", err
	}
	var preEnvelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(prepareResp, &preEnvelope); err != nil {
		return "", fmt.Errorf("unmarshal prepareRename response: %w", err)
	}
	if len(preEnvelope.Result) == 0 || string(preEnvelope.Result) == "null" {
		return fmt.Sprintf("Symbol %q in %s is not renamable by the language server.", symbol, path), nil
	}

	// rename
	renameParams := map[string]any{
		"textDocument": baseParams["textDocument"],
		"position":     baseParams["position"],
		"newName":      newName,
	}
	renameResp, err := mgr.LSPRequest(ctx, absWorkspace, path, "textDocument/rename", renameParams)
	if err != nil {
		return "", err
	}
	edits, err := parseWorkspaceEdit(renameResp)
	if err != nil {
		return "", err
	}
	if edits == nil {
		return fmt.Sprintf("Rename of %q produced no edits (symbol may not be renamable).", symbol), nil
	}

	// Validate all targets are inside the workspace.
	uris := make([]string, 0, len(edits))
	for uri := range edits {
		uris = append(uris, uri)
	}
	relPaths, err := workspaceEditTargets(absWorkspace, uris)
	if err != nil {
		return "", err
	}

	// Apply file by file (deterministic order). Any failure aborts; files
	// already written are part of the same checkpoint, so the diff shows the
	// partial state and the agent can recover.
	sort.Strings(uris)
	var fileSummary []string
	for _, uri := range uris {
		rel := relPaths[uri]
		data, err := read(ctx, rel)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		newContent, err := applyTextEdits(string(data), edits[uri])
		if err != nil {
			return "", fmt.Errorf("apply edits to %s: %w", rel, err)
		}
		if err := write(ctx, rel, newContent); err != nil {
			return "", fmt.Errorf("write %s: %w", rel, err)
		}
		fileSummary = append(fileSummary, fmt.Sprintf("%s (%d edit%s)", rel, len(edits[uri]), git.PluralS(len(edits[uri]))))
	}

	return fmt.Sprintf("Renamed %q to %q in %d file(s): %s", symbol, newName, len(uris), strings.Join(fileSummary, ", ")), nil
}
