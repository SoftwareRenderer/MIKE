package git

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"mike/backend/fsutil"
	"mike/backend/types"
	"mike/pkg/logging"
)

// maxDiffSize is the largest file whose contents a diff carries: past it the hunk is replaced by a
// size annotation, because the panel cannot show a megabyte of text and the model cannot read one.
const maxDiffSize = 1 << 20

// commitToCommitDiff runs git diff baseRef..headRef in dir; an empty baseRef means HEAD. The shortstat
// comes off the unfiltered patch, so it still counts files the binary filter strips.
func (wm *WorktreeManager) commitToCommitDiff(dir, baseRef, headRef string) (diff string, shortStat string, annotations map[string]*fileAnnotation, err error) {
	target := baseRef
	if target == "" {
		target = "HEAD"
	}
	diff, err = Run(dir, "diff", fmt.Sprintf("%s..%s", target, headRef))
	if err != nil {
		return "", "", nil, fmt.Errorf("git diff: %w", err)
	}
	shortStat = shortStatFromDiff(diff)
	diff, annotations = wm.filterLargeBinaryDiffs(dir, diff)
	return diff, shortStat, annotations, nil
}

// GetTaskDiff returns the git diff for a task's worktree.
// baseRef is the branch/ref to diff against (e.g., "master"). If empty, uses HEAD.
// headRef is an optional second ref for comparing two commits (git diff baseRef..headRef).
// When headRef is set the answer is those two trees and nothing else: no untracked files, and no
// committed half taken from HEAD.
// If skipTruncate is true, the diff is not truncated (used for ?full=1 API requests).
func (wm *WorktreeManager) GetTaskDiff(taskID, baseRef, headRef string, skipTruncate ...bool) (*TaskDiff, error) {
	worktreePath, err := wm.taskWorktree(taskID)
	if err != nil {
		return nil, err
	}

	// Reject invalid baseRef values (e.g. git error messages like "fatal: ...")
	if strings.HasPrefix(baseRef, "fatal:") {
		baseRef = ""
	}

	var diffOut, shortStat string
	var untracked []UntrackedFile
	var annotations map[string]*fileAnnotation

	if headRef != "" {
		// Two-ref diff: compare baseRef..headRef (commit-to-commit)
		diffOut, shortStat, annotations, err = wm.commitToCommitDiff(worktreePath, baseRef, headRef)
		if err != nil {
			return nil, err
		}
		// No untracked files for commit-to-commit diff
	} else {
		// Working tree vs HEAD: only uncommitted changes, since committed ones are handled
		// separately below. Checkpoints use stash create+store, so changes stay visible here.
		diffOut, err = Run(worktreePath, "diff", "HEAD")
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				// Changes exist, diffOut has the output — continue
			} else {
				return nil, fmt.Errorf("git diff: %w", err)
			}
		}
		shortStat = shortStatFromDiff(diffOut)
		diffOut, annotations = wm.filterLargeBinaryDiffs(worktreePath, diffOut)

		// Get untracked files from git status --porcelain (lines starting with "?? ")
		statusOut, err := Run(worktreePath, "status", "--porcelain")
		if err != nil {
			return nil, fmt.Errorf("git status: %w", err)
		}

		for line := range strings.SplitSeq(statusOut, "\n") {
			if len(line) >= 4 && strings.HasPrefix(line, "?? ") {
				path := strings.TrimSpace(line[3:])
				if path == "" {
					continue
				}
				// Expand directories into individual file paths
				filePaths := walkUntrackedEntries(worktreePath, path)
				for _, filePath := range filePaths {
					// Skip files in ignored directories (safety net for tracked files)
					if ShouldIgnorePath(filePath) {
						continue
					}
					// The read target, not the status path: an untracked symlink names a file
					// somewhere else, and "somewhere else" may be a private key.
					fullPath, fileSize, err := UntrackedReadTarget(worktreePath, filePath)
					if err != nil {
						logging.For(logging.Git).Debugf("untracked %s: skipped: %v", filePath, err)
						continue
					}
					// Skip content for large files or binary files.
					// Binary detection: extension check first (O(1)), null-byte scan as fallback.
					isBin := isBinaryExtension(filePath) || isBinaryContent(fullPath, fileSize)
					if fileSize > maxDiffSize || isBin {
						untracked = append(untracked, UntrackedFile{
							Path:     filePath,
							FileSize: fileSize,
							IsBinary: isBin,
						})
						continue
					}
					// Read file content directly (untracked = new file, all lines are additions)
					data, err := os.ReadFile(fullPath)
					if err != nil {
						continue
					}
					fileLines := SplitLines(string(data))
					untracked = append(untracked, UntrackedFile{Path: filePath, Lines: fileLines})
				}
			}
		}
	}

	doTruncate := len(skipTruncate) == 0 || !skipTruncate[0]
	var truncatedFiles []string
	var originalStats map[string]FileChangeStats
	if doTruncate {
		originalStats = countFileChanges(diffOut)
		diffOut, truncatedFiles = truncateDiffLines(diffOut, defaultMaxChangeLines)
		// Truncate untracked file lines (all additions for new files)
		for i := range untracked {
			if len(untracked[i].Lines) == 0 {
				continue
			}
			if len(untracked[i].Lines) > defaultMaxChangeLines {
				untracked[i].OrigAdded = len(untracked[i].Lines)
				untracked[i].Lines = untracked[i].Lines[:defaultMaxChangeLines]
				untracked[i].Truncated = true
			}
		}
	}

	// Compute staged hash so the diff hash changes on git add/reset.
	// Always compare against HEAD (current commit), NOT baseRef.
	// baseRef may be a different branch (e.g. "develop") and comparing
	// index vs develop would show ALL branch divergence as "staged".
	var stagedHash string
	var stagedPaths map[string]struct{}
	if headRef == "" {
		// git diff exits 1 when changes exist — output is still valid.
		stagedOut, stagedErr := Run(worktreePath, "diff", "--cached", "HEAD")
		if stagedErr != nil {
			if exitErr, ok := stagedErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				stagedHash = fmt.Sprintf("%x", sha256.Sum256([]byte(stagedOut)))
			}
		} else {
			stagedHash = fmt.Sprintf("%x", sha256.Sum256([]byte(stagedOut)))
		}
		// Get set of staged file paths to mark Staged on tracked entries.
		stagedNames, namesErr := Run(worktreePath, "diff", "--cached", "--name-only", "HEAD")
		if namesErr != nil {
			if exitErr, ok := namesErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				// Changes exist, output is valid
			} else {
				stagedNames = ""
			}
		}
		if stagedNames != "" {
			stagedPaths = make(map[string]struct{})
			for line := range strings.SplitSeq(stagedNames, "\n") {
				p := strings.TrimSpace(line)
				if p != "" {
					stagedPaths[p] = struct{}{}
				}
			}
		}
	}

	tracked := parseUnifiedDiffToFiles(diffOut, truncatedFiles, originalStats, annotations)
	if tracked == nil {
		tracked = make(map[string]*TrackedDiffFile)
	}
	// The committed half belongs to the working-tree comparison, where HEAD is one of the two trees. A ref
	// comparison already names both trees, so merging HEAD in lists work done after the head ref.
	var committedDiff string
	if baseRef != "" && headRef == "" {
		committedOut, err := Run(worktreePath, "diff", fmt.Sprintf("%s..HEAD", baseRef))
		if err == nil && committedOut != "" {
			committedDiff = committedOut
			committedOut, committedAnnotations := wm.filterLargeBinaryDiffs(worktreePath, committedOut)
			committedFiles := parseUnifiedDiffToFiles(committedOut, nil, nil, committedAnnotations)
			for path, cf := range committedFiles {
				if tf, ok := tracked[path]; ok {
					tf.CommittedRows = cf.Rows
				} else {
					tracked[path] = &TrackedDiffFile{
						Path:          path,
						Rows:          nil,
						CommittedRows: cf.Rows,
						Added:         cf.Added,
						Removed:       cf.Removed,
						FileSize:      cf.FileSize,
						IsBinary:      cf.IsBinary,
						Truncated:     cf.Truncated,
						OrigAdded:     cf.OrigAdded,
						OrigRemoved:   cf.OrigRemoved,
					}
				}
			}
		}
	}

	// Convert to sorted slice for deterministic WebSocket broadcast.
	var stagedPathsList []string
	if stagedPaths != nil {
		stagedPathsList = make([]string, 0, len(stagedPaths))
		for p := range stagedPaths {
			stagedPathsList = append(stagedPathsList, p)
		}
	}

	return &TaskDiff{
		Diff:              diffOut,
		CommittedDiff:     committedDiff,
		ComparesRefs:      headRef != "",
		Tracked:           tracked,
		Untracked:         untracked,
		ShortStat:         shortStat,
		TruncatedFiles:    truncatedFiles,
		OriginalFileStats: originalStats,
		StagedHash:        stagedHash,
		StagedPaths:       stagedPathsList,
	}, nil
}

