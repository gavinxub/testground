package test

import (
	"context"
	"fmt"

	config "github.com/ipfs/go-ipfs-config"
	coreopts "github.com/ipfs/interface-go-ipfs-core/options"
	utils "github.com/ipfs/testground/plans/chew-datasets/utils"
	"github.com/ipfs/testground/sdk/iptb"
	"github.com/ipfs/testground/sdk/runtime"
)

// IpfsFileStore IPFS File Store Test
type IpfsFileStore struct{}

func (t *IpfsFileStore) AcceptFiles() bool {
	return true
}

func (t *IpfsFileStore) AcceptDirs() bool {
	return true
}

func (t *IpfsFileStore) AddRepoOptions() iptb.AddRepoOptions {
	return func(cfg *config.Config) error {
		cfg.Experimental.FilestoreEnabled = true
		return nil
	}
}

func (t *IpfsFileStore) Execute(ctx context.Context, runenv *runtime.RunEnv, cfg *utils.TestCaseOptions) error {
	if cfg.IpfsInstance != nil {
		runenv.RecordMessage("Running against the Core API")

		err := cfg.ForEachPath(runenv, func(path string, size int64, isDir bool) (string, error) {
			unixfsFile, err := utils.ConvertToUnixfs(path, isDir)
			if err != nil {
				return "", err
			}

			addOptions := coreopts.Unixfs.Nocopy(true)
			cidFile, err := cfg.IpfsInstance.Unixfs().Add(ctx, unixfsFile, addOptions)
			if err != nil {
				return "", err
			}

			return cidFile.String(), nil
		})

		if err != nil {
			return err
		}

		// TODO: Act II and Act III
		runenv.RecordMessage("Test incomplete")
	}

	if cfg.IpfsDaemon != nil {
		runenv.RecordMessage("Running against the Daemon (IPTB)")
		runenv.RecordMessage("Not implemented yet")
	}

	return fmt.Errorf("not implemented")
}
