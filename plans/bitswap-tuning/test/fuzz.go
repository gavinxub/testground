package test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/testground/sdk/runtime"
	"github.com/ipfs/testground/sdk/sync"
	"golang.org/x/sync/errgroup"

	"github.com/ipfs/testground/plans/bitswap-tuning/utils"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// NOTE: To run use:
// ./testground run data-exchange/fuzz --builder=docker:go --runner="local:docker" --dep="github.com/ipfs/go-bitswap=master"

// Fuzz test Bitswap
func Fuzz(runenv *runtime.RunEnv) error {
	// Test Parameters
	timeout := time.Duration(runenv.IntParam("timeout_secs")) * time.Second
	randomDisconnectsFq := float32(runenv.IntParam("random_disconnects_fq")) / 100

	/// --- Set up
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	watcher, writer := sync.MustWatcherWriter(runenv)

	/// --- Tear down
	defer func() {
		err := utils.SignalAndWaitForAll(ctx, runenv.TestInstanceCount, "end", watcher, writer)
		if err != nil {
			runenv.RecordFailure(err)
		} else {
			runenv.RecordSuccess()
		}
		watcher.Close()
		writer.Close()
	}()

	// Create libp2p node
	h, err := libp2p.New(ctx)
	if err != nil {
		return err
	}
	defer h.Close()

	// Get sequence number of this host
	seq, err := writer.Write(sync.PeerSubtree, host.InfoFromHost(h))
	if err != nil {
		return err
	}

	// Get addresses of all peers
	peerCh := make(chan *peer.AddrInfo)
	cancelSub, err := watcher.Subscribe(sync.PeerSubtree, peerCh)
	addrInfos, err := utils.AddrInfosFromChan(peerCh, runenv.TestInstanceCount, timeout)
	if err != nil {
		cancelSub()
		return err
	}
	cancelSub()

	/// --- Warm up

	runenv.Message("I am %s with addrs: %v", h.ID(), h.Addrs())

	// Set up network (with traffic shaping)
	err = setupFuzzNetwork(ctx, runenv, watcher, writer)
	if err != nil {
		return fmt.Errorf("Failed to set up network: %w", err)
	}

	// Signal that this node is in the given state, and wait for all peers to
	// send the same signal
	signalAndWaitForAll := func(state string) {
		utils.SignalAndWaitForAll(ctx, runenv.TestInstanceCount, state, watcher, writer)
	}

	// Wait for all nodes to be ready to start
	signalAndWaitForAll("start")

	runenv.Message("Starting")
	var bsnode *utils.Node
	rootCidSubtree := &sync.Subtree{
		GroupKey:    "root-cid",
		PayloadType: reflect.TypeOf(&cid.Cid{}),
		KeyFunc: func(val interface{}) string {
			return val.(*cid.Cid).String()
		},
	}

	// Create a new blockstore
	bstoreDelay := 5 * time.Millisecond
	bstore, err := utils.CreateBlockstore(ctx, bstoreDelay)
	if err != nil {
		return err
	}

	// Create a new bitswap node from the blockstore
	bsnode, err = utils.CreateBitswapNode(ctx, h, bstore)
	if err != nil {
		return err
	}

	// Listen for seed generation
	rootCidCh := make(chan *cid.Cid, 1)
	cancelRootCidSub, err := watcher.Subscribe(rootCidSubtree, rootCidCh)
	if err != nil {
		return fmt.Errorf("Failed to subscribe to rootCidSubtree %w", err)
	}

	seedGenerated := sync.State("seed-generated")
	var start time.Time
	// Each peer generates the seed data in series, to avoid
	// overloading a single machine hosting multiple instances
	seedIndex := seq - 1
	if seedIndex > 0 {
		// Wait for the seeds with an index lower than this one
		// to generate their seed data
		doneCh := watcher.Barrier(ctx, seedGenerated, int64(seedIndex))
		if err = <-doneCh; err != nil {
			return err
		}
	}

	// Generate a file of random size and add it to the datastore
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	fileSize := 2*1024*1024 + rnd.Intn(64*1024*1024)
	runenv.Message("Generating seed data of %d bytes", fileSize)
	start = time.Now()

	rootCid, err := setupSeed(ctx, bsnode, fileSize)
	if err != nil {
		return fmt.Errorf("Failed to set up seed: %w", err)
	}

	runenv.Message("Done generating seed data of %d bytes (%s)", fileSize, time.Since(start))

	// Signal we've completed generating the seed data
	_, err = writer.SignalEntry(seedGenerated)
	if err != nil {
		return fmt.Errorf("Failed to signal seed generated: %w", err)
	}

	// Inform other nodes of the root CID
	if _, err = writer.Write(rootCidSubtree, &rootCid); err != nil {
		return fmt.Errorf("Failed to get Redis Sync rootCidSubtree %w", err)
	}

	// Get seed cid from all nodes
	var rootCids []cid.Cid
	for i := 0; i < runenv.TestInstanceCount; i++ {
		select {
		case rootCidPtr := <-rootCidCh:
			rootCids = append(rootCids, *rootCidPtr)
		case <-time.After(timeout):
			cancelRootCidSub()
			return fmt.Errorf("could not get all cids in %d seconds", timeout/time.Second)
		}
	}
	cancelRootCidSub()

	// Wait for all nodes to be ready to dial
	signalAndWaitForAll("ready-to-connect")

	// Dial all peers
	dialed, err := utils.DialOtherPeers(ctx, h, addrInfos)
	if err != nil {
		return err
	}
	runenv.Message("Dialed %d other nodes", len(dialed))

	// Wait for all nodes to be connected
	signalAndWaitForAll("connect-complete")

	/// --- Start test
	runenv.Message("Start fetching")

	// Randomly disconnect and reconnect
	var cancelFetchingCtx func()
	if randomDisconnectsFq > 0 {
		var fetchingCtx context.Context
		fetchingCtx, cancelFetchingCtx = context.WithCancel(ctx)
		go func() {
			for {
				time.Sleep(time.Duration(rnd.Intn(1000)) * time.Millisecond)

				select {
				case <-fetchingCtx.Done():
					return
				default:
					// One third of the time, disconnect from a peer then reconnect
					if rnd.Float32() < randomDisconnectsFq {
						conns := h.Network().Conns()
						conn := conns[rnd.Intn(len(conns))]
						runenv.Message("    closing connection to %s", conn.RemotePeer())
						err := conn.Close()
						if err != nil {
							runenv.Message("    error disconnecting: %w", err)
						} else {
							ai := peer.AddrInfo{
								ID:    conn.RemotePeer(),
								Addrs: []ma.Multiaddr{conn.RemoteMultiaddr()},
							}
							go func() {
								// time.Sleep(time.Duration(rnd.Intn(200)) * time.Millisecond)
								runenv.Message("    reconnecting to %s", conn.RemotePeer())
								if err := h.Connect(fetchingCtx, ai); err != nil {
									runenv.Message("    error while reconnecting to peer %v: %w", ai, err)
								}
								runenv.Message("    reconnected to %s", conn.RemotePeer())
							}()
						}
					}
				}
			}
		}()
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, rootCid := range rootCids {
		// Fetch two thirds of the root cids of other nodes
		if rnd.Float32() < 0.3 {
			continue
		}

		rootCid := rootCid
		g.Go(func() error {
			// Stagger the start of the fetch
			startDelay := time.Duration(rnd.Intn(50*runenv.TestInstanceCount)) * time.Millisecond
			time.Sleep(startDelay)

			cidStr := rootCid.String()
			pretty := cidStr[len(cidStr)-6:]

			// Half the time do a regular fetch, half the time cancel and then
			// restart the fetch
			runenv.Message("  FTCH %s after %s delay", pretty, startDelay)
			start = time.Now()
			cctx, cancel := context.WithCancel(gctx)
			if rnd.Float32() < 0.5 {
				// Cancel after a delay
				go func() {
					cancelDelay := time.Duration(rnd.Intn(100)) * time.Millisecond
					time.Sleep(cancelDelay)
					runenv.Message("  cancel %s after %s delay", pretty, startDelay)
					cancel()
				}()
				err = bsnode.FetchGraph(cctx, rootCid)
				if err != nil {
					// If there was an error (probably because the fetch was
					// cancelled) try fetching again
					runenv.Message("  got err fetching %s: %s", pretty, err)
					err = bsnode.FetchGraph(gctx, rootCid)
				}
			} else {
				defer cancel()
				err = bsnode.FetchGraph(cctx, rootCid)
			}
			timeToFetch := time.Since(start)
			runenv.Message("  RCVD %s in %s", pretty, timeToFetch)

			return err
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("Error fetching data through Bitswap: %w", err)
	}
	runenv.Message("Fetching complete")
	if randomDisconnectsFq > 0 {
		cancelFetchingCtx()
	}

	// Wait for all leeches to have downloaded the data from seeds
	signalAndWaitForAll("transfer-complete")

	// Shut down bitswap
	err = bsnode.Close()
	if err != nil {
		return fmt.Errorf("Error closing Bitswap: %w", err)
	}

	// Disconnect peers
	for _, c := range h.Network().Conns() {
		err := c.Close()
		if err != nil {
			return fmt.Errorf("Error disconnecting: %w", err)
		}
	}

	/// --- Ending the test

	return nil
}

// Set up traffic shaping with random latency and bandwidth
func setupFuzzNetwork(ctx context.Context, runenv *runtime.RunEnv, watcher *sync.Watcher, writer *sync.Writer) error {
	if !runenv.TestSidecar {
		return nil
	}

	// Wait for the network to be initialized.
	if err := sync.WaitNetworkInitialized(ctx, runenv, watcher); err != nil {
		return err
	}

	// TODO: just put the unique testplan id inside the runenv?
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	latency := time.Duration(2+rnd.Intn(100)) * time.Millisecond
	bandwidth := 1 + rnd.Intn(100)
	writer.Write(sync.NetworkSubtree(hostname), &sync.NetworkConfig{
		Network: "default",
		Enable:  true,
		Default: sync.LinkShape{
			Latency:   latency,
			Bandwidth: uint64(bandwidth * 1024 * 1024),
		},
		State: "network-configured",
	})

	runenv.Message("I have %s latency and %dMB bandwidth", latency, bandwidth)

	err = <-watcher.Barrier(ctx, "network-configured", int64(runenv.TestInstanceCount))
	if err != nil {
		return fmt.Errorf("failed to configure network: %w", err)
	}
	return nil
}