// parseUnifiedDiffToFiles parses a unified diff into per-file TrackedDiffFile entries.
// This mirrors the frontend parseUnifiedDiff but runs on the backend for caching.
// annotations provides metadata for binary/large files whose hunks were replaced.
func parseUnifiedDiffToFiles(diff string, truncatedFiles []string, origStats map[string]FileChangeStats, annotations map[string]*fileAnnotation) map[string]*TrackedDiffFile {
	if diff == "" || !strings.Contains(diff, "\n") {
		// Even with empty diff, annotations may have binary/large file entries
		if len(annotations) > 0 {
			return annotationsToFiles(annotations)
		}
		return nil
	}

	truncatedSet := make(map[string]bool, len(truncatedFiles))
	for _, f := range truncatedFiles {
		truncatedSet[f] = true
	}

	files := make(map[string]*TrackedDiffFile)
	var current *TrackedDiffFile
	var oldLine, newLine int
	var inHunk bool

	lines := strings.Split(diff, "\n")
	// Remove trailing empty line
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			_, bPath := extractDiffPaths(line)
			// If the file already exists (e.g. from a previous diff segment),
			// merge the new hunks into the existing entry instead of overwriting.
			if existing, ok := files[bPath]; ok {
				current = existing
			} else {
				current = &TrackedDiffFile{Path: bPath, Rows: nil}
				files[bPath] = current
			}
			inHunk = false
			oldLine = 0
			newLine = 0
			continue
		}
		if strings.HasPrefix(line, "new file mode ") {
			if current != nil {
				current.IsNew = true
			}
			continue
		}
		if strings.HasPrefix(line, "deleted file mode ") {
			if current != nil {
				current.IsDeleted = true
			}
			continue
		}
		// A file that did not exist before has no old side to diff against. git normally also writes
		// `new file mode`, but not always — the frontend parser read /dev/null as a signal of its own,
		// and a brand-new file shown as modified is a badge lie rather than a cosmetic miss.
		if line == "--- /dev/null" {
			if current != nil {
				current.IsNew = true
			}
			continue
		}
		if line == "+++ /dev/null" {
			if current != nil {
				current.IsDeleted = true
			}
			continue
		}
		// git's own verdict that the file cannot be diffed as text. Without this the notice was dropped
		// on the floor: a binary file in a commit-to-commit diff (which has no annotations — only the
		// task-diff pipeline produces those) came back with no size, no flag and no rows, so the panel
		// rendered an empty file that looks like a bug rather than a file git refuses to diff.
		if strings.HasPrefix(line, "Binary files ") {
			if current != nil {
				current.IsBinary = true
				// The row is emitted only when there is no annotation for this file. filterLargeBinaryDiffs
				// keeps this line AND appends `# binary-file: N`, and the task panel renders such a file
				// from FileSize/IsBinary — adding a row there would print the notice as prose under the
				// size label. With no annotation, this line is the only explanation the file will get.
				if annotations[current.Path] == nil {
					current.Rows = append(current.Rows, DiffRow{LineNumber: nil, Variant: "context", Text: line})
				}
			}
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		// Check for binary/large file annotation
		if strings.HasPrefix(line, "# binary-file: ") || strings.HasPrefix(line, "# large-file: ") {
			if current != nil {
				isBinary := strings.HasPrefix(line, "# binary-file: ")
				sizeStr := strings.TrimPrefix(line, "# binary-file: ")
				if !isBinary {
					sizeStr = strings.TrimPrefix(line, "# large-file: ")
				}
				if size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64); err == nil {
					current.FileSize = &size
					current.IsBinary = isBinary
					// Enrich with annotation metadata (origPath for renames)
					if ann := annotations[current.Path]; ann != nil && ann.origPath != "" {
						current.OrigPath = ann.origPath
					}
				}
			}
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "@@") {
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				// With no numbers there is nothing to number rows with, and the previous hunk's counters
				// would place them at lines the file does not have.
				inHunk = false
				continue
			}
			inHunk = true
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[3])
			continue
		}
		if !inHunk || current == nil {
			continue
		}

		if line == "\\ No newline at end of file" {
			continue
		}

		var variant string
		switch {
		case strings.HasPrefix(line, "+"):
			variant = "added"
			current.Added++
		case strings.HasPrefix(line, "-"):
			variant = "removed"
			current.Removed++
		case strings.HasPrefix(line, " "):
			variant = "context"
		case line == "":
			// An unchanged empty line is sometimes written as nothing rather than " "; dropping it would
			// shift every later number in the hunk.
			variant = "context"
		default:
			continue
		}

		ln := newLine
		if variant == "removed" {
			ln = oldLine
		}
		var text string
		if len(line) > 0 {
			text = line[1:]
		}
		row := DiffRow{
			LineNumber: &ln,
			Variant:    variant,
			Text:       text,
		}
		current.Rows = append(current.Rows, row)

		if variant == "context" || variant == "removed" {
			oldLine++
		}
		if variant == "context" || variant == "added" {
			newLine++
		}
	}

	// Enrich with truncation metadata + annotation data
	for path, f := range files {
		if len(f.Rows) == 0 && f.FileSize == nil && !f.IsNew && !f.IsDeleted {
			// Skip files with no diff content, but preserve new/deleted files
			// (a new or deleted blank file has a diff header but no hunk rows).
			delete(files, path)
			continue
		}
		if truncatedSet[path] {
			f.Truncated = true
			if s, ok := origStats[path]; ok {
				f.OrigAdded = s.Added
				f.OrigRemoved = s.Removed
			}
		}
		// Enrich with annotation metadata for renames
		if f.FileSize != nil {
			if ann := annotations[path]; ann != nil && ann.origPath != "" {
				f.OrigPath = ann.origPath
			}
		}
	}

	return files
}

