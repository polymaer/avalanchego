// (c) 2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package throttling

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils/linkedhashmap"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	_ InboundMsgThrottler = &inboundMsgThrottler{}
)

// InboundMsgThrottler rate-limits inbound messages from the network.
type InboundMsgThrottler interface {
	// Blocks until we can read a message of size [msgSize] from [nodeID].
	// For every call to Acquire([msgSize], [nodeID]), we must (!) call
	// Release([msgSize], [nodeID]) when done processing the message
	// (or when we give up trying to read the message.)
	Acquire(msgSize uint64, nodeID ids.ShortID)

	// Mark that we're done processing a message of size [msgSize]
	// from [nodeID].
	Release(msgSize uint64, nodeID ids.ShortID)
}

type InboundMsgThrottlerConfig struct {
	MsgByteThrottlerConfig
	MaxProcessingMsgsPerNode uint64 `json:"maxProcessingMsgsPerNode"`
}

// Returns a new, sybil-safe inbound message throttler.
func NewInboundMsgThrottler(
	log logging.Logger,
	namespace string,
	registerer prometheus.Registerer,
	vdrs validators.Set,
	config InboundMsgThrottlerConfig,
) (InboundMsgThrottler, error) {
	t := &inboundMsgThrottler{
		byteThrottler: inboundMsgByteThrottler{
			commonMsgThrottler: commonMsgThrottler{
				log:                    log,
				vdrs:                   vdrs,
				maxVdrBytes:            config.VdrAllocSize,
				remainingVdrBytes:      config.VdrAllocSize,
				remainingAtLargeBytes:  config.AtLargeAllocSize,
				nodeMaxAtLargeBytes:    config.NodeMaxAtLargeBytes,
				nodeToVdrBytesUsed:     make(map[ids.ShortID]uint64),
				nodeToAtLargeBytesUsed: make(map[ids.ShortID]uint64),
			},
			waitingToAcquire:    linkedhashmap.New(),
			nodeToWaitingMsgIDs: make(map[ids.ShortID][]uint64),
		},
		bufferThrottler: inboundMsgBufferThrottler{
			maxProcessingMsgsPerNode: config.MaxProcessingMsgsPerNode,
			nodeToNumProcessingMsgs:  make(map[ids.ShortID]uint64),
			awaitingAcquire:          make(map[ids.ShortID][]chan struct{}),
		},
	}
	return t, t.byteThrottler.metrics.initialize(namespace, registerer)
}

// A sybil-safe inbound message throttler.
// Rate-limits reading of inbound messages to prevent peers from
// consuming excess resources.
// The two resources considered are:
// 1. An inbound message buffer, where each message that we're currently
//    processing takes up 1 unit of space on the buffer.
// 2. An inbound message byte buffer, where a message of length n
//    that we're currently processing takes up n units of space on the buffer.
// A call to Acquire([msgSize], [nodeID]) blocks until we've secured
// enough of both these resources to read a message of size [msgSize] from [nodeID].
type inboundMsgThrottler struct {
	// Rate-limits based on number of messages from a given
	// node that we're currently processing.
	bufferThrottler inboundMsgBufferThrottler
	// Rate-limits based on size of all messages from a given
	// node that we're currently processing.
	byteThrottler inboundMsgByteThrottler
}

// Returns when we can read a message of size [msgSize] from node [nodeID].
// Release([msgSize], [nodeID]) must be called (!) when done with the message
// or when we give up trying to read the message, if applicable.
func (t *inboundMsgThrottler) Acquire(msgSize uint64, nodeID ids.ShortID) {
	// Acquire space on the inbound message buffer
	t.bufferThrottler.Acquire(nodeID)
	// Acquire space on the inbound message byte buffer
	t.byteThrottler.Acquire(msgSize, nodeID)
}

// Must correspond to a previous call of Acquire([msgSize], [nodeID]).
// See InboundMsgThrottler interface.
func (t *inboundMsgThrottler) Release(msgSize uint64, nodeID ids.ShortID) {
	// Release space on the inbound message buffer
	t.bufferThrottler.Release(nodeID)
	// Release space on the inbound message byte buffer
	t.byteThrottler.Release(msgSize, nodeID)
}
