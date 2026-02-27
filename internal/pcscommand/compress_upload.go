package pcscommand

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsfunctions/pcscompress"
	"github.com/qjfoidnh/BaiduPCS-Go/pcstable"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/taskframework"
)

const (
	DefaultCompressMaxRetry = 3
)

type CompressUploadOptions struct {
	Parallel         int
	MaxRetry         int
	Load             int
	NoRapidUpload    bool
	Policy           string
	DeleteAfterUpload bool
	Depth            int
	IncludeHidden     bool
}

func RunCompressUpload(localPaths []string, savePath string, opt *CompressUploadOptions) {
	if opt == nil {
		opt = &CompressUploadOptions{}
	}

	if opt.Parallel <= 0 {
		opt.Parallel = pcsconfig.Config.MaxUploadParallel
	}

	if opt.MaxRetry < 0 {
		opt.MaxRetry = DefaultCompressMaxRetry
	}

	if opt.Load <= 0 {
		opt.Load = pcsconfig.Config.MaxUploadLoad
	}

	if opt.Policy != baidupcs.SkipPolicy && opt.Policy != baidupcs.OverWritePolicy && opt.Policy != baidupcs.RsyncPolicy {
		opt.Policy = pcsconfig.Config.UPolicy
	}

	err := matchPathByShellPatternOnce(&savePath)
	if err != nil {
		fmt.Printf("警告: 压缩上传, 获取网盘路径 %s 错误, %s\n", savePath, err)
	}

	switch len(localPaths) {
	case 0:
		fmt.Printf("本地路径为空\n")
		return
	}

	var (
		pcs      = GetBaiduPCS()
		executor = &taskframework.TaskExecutor{
			IsFailedDeque: true,
		}
		statistic = pcscompress.NewCompressStatistic()
	)

	fmt.Print("\n")
	fmt.Printf("[0] 提示: 当前上传单个文件最大并发量为: %d, 最大同时上传文件数为: %d\n", opt.Parallel, opt.Load)
	fmt.Printf("[0] 提示: 压缩深度: %d (0=仅当前目录, 1=一级子目录, -1=无限深度)\n", opt.Depth)
	fmt.Printf("[0] 提示: 上传后删除压缩包: %v\n", opt.DeleteAfterUpload)

	compressOpts := &pcscompress.CompressOptions{
		Depth:            opt.Depth,
		IncludeHidden:    opt.IncludeHidden,
		CompressionLevel: 6,
	}

	var taskCount int
	for _, localPath := range localPaths {
		sourceInfo, err := os.Stat(localPath)
		if err != nil {
			fmt.Printf("警告: 路径不存在或无法访问: %s, %s\n", localPath, err)
			continue
		}

		if !sourceInfo.IsDir() {
			fmt.Printf("警告: 跳过非目录路径: %s\n", localPath)
			continue
		}

		absPath, err := filepath.Abs(localPath)
		if err != nil {
			fmt.Printf("警告: 获取绝对路径失败: %s, %s\n", localPath, err)
			continue
		}

		var directoriesToCompress []string
		if opt.Depth == 0 {
			directoriesToCompress = []string{absPath}
		} else if opt.Depth == 1 {
			subDirs, err := pcscompress.GetSubDirectories(absPath, 1)
			if err != nil {
				fmt.Printf("警告: 获取子目录失败: %s, %s\n", localPath, err)
				continue
			}
			directoriesToCompress = subDirs
		} else {
			directoriesToCompress = []string{absPath}
		}

		for _, dirPath := range directoriesToCompress {
			zipPath := pcscompress.GenerateSimpleZipName(dirPath)

			if !pcsutil.ChPathLegal(dirPath) {
				fmt.Printf("[0] %s 路径含有非法字符，已跳过!\n", dirPath)
				continue
			}

			targetSavePath := path.Clean(savePath + baidupcs.PathSeparator + path.Base(zipPath))
			info := executor.Append(&pcscompress.CompressUploadTaskUnit{
				SourcePath:        dirPath,
				TargetZipPath:     zipPath,
				SavePath:          targetSavePath,
				PCS:               pcs,
				Parallel:          opt.Parallel,
				MaxRetry:          opt.MaxRetry,
				Policy:            opt.Policy,
				NoRapidUpload:     opt.NoRapidUpload,
				DeleteAfterUpload: opt.DeleteAfterUpload,
				CompressOpts:      compressOpts,
				Statistic:         statistic,
			}, opt.MaxRetry)

			taskCount++
			fmt.Printf("[%s] 加入压缩上传队列: %s\n", info.Id(), dirPath)
		}
	}

	if executor.Count() == 0 {
		fmt.Printf("未检测到可压缩上传的目录.\n")
		return
	}

	if executor.Count() > opt.Load {
		executor.SetParallel(opt.Load)
		fmt.Printf("[0] 提示: 压缩包数量 %d 超过最大并发数 %d, 将限制并发为 %d\n", executor.Count(), opt.Load, opt.Load)
	} else {
		executor.SetParallel(executor.Count())
	}

	executor.Execute()

	fmt.Printf("\n")
	fmt.Printf("压缩上传结束, 时间: %s\n", statistic.Elapsed()/1e6*1e6)
	fmt.Printf("总文件数: %d, 原始大小: %s, 压缩后大小: %s\n",
		statistic.FileCount(),
		converter.ConvertFileSize(statistic.TotalSize(), 2),
		converter.ConvertFileSize(statistic.CompressedSize(), 2))

	failedList := executor.FailedDeque()
	if failedList.Size() != 0 {
		fmt.Printf("以下目录压缩上传失败: \n")
		tb := pcstable.NewTable(os.Stdout)
		for e := failedList.Shift(); e != nil; e = failedList.Shift() {
			item := e.(*taskframework.TaskInfoItem)
			unit := item.Unit.(*pcscompress.CompressUploadTaskUnit)
			tb.Append([]string{item.Info.Id(), unit.SourcePath})
		}
		tb.Render()
	}
}