// truncateDiffLines truncates a unified diff so each file has at most maxChangeLines change
// lines. Lines are dropped from the end of a hunk and the @@ header counts are updated to stay
// valid. Returns the truncated diff and the paths that were cut.
func truncateDiffLines(diff string, maxChangeLines int) (string, []string) {
	if !strings.Contains(diff, "\n") {
		return diff, nil
	}

	lines := strings.Split(diff, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Split into per-file blocks
	type fileBlock struct {
		path  string
		lines []string
	}
	var blocks []fileBlock
	var cur *fileBlock

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			if cur != nil {
				blocks = append(blocks, *cur)
			}
			// Named by diffBlockPath, so the truncation is reported to the right file.
			cur = &fileBlock{path: diffBlockPath(line), lines: []string{line}}
			continue
		}
		if cur != nil {
			cur.lines = append(cur.lines, line)
		}
	}
	if cur != nil {
		blocks = append(blocks, *cur)
	}

	var result []string
	var truncatedFiles []string

	for _, block := range blocks {
		// Count total change lines
		totalChanges := 0
		for _, line := range block.lines {
			if len(line) > 0 && (line[0] == '+' || line[0] == '-') &&
				!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				totalChanges++
			}
		}

		needsTruncation := totalChanges > maxChangeLines
		if needsTruncation {
			truncatedFiles = append(truncatedFiles, block.path)
		}

		if !needsTruncation {
			result = append(result, block.lines...)
			continue
		}

		// Truncate: walk lines, keep first maxChangeLines changes.
		// When limit is hit mid-hunk, update the @@ header counts and stop.
		keptChanges := 0
		inHunk := false
		var hunkResultIdx int
		var hunkOldStart, hunkNewStart int
		var hunkContext string
		var hunkOldLines, hunkNewLines int

		for _, line := range block.lines {
			if strings.HasPrefix(line, "@@") {
				// If we already hit the limit in a previous hunk, stop
				if keptChanges >= maxChangeLines {
					break
				}
				m := hunkRe.FindStringSubmatch(line)
				if m == nil {
					// A header this function cannot read is one it must not rewrite.
					inHunk = false
					result = append(result, line)
					continue
				}
				inHunk = true
				hunkResultIdx = len(result)
				hunkOldLines = 0
				hunkNewLines = 0
				hunkOldStart, _ = strconv.Atoi(m[1])
				hunkNewStart, _ = strconv.Atoi(m[3])
				hunkContext = m[5]
				result = append(result, line)
				continue
			}
			if !inHunk {
				result = append(result, line)
				continue
			}

			// Track old/new line counts
			if len(line) > 0 {
				if line[0] == ' ' || line[0] == '-' {
					hunkOldLines++
				}
				if line[0] == ' ' || line[0] == '+' {
					hunkNewLines++
				}
			}

			isChange := len(line) > 0 && (line[0] == '+' || line[0] == '-')
			if isChange {
				keptChanges++
				if keptChanges > maxChangeLines {
					// Remove overflow line from counts (it won't be appended),
					// then break to continue with the next file block.
					if line[0] == '-' {
						hunkOldLines--
					} else {
						hunkNewLines--
					}
					break
				}
			}
			result = append(result, line)
		}

		// Update header for last hunk (counts may differ from original)
		if inHunk {
			result[hunkResultIdx] = fmt.Sprintf("@@ -%d,%d +%d,%d %s@@",
				hunkOldStart, hunkOldLines, hunkNewStart, hunkNewLines, hunkContext)
		}
	}

	return strings.Join(result, "\n"), truncatedFiles
}

