package grep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/internal/toolutil"
)

// grepOptions is the fully-normalized configuration of one grep run:
// validated args, compiled pattern, context windows, and filters.
type grepOptions struct {
	args                     *grepArgs
	re                       *regexp.Regexp
	beforeCtx, afterCtx      int
	maxResults               int
	includeGlob, excludeGlob *globMatcher
	typeFilter               func(string) bool
}

// parseGrepArgs validates the tool arguments and normalizes every option
// (smart case, context windows, result caps, globs, type filter).
func parseGrepArgs(argsJSON string) (*grepOptions, error) {
	var args grepArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil, fmt.Errorf("grep: invalid args: %w", err)
	}
	if args.Pattern == "" {
		return nil, fmt.Errorf("grep: pattern is required")
	}

	smartCase := args.Pattern == strings.ToLower(args.Pattern)
	ignoreCase := args.IgnoreCase
	if !ignoreCase && smartCase {
		ignoreCase = true
	}

	re, err := compilePattern(args.Pattern, args.Literal, ignoreCase)
	if err != nil {
		return nil, fmt.Errorf("grep: invalid pattern: %w", err)
	}

	beforeCtx := args.BeforeCtx
	afterCtx := args.AfterCtx
	if args.Context > 0 {
		if beforeCtx == 0 {
			beforeCtx = args.Context
		}
		if afterCtx == 0 {
			afterCtx = args.Context
		}
	}
	if beforeCtx < 0 {
		beforeCtx = 0
	}
	if afterCtx < 0 {
		afterCtx = 0
	}

	maxResults := args.MaxResult
	if maxResults <= 0 {
		maxResults = 50
	}

	o := &grepOptions{
		args:       &args,
		re:         re,
		beforeCtx:  beforeCtx,
		afterCtx:   afterCtx,
		maxResults: maxResults,
	}
	if args.Include != "" {
		o.includeGlob = newGlobMatcher(args.Include)
	}
	if args.Exclude != "" {
		o.excludeGlob = newGlobMatcher(args.Exclude)
	}
	if args.Type != "" {
		extSet := toolutil.TypeExts(args.Type)
		o.typeFilter = func(path string) bool {
			return extSet[strings.ToLower(filepath.Ext(path))]
		}
	}
	return o, nil
}

// noMatchResult is the honest zero-hit answer for both search modes.
func noMatchResult(pattern string) string {
	return toolutil.FormatResult(fmt.Sprintf("grep: no matches found for %q", pattern), map[string]any{
		"matches": 0,
		"files":   0,
		"output":  "",
	})
}

// grepSingleFile searches one explicit file path.
func (t *grepTool) grepSingleFile(o *grepOptions, searchPath string) (string, error) {
	fm, err := searchFile(searchPath, o.re, o.beforeCtx, o.afterCtx, o.maxResults)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}
	if fm.Count == 0 {
		return noMatchResult(o.args.Pattern), nil
	}
	result := formatText([]fileMatches{fm})
	return toolutil.FormatResult(fmt.Sprintf("found %d matches in %d files", fm.Count, 1), map[string]any{
		"matches": fm.Count,
		"files":   1,
		"output":  result,
	}), nil
}

// grepSession is the mutable state of one directory search.
type grepSession struct {
	o              *grepOptions
	searchPath     string
	ignorePatterns []string

	fileCh      chan string
	resultMu    sync.Mutex
	allResults  []fileMatches
	globalCount int
	truncated   bool
}

func newGrepSession(o *grepOptions, searchPath string, workers int) *grepSession {
	s := &grepSession{
		o:          o,
		searchPath: searchPath,
		fileCh:     make(chan string, workers*4),
	}
	if !o.args.NoIgnore {
		s.ignorePatterns = loadGitignore(searchPath)
	}
	return s
}

