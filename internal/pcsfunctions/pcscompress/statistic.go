package pcscompress

import (
	"sync"
	"sync/atomic"
	"time"
)

type CompressStatistic struct {
	totalSize      int64
	compressedSize int64
	fileCount      int64
	startTime      time.Time
	mu             sync.RWMutex
}

func NewCompressStatistic() *CompressStatistic {
	return &CompressStatistic{
		startTime: time.Now(),
	}
}

func (cs *CompressStatistic) AddTotalSize(size int64) {
	atomic.AddInt64(&cs.totalSize, size)
}

func (cs *CompressStatistic) AddCompressedSize(size int64) {
	atomic.AddInt64(&cs.compressedSize, size)
}

func (cs *CompressStatistic) AddFileCount(count int64) {
	atomic.AddInt64(&cs.fileCount, count)
}

func (cs *CompressStatistic) TotalSize() int64 {
	return atomic.LoadInt64(&cs.totalSize)
}

func (cs *CompressStatistic) CompressedSize() int64 {
	return atomic.LoadInt64(&cs.compressedSize)
}

func (cs *CompressStatistic) FileCount() int64 {
	return atomic.LoadInt64(&cs.fileCount)
}

func (cs *CompressStatistic) Elapsed() time.Duration {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return time.Since(cs.startTime)
}

func (cs *CompressStatistic) StartTimer() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.startTime = time.Now()
}

func (cs *CompressStatistic) Reset() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.totalSize = 0
	cs.compressedSize = 0
	cs.fileCount = 0
	cs.startTime = time.Now()
}