// filterLargeBinaryDiffs replaces diff hunks for binary or >1MB files with size annotations.
// Returns the filtered diff and a map of file annotations for the parser to consume.
// Binary detection uses git's own "Binary files a/X and b/Y differ" marker.
// Large-file detection uses os.Stat() (>1MB text files).
func (wm *WorktreeManager) filterLargeBinaryDiffs(worktreePath, diffText string) (string, map[string]*fileAnnotation) {
	if diffText == "" {
		return diffText, nil
	}
	annotations := make(map[string]*fileAnnotation)
	var result strings.Builder
	lines := strings.Split(diffText, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "diff --git ") {
			// Extract a/ and b/ paths from "diff --git a/X b/Y"
			aPath, bPath := extractDiffPaths(line)

			// Scan header lines for "Binary files a/X and b/Y differ" marker
			// and the index line for blob hashes (fallback size lookup).
			isBinary := false
			var binarySize int64
			var blobHash string
			// Write the "diff --git" line and advance past it
			result.WriteString(line + "\n")
			i++
			for i < len(lines) {
				l := lines[i]
				if strings.HasPrefix(l, "@@ ") || strings.HasPrefix(l, "diff --git ") {
					break
				}
				if strings.HasPrefix(l, "Binary files ") {
					isBinary = true
					binarySize = extractBinarySize(l)
				}
				if strings.HasPrefix(l, "index ") && blobHash == "" {
					// index abc123..def456 mode — grab the second hash (new blob)
					blobHash = extractBlobHash(l)
				}
				result.WriteString(l + "\n")
				i++
			}

			if isBinary {
				// Git confirmed binary — resolve size.
				if binarySize == 0 {
					checkPath := bPath
					if checkPath == "" || strings.Contains(line, "/dev/null") {
						checkPath = aPath
					}
					if size, ok := getFileSize(worktreePath, checkPath, blobHash, wm); ok {
						binarySize = size
					}
				}
				result.WriteString(fmt.Sprintf("# binary-file: %d\n", binarySize))
				for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					i++
				}
				annotations[bPath] = &fileAnnotation{
					path:     bPath,
					origPath: aPath,
					size:     binarySize,
					isBinary: true,
				}
				continue
			}

			// Not binary — check if it's a large text file.
			if size, ok := getFileSize(worktreePath, bPath, "", wm); ok && size > maxDiffSize {
				result.WriteString(fmt.Sprintf("# large-file: %d\n", size))
				for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					i++
				}
				annotations[bPath] = &fileAnnotation{
					path:     bPath,
					origPath: aPath,
					size:     size,
					isBinary: false,
				}
				continue
			}
			// Normal text file — headers already written by inner loop.
			// Continue outer loop to process hunk lines from current i position.
			continue
		}
		result.WriteString(line + "\n")
		i++
	}
	out := result.String()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, annotations
}

