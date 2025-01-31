// Package find is used to find files that match the provided find pattern
// or CSV file. It also filters out any files that match the exclude pattern (if
// any)
package find

import (
	"encoding/csv"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/exp/slices"

	"github.com/ayoisaiah/f2/internal/config"
	internalpath "github.com/ayoisaiah/f2/internal/path"
)

const (
	dotCharacter = 46
)

// csvRows keeps track of each row in a CSV file so that it can be associated
// with a file renaming change. The key is the absolute path of the source file
// and the value is the correspoding row in the CSV file.
var csvRows = make(map[string][]string)

// readCSVFile reads all the records contained in a CSV file specified by
// `pathToCSV`.
func readCSVFile(pathToCSV string) ([][]string, error) {
	f, err := os.Open(pathToCSV)
	if err != nil {
		return nil, err
	}

	defer f.Close()

	csvReader := csv.NewReader(f)

	records, err := csvReader.ReadAll()
	if err != nil {
		return nil, err
	}

	return records, nil
}

// filterMatches filters out files that do not match the find string or one
// that matches any exclusion patterns.
func filterMatches(
	pathsToFilter internalpath.Collection,
	pathsToSearch []string,
	searchRegex *regexp.Regexp, excludeFilterInput []string,
	includeDir, includeHidden, onlyDir, ignoreExt bool,
) error {
	excludeFilter := strings.Join(excludeFilterInput, "|")

	excludeMatchRegex, err := regexp.Compile(excludeFilter)
	if err != nil {
		return err
	}

	for path, dirEntry := range pathsToFilter {
		filteredDirEntry := dirEntry[:0]

		for _, entry := range dirEntry {
			filename := entry.Name()

			entryIsDir := entry.IsDir()

			if entryIsDir && !includeDir {
				continue
			}

			if onlyDir && !entryIsDir {
				continue
			}

			if !includeHidden {
				entryIsHidden, err := isHidden(filename, path)
				if err != nil {
					return err
				}

				// Ensure file arguments are not affected
				if entryIsHidden {
					entryAbsPath, err := filepath.Abs(
						filepath.Join(path, filename),
					)
					if err != nil {
						return err
					}

					shouldSkip := true

					for _, pathArg := range pathsToSearch {
						argAbsPath, err := filepath.Abs(pathArg)
						if err != nil {
							return err
						}

						if strings.EqualFold(entryAbsPath, argAbsPath) {
							shouldSkip = false
						}
					}

					if shouldSkip {
						continue
					}
				}
			}

			if ignoreExt && !entryIsDir {
				filename = internalpath.FilenameWithoutExtension(filename)
			}

			if excludeFilter != "" && excludeMatchRegex.MatchString(filename) {
				continue
			}

			matched := searchRegex.MatchString(filename)
			if matched {
				filteredDirEntry = append(filteredDirEntry, entry)
			}

			pathsToFilter[path] = filteredDirEntry
		}

		if len(filteredDirEntry) == 0 {
			delete(pathsToFilter, path)
		}
	}

	return nil
}

func removeHidden(
	de []os.DirEntry,
	baseDir string,
) (ret []os.DirEntry, err error) {
	for _, e := range de {
		r, err := isHidden(e.Name(), baseDir)
		if err != nil {
			return nil, err
		}

		if !r {
			ret = append(ret, e)
		}
	}

	return ret, nil
}

func walk(
	paths internalpath.Collection,
	maxDepth int,
	includeHidden bool,
) error {
	var recursedPaths []string

	var currentDepth int

	// currentLevel represents the current level of directories
	// and their contents
	currentLevel := make(map[string][]os.DirEntry)

loop:
	// The goal of each iteration is to created entries for each
	// unaccounted directory in the current level
	for dir, dirContents := range paths {
		if slices.Contains(recursedPaths, dir) {
			continue
		}

		if !includeHidden {
			var err error
			dirContents, err = removeHidden(dirContents, dir)
			if err != nil {
				return err
			}
		}

		for _, entry := range dirContents {
			if entry.IsDir() {
				fp := filepath.Join(dir, entry.Name())
				dirEntry, err := os.ReadDir(fp)
				if err != nil {
					return err
				}

				currentLevel[fp] = dirEntry
			}
		}

		recursedPaths = append(recursedPaths, dir)
	}

	// if there are directories in the current level
	// store each directory entry and empty the
	// currentLevel so that it may be repopulated
	if len(currentLevel) > 0 {
		for dir, dirContents := range currentLevel {
			paths[dir] = dirContents

			delete(currentLevel, dir)
		}

		currentDepth++
		if !(maxDepth > 0 && currentDepth == maxDepth) {
			goto loop
		}
	}

	return nil
}

