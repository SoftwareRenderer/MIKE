package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mike/backend/git"
)

// The LSP tools: each resolves a symbol by text search over the task's own file access, then asks the
// language server about that position.

// lspSymbolTarget is the position, workspace and textDocument/position params every request about a
// resolved symbol carries.
type lspSymbolTarget struct {
	mgr          lspManager
	path         string
	symbol       string
	absWorkspace string
	params       map[string]any
}

// resolveLSPSymbol is the half every LSP tool shares before it asks the language server anything: the
// file-type check, the read through the task's own file access, the text search that turns a name into a
// position, and the params for it. `required` names the args that must all be non-empty.
func (r *toolRunner) resolveLSPSymbol(ctx context.Context, args map[string]any, worktreePath string, read readFunc, required ...string) (*lspSymbolTarget, string, error) {
	mgr := r.lspMgr()
	if mgr == nil {
		return nil, "", fmt.Errorf("LSP manager not available")
	}
	var missing []string
	for _, key := range required {
		if v, _ := args[key].(string); v == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, "", fmt.Errorf("%s are required", andList(missing))
	}
	path, _ := args["path"].(string)
	symbol, _ := args["symbol"].(string)

	if !mgr.IsToolCallsEnabledForFile(path) {
		return nil, "", fmt.Errorf("LSP tool calls are disabled for this file type")
	}

	absWorkspace, err := filepath.Abs(worktreePath)
	if err != nil {
		absWorkspace = worktreePath
	}

	content, err := read(ctx, path)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	position := findSymbolInText(string(content), symbol)
	if position == nil {
		// Not a failure: the sentence is the answer the model gets.
		return nil, fmt.Sprintf("Symbol %q not found in %s. Use read_file to inspect the file.", symbol, path), nil
	}

	return &lspSymbolTarget{
		mgr:          mgr,
		path:         path,
		symbol:       symbol,
		absWorkspace: absWorkspace,
		params: map[string]any{
			"textDocument": map[string]any{"uri": "file://" + filepath.Join(worktreePath, path)},
			"position":     position,
		},
	}, "", nil
}

// andList renders argument names the way the tool's error messages read: "path and symbol".
func andList(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// handleLSPSymbolRequest resolves a symbol by text search and calls the given LSP method at that
// position. ctx bounds the round trip, so stopping the task stops the tool.
func (r *toolRunner) handleLSPSymbolRequest(ctx context.Context, args map[string]any, worktreePath string, method string, extraParams map[string]any, read readFunc) (string, error) {
	target, answer, err := r.resolveLSPSymbol(ctx, args, worktreePath, read, "path", "symbol")
	if err != nil {
		return "", err
	}
	if target == nil {
		return answer, nil
	}

	params := target.params
	for k, v := range extraParams {
		params[k] = v
	}

	response, err := target.mgr.LSPRequest(ctx, target.absWorkspace, target.path, method, params)
	if err != nil {
		return "", err
	}

	return formatLSPResponse(response, method, target.absWorkspace), nil
}

// handleLSPRename renames a symbol with textDocument/rename after prepareRename, then applies the
// returned WorkspaceEdit to every target file, all of which must be inside the workspace. Writes go
// through the same sandbox-aware callbacks as write_file, so a rename lands as one checkpoint.
func (r *toolRunner) handleLSPRename(ctx context.Context, args map[string]any, worktreePath string, read readFunc, write writeFunc) (string, error) {
	target, answer, err := r.resolveLSPSymbol(ctx, args, worktreePath, read, "path", "symbol", "new_name")
	if err != nil {
		return "", err
	}
	if target == nil {
		return answer, nil
	}
	mgr, path, symbol, absWorkspace := target.mgr, target.path, target.symbol, target.absWorkspace
	newName, _ := args["new_name"].(string)
	baseParams := target.params

	// A null prepareRename result means the symbol cannot be renamed.
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

	uris := make([]string, 0, len(edits))
	for uri := range edits {
		uris = append(uris, uri)
	}
	relPaths, err := workspaceEditTargets(absWorkspace, uris)
	if err != nil {
		return "", err
	}

	// Sorted so the writes land in a fixed order. A failure aborts: files already written stay in the
	// same checkpoint, so the diff shows the partial state.
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