// walkUntrackedEntries expands git status --porcelain "?? " entries into individual file paths.
// When a whole directory is untracked, git status returns "?? dir/" — this function walks
// the directory and returns all regular file paths (relative to worktreePath).
func walkUntrackedEntries(worktreePath, path string) []string {
	fullPath := filepath.Join(worktreePath, path)
	// Lstat, not Stat: a symlink is neither a regular file nor a directory, so it is listed
	// neither here nor walked into. UntrackedReadTarget judges its target.
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil
	}
	if info.Mode().IsRegular() {
		if ShouldIgnoreFile(path) {
			return nil
		}
		return []string{path}
	}
	if !info.IsDir() {
		return nil
	}
	// It's a directory — walk it recursively
	var files []string
	_ = filepath.Walk(fullPath, func(fp string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(worktreePath, fp)
		if err != nil {
			return nil
		}
		if ShouldIgnoreFile(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files
}

// TaskDiffFromRepo computes the commit-to-commit TaskDiff of a repository directory, for a task whose
// worktree is gone.
func (wm *WorktreeManager) TaskDiffFromRepo(repoPath, baseRef, headRef string, skipTruncate bool) (*TaskDiff, error) {
	if repoPath == "" || headRef == "" {
		return nil, fmt.Errorf("TaskDiffFromRepo: repoPath and headRef are required")
	}
	diffOut, shortStat, annotations, err := wm.commitToCommitDiff(repoPath, baseRef, headRef)
	if err != nil {
		return nil, err
	}

	var truncatedFiles []string
	var originalStats map[string]FileChangeStats
	if !skipTruncate {
		originalStats = countFileChanges(diffOut)
		diffOut, truncatedFiles = truncateDiffLines(diffOut, defaultMaxChangeLines)
	}
	tracked := parseUnifiedDiffToFiles(diffOut, truncatedFiles, originalStats, annotations)
	if tracked == nil {
		tracked = make(map[string]*TrackedDiffFile)
	}
	return &TaskDiff{
		Diff:              diffOut,
		ComparesRefs:      true,
		Tracked:           tracked,
		Untracked:         []UntrackedFile{},
		ShortStat:         shortStat,
		TruncatedFiles:    truncatedFiles,
		OriginalFileStats: originalStats,
	}, nil
}

// extractDiffPaths pulls both a/ and b/ paths from "diff --git a/X b/Y" header.
func extractDiffPaths(line string) (aPath, bPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")

	// git C-quotes a path containing a double quote, backslash or control character, so the header
	// arrives as `diff --git "a/quo\"te.ts" "b/quo\"te.ts"`. Unhandled, the `a/` test fails and the
	// ` b/` separator sits inside the quoting, so every such file in a diff merges into one "" entry.
	if strings.HasPrefix(rest, `"`) {
		aTok, bTok := splitQuotedPathPair(rest)
		// The a/ and b/ prefixes are INSIDE the quoting (git quotes the whole `b/path` token), so they
		// are stripped after unquoting, not before.
		return stripDiffSide(unquoteGitPath(aTok), "a/"), stripDiffSide(unquoteGitPath(bTok), "b/")
	}

	// Parse "a/path b/path"
	if !strings.HasPrefix(rest, "a/") {
		return "", ""
	}
	path := rest[2:] // strip "a/"
	aPath = path
	// Find " b/" separator
	if idx := strings.Index(path, " b/"); idx >= 0 {
		aPath = path[:idx]
		bPath = path[idx+3:] // strip " b/"
	} else {
		bPath = aPath
	}
	return aPath, bPath
}

// diffBlockPath names the file a `diff --git` header opens. An unreadable header gets "unknown", a key no
// file ever asks for.
func diffBlockPath(line string) string {
	if _, bPath := extractDiffPaths(line); bPath != "" {
		return bPath
	}
	return "unknown"
}

// TaskDiffBaseline is the ref a task's diff, commit list and diff stats are computed against, and the only
// way to ask. It answers the pinned commit, else a divergence point when the recorded ref is gone, else the
// fork thread's merge-base, degrading to the next link instead of failing.
func (wm *WorktreeManager) TaskDiffBaseline(task *types.Task) string {
	if wm == nil || wm.store == nil || task == nil {
		return ""
	}
	projectPath, originBranch := "", ""
	if project, err := wm.store.GetProject(task.ProjectID); err == nil && project != nil {
		projectPath, originBranch = project.Path, project.OriginBranch
	}
	baseRef := wm.resolveTaskBaseline(projectPath, originBranch, task)
	forkBranch := ""
	if thread, err := wm.store.GetThread(task.CurrentThreadID); err == nil && thread != nil {
		forkBranch = thread.GitBranch
	}
	return wm.resolveDiffBaseline(task.ID, baseRef, task.Branch, forkBranch)
}

// resolveDiffBaseline computes the ref to diff a task against: baseRef for the original thread, the
// merge-base of forkBranch and taskBranch for a fork, and baseRef again when the task branch *is* the thread
// branch. A merge-base it cannot find is nothing a caller can act on, so it answers baseRef.
func (wm *WorktreeManager) resolveDiffBaseline(taskID, baseRef, taskBranch, forkBranch string) string {
	// Reject invalid baseRef values (e.g. git error messages like "fatal: ...")
	if strings.HasPrefix(baseRef, "fatal:") {
		baseRef = ""
	}
	if forkBranch == "" {
		// Original thread — diff against base_ref
		return baseRef
	}
	if forkBranch == taskBranch {
		// Task branch was switched to thread branch — diff against original baseline
		return baseRef
	}
	// Forked thread — find the divergence point via merge-base
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return baseRef
	}
	mb, err := Run(worktreePath, "merge-base", forkBranch, taskBranch)
	if err != nil {
		return baseRef
	}
	return strings.TrimSpace(mb)
}

// shortStatFromDiff computes a git-style shortstat: "X file(s) changed, Y insertion(s)(+),
// Z deletion(s)(-)", omitting zero-value parts.
func shortStatFromDiff(diff string) string {
	stats := countFileChanges(diff)
	files := len(stats)
	var totalAdded, totalRemoved int
	for _, s := range stats {
		totalAdded += s.Added
		totalRemoved += s.Removed
	}
	if files == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	parts = append(parts, fmt.Sprintf("%d file%s changed", files, PluralS(files)))
	if totalAdded > 0 {
		parts = append(parts, fmt.Sprintf("%d insertion%s(+)", totalAdded, PluralS(totalAdded)))
	}
	if totalRemoved > 0 {
		parts = append(parts, fmt.Sprintf("%d deletion%s(-)", totalRemoved, PluralS(totalRemoved)))
	}
	return strings.Join(parts, ", ")
}

// UntrackedReadTarget resolves a worktree-relative path to the host file whose content may go
// into a diff, plus its size, and refuses anything that is not a regular file inside the
// worktree. git status lists a symlink the way it lists a file, so without this a
// `key -> ../../.ssh/id_rsa` planted in a task worktree puts a private key in the task diff.
func UntrackedReadTarget(worktreePath, rel string) (string, int64, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", 0, fmt.Errorf("path %q must be relative to the worktree", rel)
	}
	resolved, err := fsutil.Contained(filepath.Join(worktreePath, rel), []string{filepath.Clean(worktreePath)})
	if err != nil {
		return "", 0, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", rel)
	}
	return resolved, info.Size(), nil
}

