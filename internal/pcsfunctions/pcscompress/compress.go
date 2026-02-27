package pcscompress

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrSourceNotExist        = errors.New("源路径不存在")
	ErrSourceNotDirectory    = errors.New("源路径不是目录")
	ErrPermissionDenied      = errors.New("权限不足")
	ErrDiskSpaceInsufficient = errors.New("磁盘空间不足")
	ErrCompressFailed        = errors.New("压缩失败")
	ErrCreateZipFailed       = errors.New("创建ZIP文件失败")
)

type CompressOptions struct {
	Depth           int  `json:"depth"`
	IncludeHidden   bool `json:"include_hidden"`
	CompressionLevel int `json:"compression_level"`
}

type CompressTask struct {
	SourcePath     string          `json:"source_path"`
	TargetZipPath  string          `json:"target_zip_path"`
	Options        CompressOptions `json:"options"`
	TotalFiles     int64           `json:"total_files"`
	TotalSize      int64           `json:"total_size"`
	CompressedSize int64           `json:"compressed_size"`
	ProcessedFiles int64           `json:"processed_files"`
	StartTime      time.Time       `json:"start_time"`
	EndTime        time.Time       `json:"end_time"`
	mu             sync.RWMutex    `json:"-"`
	OnProgress     func(processed, total int64, currentFile string) `json:"-"`
}

type CompressResult struct {
	Success        bool           `json:"success"`
	SourcePath     string         `json:"source_path"`
	TargetZipPath  string         `json:"target_zip_path"`
	TotalFiles     int64          `json:"total_files"`
	TotalSize      int64          `json:"total_size"`
	CompressedSize int64          `json:"compressed_size"`
	Duration       time.Duration  `json:"duration"`
	Error          error          `json:"error"`
}

func NewCompressTask(sourcePath, targetZipPath string, opts *CompressOptions) *CompressTask {
	if opts == nil {
		opts = &CompressOptions{
			Depth:            -1,
			IncludeHidden:    false,
			CompressionLevel: 6,
		}
	}
	return &CompressTask{
		SourcePath:    sourcePath,
		TargetZipPath: targetZipPath,
		Options:       *opts,
	}
}

func (ct *CompressTask) GetProgress() (processed, total int64, percentage float64) {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	processed = ct.ProcessedFiles
	total = ct.TotalFiles
	if total > 0 {
		percentage = float64(processed) / float64(total) * 100
	}
	return
}

func (ct *CompressTask) GetSpeed() int64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	if ct.StartTime.IsZero() {
		return 0
	}
	elapsed := time.Since(ct.StartTime).Seconds()
	if elapsed > 0 {
		return int64(float64(ct.CompressedSize) / elapsed)
	}
	return 0
}

func (ct *CompressTask) updateProgress(processed, compressed int64, currentFile string) {
	ct.mu.Lock()
	ct.ProcessedFiles = processed
	ct.CompressedSize = compressed
	ct.mu.Unlock()
	if ct.OnProgress != nil {
		ct.OnProgress(processed, ct.TotalFiles, currentFile)
	}
}