func RunCompressOnly(localPaths []string, outputDir string, opt *CompressUploadOptions) {
	if opt == nil {
		opt = &CompressUploadOptions{}
	}

	switch len(localPaths) {
	case 0:
		fmt.Printf("本地路径为空\n")
		return
	}

	compressOpts := &pcscompress.CompressOptions{
		Depth:            opt.Depth,
		IncludeHidden:    opt.IncludeHidden,
		CompressionLevel: 6,
	}

	queue := pcscompress.NewCompressQueue(1)

	queue.OnTaskStart = func(item *pcscompress.CompressQueueItem) {
		fmt.Printf("[压缩开始] %s\n", item.Task.SourcePath)
	}

	queue.OnTaskProgress = func(item *pcscompress.CompressQueueItem, processed, total int64, currentFile string) {
		percentage := float64(0)
		if total > 0 {
			percentage = float64(processed) / float64(total) * 100
		}
		fmt.Printf("\r[压缩中] %s: %d/%d (%.1f%%)", 
			filepath.Base(item.Task.SourcePath), processed, total, percentage)
	}

	queue.OnTaskComplete = func(item *pcscompress.CompressQueueItem) {
		if item.Result.Success {
			fmt.Printf("\n[压缩完成] %s -> %s (原始: %s, 压缩后: %s)\n",
				item.Task.SourcePath,
				item.Task.TargetZipPath,
				converter.ConvertFileSize(item.Result.TotalSize, 2),
				converter.ConvertFileSize(item.Result.CompressedSize, 2))
		} else {
			fmt.Printf("\n[压缩失败] %s: %v\n", item.Task.SourcePath, item.Result.Error)
		}
	}

	for _, localPath := range localPaths {
		sourceInfo, err := os.Stat(localPath)
		if err != nil {
			fmt.Printf("警告: 路径不存在或无法访问: %s, %s\n", localPath, err)
			continue
		}

		if !sourceInfo.IsDir() {
			fmt.Printf("警告: 跳过非目录路径: %s\n", localPath)
			continue
		}

		absPath, err := filepath.Abs(localPath)
		if err != nil {
			fmt.Printf("警告: 获取绝对路径失败: %s, %s\n", localPath, err)
			continue
		}

		var directoriesToCompress []string
		if opt.Depth == 0 {
			directoriesToCompress = []string{absPath}
		} else if opt.Depth == 1 {
			subDirs, err := pcscompress.GetSubDirectories(absPath, 1)
			if err != nil {
				fmt.Printf("警告: 获取子目录失败: %s, %s\n", localPath, err)
				continue
			}
			directoriesToCompress = subDirs
		} else {
			directoriesToCompress = []string{absPath}
		}

		for _, dirPath := range directoriesToCompress {
			zipName := pcscompress.GenerateSimpleZipName(dirPath)
			var zipPath string
			if outputDir != "" {
				zipPath = filepath.Join(outputDir, zipName)
			} else {
				zipPath = zipName
			}

			err = queue.AddTask(dirPath, zipPath, compressOpts)
			if err != nil {
				fmt.Printf("警告: 添加压缩任务失败: %s, %s\n", localPath, err)
				continue
			}
		}
	}

	if queue.Count() == 0 {
		fmt.Printf("未检测到可压缩的目录.\n")
		return
	}

	fmt.Printf("\n开始压缩 %d 个目录...\n", queue.Count())
	queue.Execute()
	queue.PrintSummary()
}