// stashUntrackedCommit returns the commit holding a stash entry's untracked files, when hash names a
// stash commit made with -u. Git keeps those files only in the third parent, so reading the stash
// commit's own tree would lose them.
func (wm *WorktreeManager) stashUntrackedCommit(dir, hash string) (string, bool) {
	out, err := Run(dir, "rev-list", "--parents", "-n1", hash)
	if err != nil {
		return "", false
	}
	ancestors := strings.Fields(out)
	if len(ancestors) != 4 {
		return "", false // not a stash commit with an untracked parent
	}
	candidate := ancestors[3]
	files, err := Run(dir, "ls-tree", "-r", "--name-only", candidate)
	if err != nil || strings.TrimSpace(files) == "" {
		return "", false // nothing to overlay
	}
	return candidate, true
}

// extractBlobHash extracts the second (new) hash from "index abc..def mode" line.
func extractBlobHash(line string) string {
	line = strings.TrimPrefix(line, "index ")
	parts := strings.SplitN(line, "..", 2)
	if len(parts) == 2 {
		// Second hash may be followed by " mode..."
		hash := strings.SplitN(parts[1], " ", 2)[0]
		return hash
	}
	return ""
}

// ComputeUntrackedHash computes a SHA-256 hash for an untracked file based on its lines.
func ComputeUntrackedHash(u UntrackedFile) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.Path+strings.Join(u.Lines, "\n"))))
}