// searchPaths groups the paths that will be searched and their
// directory contents.
func searchPaths(
	pathsToSearch []string,
	maxDepth int,
	recursive, includeHidden bool,
) (internalpath.Collection, error) {
	paths := make(internalpath.Collection)

	if len(pathsToSearch) == 0 {
		pathsToSearch = append(pathsToSearch, ".")
	}

	for _, path := range pathsToSearch {
		var fileInfo os.FileInfo

		path = filepath.Clean(path)

		// Skip paths that have already been processed
		if _, ok := paths[path]; ok {
			continue
		}

		fileInfo, err := os.Stat(path)
		if err != nil {
			return nil, err
		}

		if fileInfo.IsDir() {
			paths[path], err = os.ReadDir(path)
			if err != nil {
				return nil, err
			}

			continue
		}

		dir := filepath.Dir(path)

		var dirEntry []fs.DirEntry

		dirEntry, err = os.ReadDir(dir)
		if err != nil {
			return nil, err
		}

	entryLoop:
		for _, entry := range dirEntry {
			if entry.Name() == fileInfo.Name() {
				// Ensure that the file is not already
				// present in the directory entry
				for _, e := range paths[dir] {
					if e.Name() == fileInfo.Name() {
						break entryLoop
					}
				}

				paths[dir] = append(paths[dir], entry)

				break
			}
		}
	}

	if recursive {
		err := walk(paths, maxDepth, includeHidden)
		if err != nil {
			return nil, err
		}
	}

	return paths, nil
}

// handleCSV reads the provided CSV file, and finds all the
// valid candidates for replacement.
func handleCSV(
	csvFilename string,
	findSliceOpt, replacementSliceOpt []string,
) (internalpath.Collection, error) {
	paths := make(internalpath.Collection)

	records, err := readCSVFile(csvFilename)
	if err != nil {
		return nil, err
	}

	csvAbsPath, err := filepath.Abs(csvFilename)
	if err != nil {
		return nil, err
	}

	findSlice := make([]string, 0, len(records))

	replacementSlice := make([]string, 0, len(records))

	for _, record := range records {
		if len(record) == 0 {
			continue
		}

		source := strings.TrimSpace(record[0])

		absSourcePath := filepath.Join(filepath.Dir(csvAbsPath), source)

		fileInfo, err2 := os.Stat(absSourcePath)
		if err2 != nil {
			return nil, err2
		}

		findSlice = append(findSlice, fileInfo.Name())

		sourceDir := filepath.Dir(absSourcePath)

		var dirEntry []fs.DirEntry

		dirEntry, err2 = os.ReadDir(sourceDir)
		if err2 != nil {
			return nil, err2
		}

	entryLoop:
		for _, entry := range dirEntry {
			if entry.Name() == fileInfo.Name() {
				// Ensure that the file is not already
				// present in the directory entry
				for _, e := range paths[sourceDir] {
					if e.Name() == fileInfo.Name() {
						break entryLoop
					}
				}

				paths[sourceDir] = append(paths[sourceDir], entry)

				break
			}
		}

		if len(record) > 1 {
			target := strings.TrimSpace(record[1])

			replacementSlice = append(replacementSlice, target)
		}

		csvRows[absSourcePath] = record
	}

	if len(replacementSliceOpt) == 0 {
		if len(findSliceOpt) == 0 {
			config.SetFindSlice(findSlice)
			config.SetReplacementSlice(replacementSlice)

			err = config.SetFindStringRegex(0)
			if err != nil {
				return nil, err
			}
		}
	}

	return paths, nil
}

func Find(conf *config.Config) (internalpath.Collection, error) {
	if conf.CSVFilename != "" {
		return handleCSV(
			conf.CSVFilename,
			conf.FindSlice,
			conf.ReplacementSlice,
		)
	}

	paths, err := searchPaths(
		conf.PathsToFilesOrDirs,
		conf.MaxDepth,
		conf.Recursive,
		conf.IncludeHidden,
	)
	if err != nil {
		return nil, err
	}

	err = filterMatches(
		paths,
		conf.PathsToFilesOrDirs,
		conf.SearchRegex,
		conf.ExcludeFilter,
		conf.IncludeDir,
		conf.IncludeHidden,
		conf.OnlyDir,
		conf.IgnoreExt,
	)
	if err != nil {
		return nil, err
	}

	return paths, nil
}

func GetCSVRows() map[string][]string {
	return csvRows
}