// walkFilter is the filepath.WalkDir callback: it applies hidden-dir,
// gitignore, glob, type, and depth filters, then enqueues the file.
func (s *grepSession) walkFilter(ctx context.Context, path string, d os.DirEntry, err error) error {
	args := s.o.args
	if err != nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d.IsDir() {
		name := d.Name()
		if !args.Hidden && strings.HasPrefix(name, ".") && name != "." && name != ".." {
			return filepath.SkipDir
		}
		if toolutil.IsSkippedDir(name) {
			return filepath.SkipDir
		}
		if !args.NoIgnore && isIgnoredByGitignore(path, name, s.ignorePatterns, s.searchPath) {
			return filepath.SkipDir
		}
		return nil
	}

	if !args.Hidden && strings.HasPrefix(d.Name(), ".") {
		return nil
	}
	if !toolutil.IsTextFile(path) {
		return nil
	}
	if s.o.includeGlob != nil && !s.o.includeGlob.match(path) {
		return nil
	}
	if s.o.excludeGlob != nil && s.o.excludeGlob.match(path) {
		return nil
	}
	if s.o.typeFilter != nil && !s.o.typeFilter(path) {
		return nil
	}

	relPath, _ := filepath.Rel(s.searchPath, path)
	depth := 0
	if relPath != "" {
		depth = len(strings.Split(relPath, string(filepath.Separator)))
	}
	if args.MaxDepth > 0 && depth > args.MaxDepth {
		return nil
	}

	s.resultMu.Lock()
	if s.o.maxResults > 0 && s.globalCount >= s.o.maxResults {
		s.resultMu.Unlock()
		return nil
	}
	s.resultMu.Unlock()

	select {
	case s.fileCh <- path:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// runWorker searches enqueued files and merges results under the cap.
func (s *grepSession) runWorker(ctx context.Context, cancel context.CancelFunc) {
	for path := range s.fileCh {
		fm, err := searchFile(path, s.o.re, s.o.beforeCtx, s.o.afterCtx, s.o.maxResults)
		if err != nil || fm.Count == 0 {
			continue
		}

		s.resultMu.Lock()
		if s.o.maxResults > 0 && s.globalCount >= s.o.maxResults {
			s.resultMu.Unlock()
			continue
		}
		s.globalCount += fm.Count
		if s.o.maxResults > 0 && s.globalCount > s.o.maxResults {
			fm.Count -= (s.globalCount - s.o.maxResults)
			if len(fm.Matches) > fm.Count {
				fm.Matches = fm.Matches[:fm.Count]
			}
			s.globalCount = s.o.maxResults
			s.truncated = true
		}
		s.allResults = append(s.allResults, fm)
		s.resultMu.Unlock()

		if s.o.maxResults > 0 && s.globalCount >= s.o.maxResults {
			cancel()
			return
		}
	}
}

// format renders the collected matches (sorted by path, truncation noted).
func (s *grepSession) format() string {
	if len(s.allResults) == 0 {
		return noMatchResult(s.o.args.Pattern)
	}
	sort.Slice(s.allResults, func(i, j int) bool {
		return s.allResults[i].Path < s.allResults[j].Path
	})
	result := formatText(s.allResults)
	if s.truncated {
		result += fmt.Sprintf("\n... truncated at %d matches (use max_results to increase limit)\n", s.o.maxResults)
	}
	return toolutil.FormatResult(fmt.Sprintf("found %d matches in %d files", s.globalCount, len(s.allResults)), map[string]any{
		"matches": s.globalCount,
		"files":   len(s.allResults),
		"output":  result,
	})
}

// grepDirectory walks a directory tree with worker-goroutine fan-out.
func (t *grepTool) grepDirectory(ctx context.Context, o *grepOptions, searchPath string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	maxWorkers := toolutil.IOWorkers()
	s := newGrepSession(o, searchPath, maxWorkers)

	var walkWg sync.WaitGroup
	walkWg.Add(1)
	go func() {
		defer walkWg.Done()
		defer close(s.fileCh)
		filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
			return s.walkFilter(ctx, path, d, err)
		})
	}()

	var workerWg sync.WaitGroup
	for i := 0; i < maxWorkers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			s.runWorker(ctx, cancel)
		}()
	}

	walkWg.Wait()
	workerWg.Wait()
	return s.format(), nil
}