// ParseUnifiedDiff parses raw git diff output into per-file entries, sorted by path. It is the only
// diff grammar in the product: ?parsed=1 ships its output and the panel never re-parses patch text.
// backend/git/diff_parse_parity_test.go holds the parity cases.
func ParseUnifiedDiff(patch string) []TrackedDiffFile {
	return SortedParsedFiles(parseUnifiedDiffToFiles(patch, nil, nil, nil))
}

// stripDiffSide removes a leading "a/" or "b/" diff-side prefix.
func stripDiffSide(path, side string) string {
	return strings.TrimPrefix(path, side)
}

// SortedParsedFiles flattens a parsed map into path order, so a response never inherits Go's map
// iteration order.
func SortedParsedFiles(tracked map[string]*TrackedDiffFile) []TrackedDiffFile {
	out := make([]TrackedDiffFile, 0, len(tracked))
	for _, f := range tracked {
		if f != nil {
			out = append(out, *f)
		}
	}
	slices.SortFunc(out, func(a, b TrackedDiffFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

// annotationsToFiles creates TrackedDiffFile entries from annotations (for empty diffs with binary files).
func annotationsToFiles(annotations map[string]*fileAnnotation) map[string]*TrackedDiffFile {
	files := make(map[string]*TrackedDiffFile, len(annotations))
	for path, ann := range annotations {
		f := &TrackedDiffFile{
			Path:     ann.path,
			FileSize: &ann.size,
			IsBinary: ann.isBinary,
		}
		if ann.origPath != "" {
			f.OrigPath = ann.origPath
		}
		files[path] = f
	}
	return files
}
