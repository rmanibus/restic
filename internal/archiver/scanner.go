package archiver

import (
	"context"
	"sort"

	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/frontend"
	"github.com/restic/restic/internal/restic"
)

// Scanner  traverses the targets and calls the function Result with cumulated
// stats concerning the files and folders found. Select is used to decide which
// items should be included. Error is called when an error occurs.
type Scanner struct {
	Frontend     frontend.Frontend
	SelectByName SelectByNameFunc
	Select       SelectFunc
	Error        ErrorFunc
	Result       func(item string, s ScanStats)
}

// NewScanner initializes a new Scanner.
func NewScanner(Frontend frontend.Frontend) *Scanner {
	return &Scanner{
		Frontend:     Frontend,
		SelectByName: func(_ string) bool { return true },
		Select:       func(_ restic.FileMetadata) bool { return true },
		Error:        func(_ string, err error) error { return err },
		Result:       func(_ string, s ScanStats) {},
	}
}

// ScanStats collect statistics.
type ScanStats struct {
	Files, Dirs, Others uint
	Bytes               uint64
}

func (s *Scanner) scanTree(ctx context.Context, stats ScanStats, tree Tree) (ScanStats, error) {
	// traverse the path in the file system for all leaf nodes
	if tree.Leaf() {
		abstarget, err := tree.FileMetadata.Abs()
		if err != nil {
			return ScanStats{}, err
		}

		stats, err = s.scan(ctx, stats, abstarget)
		if err != nil {
			return ScanStats{}, err
		}

		return stats, nil
	}

	// otherwise recurse into the nodes in a deterministic order
	for _, name := range tree.NodeNames() {
		var err error
		stats, err = s.scanTree(ctx, stats, tree.Nodes[name])
		if err != nil {
			return ScanStats{}, err
		}

		if ctx.Err() != nil {
			return stats, nil
		}
	}

	return stats, nil
}

// Scan traverses the targets. The function Result is called for each new item
// found, the complete result is also returned by Scan.
func (s *Scanner) Scan(ctx context.Context, targets []restic.LazyFileMetadata) error {
	debug.Log("start scan for %v", targets)

	// we're using the same tree representation as the archiver does
	cleanTargets, err := resolveRelativeTargets(targets)
	if err != nil {
		return err
	}
	debug.Log("clean targets %v", cleanTargets)
	tree, err := newTree(cleanTargets)
	if err != nil {
		return err
	}

	stats, err := s.scanTree(ctx, ScanStats{}, *tree)
	if err != nil {
		return err
	}

	s.Result("", stats)
	debug.Log("result: %+v", stats)
	return nil
}

func (s *Scanner) scan(ctx context.Context, stats ScanStats, target restic.LazyFileMetadata) (ScanStats, error) {
	if ctx.Err() != nil {
		return stats, nil
	}

	// exclude files by path before running stat to reduce number of lstat calls
	if !s.SelectByName(target.Name()) {
		return stats, nil
	}

	// get file information
	err := target.Init()
	if err != nil {
		return stats, s.Error(target.Path(), err)
	}

	// run remaining select functions that require file information
	if !s.Select(target) {
		return stats, nil
	}

	switch {
	case target.Mode().IsRegular():
		stats.Files++
		stats.Bytes += uint64(target.Size())
	case target.Mode().IsDir():
		children, err := target.Children()
		if err != nil {
			return stats, s.Error(target.Path(), err)
		}

		sort.Slice(children, func(a, b int) bool {
			return children[a].Name() < children[b].Name()
		})

		for _, child := range children {
			stats, err = s.scan(ctx, stats, child)
			if err != nil {
				return stats, err
			}
		}
		stats.Dirs++
	default:
		stats.Others++
	}

	s.Result(target.Path(), stats)
	return stats, nil
}