func (ct *CompressTask) countFiles() error {
	err := filepath.Walk(ct.SourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !ct.Options.IncludeHidden {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != "." && base != ".." {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !info.IsDir() {
			ct.TotalFiles++
			ct.TotalSize += info.Size()
		}
		return nil
	})
	return err
}

func (ct *CompressTask) checkDiskSpace() error {
	var stat syscall.Statfs_t
	targetDir := filepath.Dir(ct.TargetZipPath)
	if targetDir == "" {
		targetDir = "."
	}
	err := syscall.Statfs(targetDir, &stat)
	if err != nil {
		return nil
	}
	freeSpace := int64(stat.Bavail) * int64(stat.Bsize)
	estimatedSize := ct.TotalSize
	if estimatedSize > freeSpace {
		return ErrDiskSpaceInsufficient
	}
	return nil
}

func (ct *CompressTask) Execute() *CompressResult {
	result := &CompressResult{
		SourcePath:    ct.SourcePath,
		TargetZipPath: ct.TargetZipPath,
	}

	sourceInfo, err := os.Stat(ct.SourcePath)
	if err != nil {
		if os.IsPermission(err) {
			result.Error = ErrPermissionDenied
		} else if os.IsNotExist(err) {
			result.Error = ErrSourceNotExist
		} else {
			result.Error = err
		}
		return result
	}

	if !sourceInfo.IsDir() {
		result.Error = ErrSourceNotDirectory
		return result
	}

	ct.StartTime = time.Now()
	defer func() {
		ct.EndTime = time.Now()
		result.Duration = ct.EndTime.Sub(ct.StartTime)
	}()

	err = ct.countFiles()
	if err != nil {
		result.Error = fmt.Errorf("统计文件失败: %w", err)
		return result
	}

	if ct.TotalFiles == 0 {
		result.Error = errors.New("目录为空，没有文件可压缩")
		return result
	}

	err = ct.checkDiskSpace()
	if err != nil {
		result.Error = err
		return result
	}

	zipFile, err := os.Create(ct.TargetZipPath)
	if err != nil {
		if os.IsPermission(err) {
			result.Error = ErrPermissionDenied
		} else {
			result.Error = fmt.Errorf("%w: %v", ErrCreateZipFailed, err)
		}
		return result
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	var processedFiles int64 = 0
	var compressedSize int64 = 0

	sourceBase := filepath.Base(ct.SourcePath)

	err = filepath.Walk(ct.SourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !ct.Options.IncludeHidden {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != "." && base != ".." {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		relPath, err := filepath.Rel(ct.SourcePath, path)
		if err != nil {
			return err
		}

		zipPath := filepath.Join(sourceBase, relPath)
		zipPath = strings.ReplaceAll(zipPath, "\\", "/")

		if info.IsDir() {
			_, err = zipWriter.Create(zipPath + "/")
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = zipPath
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			if os.IsPermission(err) {
				fmt.Printf("警告: 跳过无权限文件: %s\n", path)
				return nil
			}
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		if err != nil {
			file.Close()
			return fmt.Errorf("写入文件 %s 失败: %w", path, err)
		}

		processedFiles++
		compressedSize += info.Size()
		ct.updateProgress(processedFiles, compressedSize, path)

		return nil
	})

	if err != nil {
		result.Error = fmt.Errorf("%w: %v", ErrCompressFailed, err)
		return result
	}

	result.Success = true
	result.TotalFiles = ct.TotalFiles
	result.TotalSize = ct.TotalSize
	result.CompressedSize = compressedSize

	return result
}

func GenerateUniqueZipName(sourcePath string) string {
	absPath, err := filepath.Abs(sourcePath)
	if err != nil {
		absPath = sourcePath
	}
	baseName := filepath.Base(absPath)
	timestamp := time.Now().Format("20060102_150405")
	return fmt.Sprintf("%s_%s.zip", baseName, timestamp)
}

func GenerateSimpleZipName(sourcePath string) string {
	absPath, err := filepath.Abs(sourcePath)
	if err != nil {
		absPath = sourcePath
	}
	return filepath.Base(absPath) + ".zip"
}

func GetSubDirectories(parentPath string, depth int) ([]string, error) {
	var dirs []string
	parentInfo, err := os.Stat(parentPath)
	if err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() {
		return nil, ErrSourceNotDirectory
	}

	if depth == 0 {
		dirs = append(dirs, parentPath)
		return dirs, nil
	}

	if depth == 1 {
		entries, err := os.ReadDir(parentPath)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				fullPath := filepath.Join(parentPath, entry.Name())
				dirs = append(dirs, fullPath)
			}
		}
		return dirs, nil
	}

	err = filepath.Walk(parentPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(parentPath, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		currentDepth := strings.Count(relPath, string(os.PathSeparator)) + 1

		if depth > 0 && currentDepth > depth {
			return filepath.SkipDir
		}

		if depth < 0 || currentDepth <= depth {
			dirs = append(dirs, path)
		}

		return nil
	})

	return dirs, err
}
