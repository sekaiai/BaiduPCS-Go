package pcscompress

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
)

type QueueStatus int

const (
	QueueStatusIdle QueueStatus = iota
	QueueStatusRunning
	QueueStatusPaused
	QueueStatusStopped
)

type CompressQueueItem struct {
	Task       *CompressTask
	Result     *CompressResult
	Status     string
	RetryCount int
}

type CompressQueue struct {
	items         []*CompressQueueItem
	currentIndex  int32
	status        int32
	maxConcurrent int32
	mu            sync.RWMutex
	OnTaskStart   func(item *CompressQueueItem)
	OnTaskProgress func(item *CompressQueueItem, processed, total int64, currentFile string)
	OnTaskComplete func(item *CompressQueueItem)
	OnQueueComplete func(results []*CompressQueueItem)
}

func NewCompressQueue(maxConcurrent int) *CompressQueue {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &CompressQueue{
		items:         make([]*CompressQueueItem, 0),
		maxConcurrent: int32(maxConcurrent),
		status:        int32(QueueStatusIdle),
	}
}

func (cq *CompressQueue) AddTask(sourcePath, targetZipPath string, opts *CompressOptions) error {
	cq.mu.Lock()
	defer cq.mu.Unlock()

	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("获取源路径绝对路径失败: %w", err)
	}

	if targetZipPath == "" {
		if len(cq.items) == 1 {
			targetZipPath = GenerateSimpleZipName(sourcePath)
		} else {
			targetZipPath = GenerateUniqueZipName(sourcePath)
		}
	}

	absTarget, err := filepath.Abs(targetZipPath)
	if err != nil {
		return fmt.Errorf("获取目标路径绝对路径失败: %w", err)
	}

	task := NewCompressTask(absSource, absTarget, opts)
	item := &CompressQueueItem{
		Task:   task,
		Status: "pending",
	}

	cq.items = append(cq.items, item)
	return nil
}

func (cq *CompressQueue) AddDirectory(parentPath string, depth int, opts *CompressOptions) error {
	dirs, err := GetSubDirectories(parentPath, depth)
	if err != nil {
		return fmt.Errorf("获取子目录失败: %w", err)
	}

	for _, dir := range dirs {
		var zipName string
		if len(dirs) == 1 {
			zipName = GenerateSimpleZipName(dir)
		} else {
			zipName = GenerateUniqueZipName(dir)
		}
		err := cq.AddTask(dir, zipName, opts)
		if err != nil {
			return fmt.Errorf("添加压缩任务失败: %s, %w", dir, err)
		}
	}

	return nil
}

func (cq *CompressQueue) Count() int {
	cq.mu.RLock()
	defer cq.mu.RUnlock()
	return len(cq.items)
}

func (cq *CompressQueue) GetStatus() QueueStatus {
	return QueueStatus(atomic.LoadInt32(&cq.status))
}

func (cq *CompressQueue) Execute() {
	if !atomic.CompareAndSwapInt32(&cq.status, int32(QueueStatusIdle), int32(QueueStatusRunning)) {
		return
	}

	defer atomic.StoreInt32(&cq.status, int32(QueueStatusIdle))

	for i := range cq.items {
		if cq.GetStatus() == QueueStatusStopped {
			break
		}

		for cq.GetStatus() == QueueStatusPaused {
			time.Sleep(100 * time.Millisecond)
			if cq.GetStatus() == QueueStatusStopped {
				return
			}
		}

		item := cq.items[i]
		item.Status = "running"

		if cq.OnTaskStart != nil {
			cq.OnTaskStart(item)
		}

		item.Task.OnProgress = func(processed, total int64, currentFile string) {
			if cq.OnTaskProgress != nil {
				cq.OnTaskProgress(item, processed, total, currentFile)
			}
		}

		result := item.Task.Execute()
		item.Result = result

		if result.Success {
			item.Status = "completed"
		} else {
			item.Status = "failed"
		}

		if cq.OnTaskComplete != nil {
			cq.OnTaskComplete(item)
		}
	}

	if cq.OnQueueComplete != nil {
		cq.OnQueueComplete(cq.items)
	}
}

func (cq *CompressQueue) Stop() {
	atomic.StoreInt32(&cq.status, int32(QueueStatusStopped))
}

func (cq *CompressQueue) Pause() {
	atomic.StoreInt32(&cq.status, int32(QueueStatusPaused))
}

func (cq *CompressQueue) Resume() {
	atomic.CompareAndSwapInt32(&cq.status, int32(QueueStatusPaused), int32(QueueStatusRunning))
}

func (cq *CompressQueue) GetResults() []*CompressQueueItem {
	cq.mu.RLock()
	defer cq.mu.RUnlock()
	results := make([]*CompressQueueItem, len(cq.items))
	copy(results, cq.items)
	return results
}

func (cq *CompressQueue) GetCompletedZipPaths() []string {
	cq.mu.RLock()
	defer cq.mu.RUnlock()
	var paths []string
	for _, item := range cq.items {
		if item.Result != nil && item.Result.Success {
			paths = append(paths, item.Result.TargetZipPath)
		}
	}
	return paths
}

func (cq *CompressQueue) CleanupFailedTasks() {
	cq.mu.Lock()
	defer cq.mu.Unlock()
	for _, item := range cq.items {
		if item.Result != nil && !item.Result.Success {
			if item.Task.TargetZipPath != "" {
				os.Remove(item.Task.TargetZipPath)
			}
		}
	}
}

func (cq *CompressQueue) PrintSummary() {
	cq.mu.RLock()
	defer cq.mu.RUnlock()

	var successCount, failedCount int
	var totalOriginalSize, totalCompressedSize int64

	fmt.Println("\n========== 压缩任务汇总 ==========")
	for i, item := range cq.items {
		status := "成功"
		if item.Result == nil || !item.Result.Success {
			status = "失败"
			failedCount++
		} else {
			successCount++
			totalOriginalSize += item.Result.TotalSize
			totalCompressedSize += item.Result.CompressedSize
		}

		fmt.Printf("[%d] %s -> %s (%s)\n", i+1, item.Task.SourcePath, item.Task.TargetZipPath, status)
		if item.Result != nil && item.Result.Error != nil {
			fmt.Printf("    错误: %v\n", item.Result.Error)
		}
	}

	fmt.Println("================================")
	fmt.Printf("成功: %d, 失败: %d\n", successCount, failedCount)
	if totalOriginalSize > 0 {
		ratio := float64(totalCompressedSize) / float64(totalOriginalSize) * 100
		fmt.Printf("原始大小: %s, 压缩后: %s, 压缩率: %.2f%%\n",
			converter.ConvertFileSize(totalOriginalSize, 2),
			converter.ConvertFileSize(totalCompressedSize, 2),
			ratio)
	}
}
