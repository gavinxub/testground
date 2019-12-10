package utils

import (
	"fmt"
	"os"

	"github.com/dustin/go-humanize"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/testground/sdk/runtime"
)

// TestConfig is a test configuration.
type TestConfig []*TestDir

// TestDir is a test directory or file. If the depth is 0 (zero), then this it
// is a file.
type TestDir struct {
	Path  string
	Depth uint
	Size  int64
}

// GetTestConfig retrieves the configuration from the runtime environment.
func GetTestConfig(runenv *runtime.RunEnv, acceptFiles bool, acceptDirs bool) (cfg TestConfig, err error) {
	cfg = TestConfig{}

	if acceptFiles {
		// Usage: --test-param file-sizes='["10GB"]'
		sizes := runenv.StringArrayParamD("file-sizes", []string{"1MB", "10MB", "100MB" /* "1GB", "10GB" */})

		for _, size := range sizes {
			n, err := humanize.ParseBytes(size)
			if err != nil {
				return cfg, err
			}

			file, err := CreateRandomFile(runenv, os.TempDir(), int64(n))
			if err != nil {
				return nil, err
			}

			fmt.Printf("%s: %s file created\n", file, humanize.Bytes(n))

			cfg = append(cfg, &TestDir{
				Path: file,
				Size: int64(n),
			})
		}
	}

	if acceptDirs {
		// Usage: --test-param dir-cfg='[{"depth": 10, "size": "1MB"}, {"depth": 100, "size": "1MB"}]
		dirConfigs := []rawDirConfig{}
		ok := runenv.JSONParam("dir-cfg", &dirConfigs)
		if !ok {
			dirConfigs = defaultDirConfigs
		}

		for _, dir := range dirConfigs {
			n, err := humanize.ParseBytes(dir.Size)
			if err != nil {
				return nil, err
			}

			path, err := CreateRandomDirectory(runenv, os.TempDir(), dir.Depth)
			if err != nil {
				return nil, err
			}

			_, err = CreateRandomFile(runenv, path, int64(n))
			if err != nil {
				return nil, err
			}

			fmt.Printf("%s: %s directory created\n", humanize.Bytes(n), path)

			cfg = append(cfg, &TestDir{
				Path:  path,
				Depth: dir.Depth,
				Size:  int64(n),
			})
		}
	}

	return cfg, nil
}

type OsTestFunction func(path string, isDir bool) (string, error)

func (tc TestConfig) ForEachPath(runenv *runtime.RunEnv, fn OsTestFunction) error {
	for _, cfg := range tc {
		err := func() error {
			var cid string
			var err error

			if cfg.Depth == 0 {
				cid, err = fn(cfg.Path, false)
			} else {
				cid, err = fn(cfg.Path, true)
			}

			if err != nil {
				return fmt.Errorf("%s: failed to add %s", cfg.Path, err)
			}

			fmt.Printf("%s: %s added\n", cfg.Path, cid)
			return nil
		}()

		if err != nil {
			return err
		}
	}

	return nil
}

func ConvertToUnixfs(path string, isDir bool) (files.Node, error) {
	var unixfsFile files.Node
	var err error

	if isDir {
		unixfsFile, err = GetPathToUnixfsDirectory(path)
	} else {
		unixfsFile, err = GetPathToUnixfsFile(path)
	}

	if err != nil {
		return unixfsFile, fmt.Errorf("failed to get Unixfs file from path: %s", err)
	}

	return unixfsFile, err
}

func (tc TestConfig) Cleanup() {
	for _, dir := range tc {
		os.RemoveAll(dir.Path)
	}
}

type testConfig struct {
	Depth uint
	Size  int64
}

type rawDirConfig struct {
	Depth uint   `json:"depth"`
	Size  string `json:"size"`
}

var defaultDirConfigs = []rawDirConfig{
	rawDirConfig{
		Depth: 10,
		Size:  "1MB",
	},
	rawDirConfig{
		Depth: 50,
		Size:  "1MB",
	},
}
