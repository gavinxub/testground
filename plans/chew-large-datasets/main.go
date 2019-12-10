package main

import (
	"context"
	"fmt"

	"github.com/ipfs/testground/plans/chew-large-datasets/test"
	"github.com/ipfs/testground/plans/chew-large-datasets/utils"
	"github.com/ipfs/testground/sdk/iptb"
	"github.com/ipfs/testground/sdk/runtime"
)

var testCases = []utils.TestCase{
	&test.IpfsAddDefaults{},
	&test.IpfsAddTrickleDag{},
	&test.IpfsAddDirSharding{},
	&test.IpfsMfs{},
	&test.IpfsMfsDirSharding{},
	&test.IpfsUrlStore{},
	&test.IpfsFileStore{},
}

func main() {
	runenv := runtime.CurrentRunEnv()
	if runenv.TestCaseSeq < 0 {
		panic("test case sequence number not set")
	}

	tc := testCases[runenv.TestCaseSeq]

	cfg, err := utils.GetTestConfig(runenv, tc.AcceptFiles(), tc.AcceptDirs())
	defer cfg.Cleanup()
	if err != nil {
		runenv.Abort(fmt.Errorf("could not retrieve test config: %s", err))
		return
	}

	ctx := context.Background()

	opts := &utils.TestCaseOptions{
		IpfsInstance: nil,
		IpfsDaemon:   nil,
		TestConfig:   cfg,
	}

	mode, modeSet := runenv.StringParam("mode")

	testCoreAPI := true
	testDaemon := true

	if modeSet {
		switch mode {
		case "daemon":
			testCoreAPI = false
		case "coreapi":
			testDaemon = false
		default:
			panic(fmt.Errorf("invalid mode set: %s", mode))
		}
	}

	if testCoreAPI {
		apiOpts := tc.InstanceOptions()

		if apiOpts == nil {
			fmt.Println("Test not implemented against CoreAPI yet")
		} else {
			ipfs, err := utils.CreateIpfsInstance(ctx, apiOpts)
			if err != nil {
				runenv.Abort(fmt.Errorf("failed to get temp dir: %s", err))
				return
			}

			opts.IpfsInstance = ipfs
		}
	}

	if testDaemon {
		spec := tc.DaemonOptions()

		if spec == nil {
			fmt.Println("Daemon testing not yet implemented")
		} else {
			ensemble := iptb.NewTestEnsemble(ctx, spec)
			ensemble.Initialize()
			defer ensemble.Destroy()

			// In this test suite we agree that the node is tagged as 'node'.
			node := ensemble.GetNode("node")
			client := node.Client()
			opts.IpfsDaemon = client
		}
	}

	tc.Execute(ctx, runenv, opts)
}
