package p2p

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/gammazero/deque"
	"github.com/libp2p/go-libp2p/core/peer"
)

// peerStat represents a peer's average statistics.
type peerStat struct {
	sync.RWMutex
	peerID peer.ID
	// score is the average speed per single request
	peerScore float32
	// pruneDeadline specifies when disconnected peer will be removed if
	// it does not return online.
	pruneDeadline time.Time
}

// updateStats recalculates peer.score by averaging the last score
// updateStats takes the total amount of bytes that were requested from the peer
// and the total request duration(in milliseconds). The final score is calculated
// by dividing the amount by time, so the result score will represent how many bytes
// were retrieved in 1 millisecond. This value will then be averaged relative to the
// previous peerScore.
func (p *peerStat) updateStats(amount uint64, time uint64) {
	p.Lock()
	defer p.Unlock()
	averageSpeed := float32(amount)
	if time != 0 {
		averageSpeed /= float32(time)
	}
	if p.peerScore == 0.0 {
		p.peerScore = averageSpeed
		return
	}
	p.peerScore = (p.peerScore + averageSpeed) / 2
}

// decreaseScore decreases peerScore by 20% of the peer that failed the request by any reason.
// NOTE: decreasing peerScore in one session will not affect its position in queue in another
// session(as we can have multiple sessions running concurrently).
// TODO(vgonkivs): to figure out the better scoring increments/decrements
func (p *peerStat) decreaseScore() {
	p.Lock()
	defer p.Unlock()

	p.peerScore -= p.peerScore / 100 * 20
}

// score reads a peer's latest score from the queue
func (p *peerStat) score() float32 {
	p.RLock()
	defer p.RUnlock()
	return p.peerScore
}

// peerStats implements heap.Interface, so we can be sure that we are getting the peer
// with the highest score, each time we call Pop.
type peerStats []*peerStat

func newPeerStats() peerStats {
	ps := make(peerStats, 0)
	heap.Init(&ps)
	return ps
}

func (ps peerStats) Len() int { return len(ps) }

// Less compares two peerScores.
// Less is used by heap.Interface to build the queue in a decreasing order.
func (ps peerStats) Less(i, j int) bool {
	return ps[i].score() > ps[j].score()
}

func (ps peerStats) Swap(i, j int) {
	ps[i], ps[j] = ps[j], ps[i]
}

// Push adds peerStat to the queue.
func (ps *peerStats) Push(x any) {
	item := x.(*peerStat)
	*ps = append(*ps, item)
}

// Pop returns the peer with the highest score from the queue.
func (ps *peerStats) Pop() any {
	old := *ps
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*ps = old[:n-1]
	return item
}

// peerQueue wraps peerStats and guards it with the mutex.
type peerQueue struct {
	ctx context.Context

	peerQueueLk sync.RWMutex

	stats peerStats
	// capacity specifies how many active peers do we have in the queue.
	// It will be decreased if peer will return errors.
	capacity int

	// deque contains a set of trusted peers that are needed for the fallback solution.
	deque *deque.Deque[peer.ID]

	havePeer chan struct{}
}

func newPeerQueue(ctx context.Context, stats []*peerStat, trustedPeers []peer.ID) *peerQueue {
	statsCh := make(chan struct{}, len(stats))
	pq := &peerQueue{
		ctx:      ctx,
		stats:    newPeerStats(),
		deque:    deque.New[peer.ID](len(trustedPeers)),
		havePeer: statsCh,
	}
	for _, stat := range stats {
		pq.push(stat)
	}

	for _, pID := range trustedPeers {
		pq.deque.PushBack(pID)
	}
	return pq
}

// waitPop pops the peer with the biggest score.
// in case if there are no peer available in current session, it blocks until
// the peer will be pushed in.
func (p *peerQueue) waitPop(ctx context.Context) *peerStat {
	if p.empty() {
		stat := &peerStat{peerID: p.deque.PopFront()}
		p.deque.PushBack(stat.peerID)
		return stat
	}

	select {
	case <-ctx.Done():
		return &peerStat{}
	case <-p.ctx.Done():
		return &peerStat{}
	case <-p.havePeer:
	}
	p.peerQueueLk.Lock()
	defer p.peerQueueLk.Unlock()
	return heap.Pop(&p.stats).(*peerStat)
}

// push adds the peer to the queue.
func (p *peerQueue) push(stat *peerStat) {
	if p.empty() {
		return
	}

	p.peerQueueLk.Lock()
	heap.Push(&p.stats, stat)
	p.peerQueueLk.Unlock()
	// notify that the peer is available in the queue, so it can be popped out
	p.havePeer <- struct{}{}
}

func (p *peerQueue) decreaseCapacity() {
	p.peerQueueLk.Lock()
	defer p.peerQueueLk.Unlock()
	if p.capacity == 0 {
		return
	}
	p.capacity--
}

func (p *peerQueue) empty() bool {
	p.peerQueueLk.Lock()
	defer p.peerQueueLk.Unlock()
	return p.capacity == 0
}
