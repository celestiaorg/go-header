package p2p

import (
	"container/heap"
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func Test_PeerStatsPush(t *testing.T) {
	pQueue := newPeerStats()
	pQueue.Push(&peerStat{peerID: "peerID"})
	require.True(t, pQueue.Len() == 1)
}

func Test_PeerStatsPop(t *testing.T) {
	pQueue := newPeerStats()
	pQueue.Push(&peerStat{peerID: "peerID"})
	stats := heap.Pop(&pQueue).(*peerStat)
	require.Equal(t, stats.peerID, peer.ID("peerID"))
}

func Test_PeerQueuePopBestPeer(t *testing.T) {
	peersStat := peerStats{
		{peerID: "peerID1", peerScore: 1},
		{peerID: "peerID2", peerScore: 2},
		{peerID: "peerID3", peerScore: 4},
		{peerID: "peerID4"}, // score = 0
	}
	wantStat := peerStats{
		{peerID: "peerID3", peerScore: 4},
		{peerID: "peerID2", peerScore: 2},
		{peerID: "peerID1", peerScore: 1},
		{peerID: "peerID4"}, // score = 0
	}

	// we do not need timeout/cancel functionality here
	pQueue := newPeerQueue(context.Background(), peersStat)
	for index := 0; index < pQueue.stats.Len(); index++ {
		stats := heap.Pop(&pQueue.stats).(*peerStat)
		require.Equal(t, stats, wantStat[index])
	}
}

func Test_PeerQueueRemovePeer(t *testing.T) {
	peersStat := []*peerStat{
		{peerID: "peerID1", peerScore: 1},
		{peerID: "peerID2", peerScore: 2},
		{peerID: "peerID3", peerScore: 4},
		{peerID: "peerID4"}, // score = 0
	}

	// we do not need timeout/cancel functionality here
	pQueue := newPeerQueue(context.Background(), peersStat)

	_ = heap.Pop(&pQueue.stats)
	stat := heap.Pop(&pQueue.stats).(*peerStat)
	require.Equal(t, stat.peerID, peer.ID("peerID2"))
}

func Test_StatsUpdateStats(t *testing.T) {
	// we do not need timeout/cancel functionality here
	pQueue := newPeerQueue(context.Background(), []*peerStat{})
	stat := &peerStat{peerID: "peerID", peerScore: 0}
	heap.Push(&pQueue.stats, stat)
	testCases := []struct {
		inputTime   time.Duration
		inputBytes  int
		resultScore float32
	}{
		// common case, where time and bytes is not equal to 0
		{
			inputTime:   time.Millisecond * 16,
			inputBytes:  4,
			resultScore: 4,
		},
		// in case if bytes is equal to 0,
		// then the request was failed and previous score will be
		// decreased
		{
			inputTime:   time.Millisecond * 10,
			inputBytes:  0,
			resultScore: 2,
		},
		// testing case with time=0, to ensure that dividing by 0 is handled properly
		{
			inputTime:   0,
			inputBytes:  0,
			resultScore: 1,
		},
	}

	for _, tt := range testCases {
		stat.updateStats(tt.inputBytes, tt.inputTime)
		updatedStat := heap.Pop(&pQueue.stats).(*peerStat)
		require.Equal(t, updatedStat.score(), stat.score())
		heap.Push(&pQueue.stats, updatedStat)
	}
}

func Test_StatDecreaseScore(t *testing.T) {
	pStats := &peerStat{
		peerID:    peer.ID("test"),
		peerScore: 100,
	}
	// will decrease score by 20%
	pStats.decreaseScore()
	require.Equal(t, pStats.score(), float32(80.0))
}

// BenchmarkPeerQueue/push_for_10-14         	 2373187	       489.3 ns/op	     448 B/op	       8 allocs/op
// BenchmarkPeerQueue/pop_for_10-14          	 1561532	       761.8 ns/op	     800 B/op	      10 allocs/op
// BenchmarkPeerQueue/push_for_100-14        	  253057	      4590 ns/op	    2368 B/op	      11 allocs/op
// BenchmarkPeerQueue/pop_for_100-14         	  151729	      7503 ns/op	    8000 B/op	     100 allocs/op
// BenchmarkPeerQueue/push_for_1000-14       	   25915	     46220 ns/op	   17728 B/op	      14 allocs/op
// BenchmarkPeerQueue/pop_for_1000-14        	   15207	     75764 ns/op	   80000 B/op	    1000 allocs/op
// BenchmarkPeerQueue/push_for_1000000-14    	      15	  69555594 ns/op	44948820 B/op	      41 allocs/op
// BenchmarkPeerQueue/pop_for_1000000-14     	      15	  75170956 ns/op	80000032 B/op	 1000000 allocs/op
func BenchmarkPeerQueue(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	peers := [][]*peerStat{
		generatePeerStats(b, 10),
		generatePeerStats(b, 100),
		generatePeerStats(b, 1000),
		generatePeerStats(b, 1000000),
	}

	for _, peerStats := range peers {
		var queue *peerQueue
		b.Run(fmt.Sprintf("push for %d", len(peerStats)), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				queue = newPeerQueue(ctx, peerStats)
			}
		})

		b.Run(fmt.Sprintf("pop for %d", len(peerStats)), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for range peerStats {
					_ = queue.waitPop(ctx)
				}
			}
		})
	}
}

func generatePeerStats(b *testing.B, number int) []*peerStat {
	stats := make([]*peerStat, number)
	for i := range stats {
		priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
		require.NoError(b, err)
		id, err := peer.IDFromPrivateKey(priv)
		require.NoError(b, err)
		stats[i] = &peerStat{
			peerID:    id,
			peerScore: rand.Float32(),
		}
	}
	return stats
}
