package pcscompress

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsfunctions"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsfunctions/pcsupload"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/checksum"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/taskframework"
)

type CompressUploadTaskUnit struct {
	SourcePath        string
	TargetZipPath     string
	SavePath          string
	PCS               *baidupcs.BaiduPCS
	Parallel          int
	MaxRetry          int
	Policy            string
	NoRapidUpload     bool
	DeleteAfterUpload bool
	CompressOpts      *CompressOptions
	Statistic         *CompressStatistic

	taskInfo       *taskframework.TaskInfo
	compressResult *CompressResult
}

func (cutu *CompressUploadTaskUnit) SetTaskInfo(taskInfo *taskframework.TaskInfo) {
	cutu.taskInfo = taskInfo
}

func (cutu *CompressUploadTaskUnit) Run() (result *taskframework.TaskUnitRunResult) {
	result = &taskframework.TaskUnitRunResult{}

	fmt.Printf("[%s] 开始压缩: %s\n", cutu.taskInfo.Id(), cutu.SourcePath)

	task := NewCompressTask(cutu.SourcePath, cutu.TargetZipPath, cutu.CompressOpts)
	task.OnProgress = func(processed, total int64, currentFile string) {
		percentage := float64(0)
		if total > 0 {
			percentage = float64(processed) / float64(total) * 100
		}
		fmt.Printf("\r[%s] 压缩进度: %d/%d (%.1f%%) - %s", 
			cutu.taskInfo.Id(), processed, total, percentage, 
			converter.ShortDisplay(filepath.Base(currentFile), 30))
	}

	cutu.compressResult = task.Execute()

	if !cutu.compressResult.Success {
		result.ResultMessage = fmt.Sprintf("压缩失败: %v", cutu.compressResult.Error)
		result.Err = cutu.compressResult.Error
		result.NeedRetry = false
		return
	}

	fmt.Printf("\n[%s] 压缩完成: %s -> %s (原始大小: %s, 压缩后: %s)\n",
		cutu.taskInfo.Id(),
		cutu.SourcePath,
		cutu.TargetZipPath,
		converter.ConvertFileSize(cutu.compressResult.TotalSize, 2),
		converter.ConvertFileSize(cutu.compressResult.CompressedSize, 2))

	if cutu.Statistic != nil {
		cutu.Statistic.AddTotalSize(cutu.compressResult.TotalSize)
		cutu.Statistic.AddCompressedSize(cutu.compressResult.CompressedSize)
		cutu.Statistic.AddFileCount(cutu.compressResult.TotalFiles)
	}

	fmt.Printf("[%s] 开始上传: %s\n", cutu.taskInfo.Id(), cutu.TargetZipPath)

	uploadResult := cutu.upload()
	if uploadResult != nil {
		result.Succeed = uploadResult.Succeed
		result.NeedRetry = uploadResult.NeedRetry
		result.Err = uploadResult.Err
		result.ResultMessage = uploadResult.ResultMessage
		result.Extra = uploadResult.Extra
	}

	if result.Succeed && cutu.DeleteAfterUpload {
		fmt.Printf("[%s] 删除本地压缩包: %s\n", cutu.taskInfo.Id(), cutu.TargetZipPath)
		err := os.Remove(cutu.TargetZipPath)
		if err != nil {
			fmt.Printf("[%s] 警告: 删除压缩包失败: %v\n", cutu.taskInfo.Id(), err)
		}
	}

	return
}

func (cutu *CompressUploadTaskUnit) upload() *taskframework.TaskUnitRunResult {
	uploadDatabase, err := pcsupload.NewUploadingDatabase()
	if err != nil {
		return &taskframework.TaskUnitRunResult{
			ResultMessage: fmt.Sprintf("打开上传数据库失败: %v", err),
			Err:           err,
			NeedRetry:      false,
		}
	}
	defer uploadDatabase.Close()

	statistic := &pcsupload.UploadStatistic{}
	statistic.StartTimer()

	uploadTask := &pcsupload.UploadTaskUnit{
		LocalFileChecksum: checksum.NewLocalFileChecksum(cutu.TargetZipPath, int(baidupcs.SliceMD5Size)),
		SavePath:          cutu.SavePath,
		PCS:               cutu.PCS,
		UploadingDatabase: uploadDatabase,
		Parallel:          cutu.Parallel,
		PrintFormat:       "[%s] ↑ %s/%s %s/s in %s ............\n",
		NoRapidUpload:     cutu.NoRapidUpload,
		Policy:            cutu.Policy,
		UploadStatistic:   statistic,
	}

	uploadTask.SetTaskInfo(cutu.taskInfo)

	return uploadTask.Run()
}

func (cutu *CompressUploadTaskUnit) OnRetry(lastRunResult *taskframework.TaskUnitRunResult) {
	if lastRunResult.Err == nil {
		fmt.Printf("[%s] %s, 重试 %d/%d\n", cutu.taskInfo.Id(), lastRunResult.ResultMessage, cutu.taskInfo.Retry(), cutu.taskInfo.MaxRetry())
		return
	}
	fmt.Printf("[%s] %s, %s, 重试 %d/%d\n", cutu.taskInfo.Id(), lastRunResult.ResultMessage, lastRunResult.Err, cutu.taskInfo.Retry(), cutu.taskInfo.MaxRetry())
}

func (cutu *CompressUploadTaskUnit) OnSuccess(lastRunResult *taskframework.TaskUnitRunResult) {
	fmt.Printf("[%s] 压缩上传成功: %s -> %s\n", cutu.taskInfo.Id(), cutu.SourcePath, cutu.SavePath)
}

func (cutu *CompressUploadTaskUnit) OnFailed(lastRunResult *taskframework.TaskUnitRunResult) {
	if lastRunResult.Err == nil {
		fmt.Printf("[%s] %s\n", cutu.taskInfo.Id(), lastRunResult.ResultMessage)
		return
	}
	fmt.Printf("[%s] %s, %s\n", cutu.taskInfo.Id(), lastRunResult.ResultMessage, lastRunResult.Err)

	if cutu.TargetZipPath != "" {
		if _, err := os.Stat(cutu.TargetZipPath); err == nil {
			fmt.Printf("[%s] 清理失败的压缩包: %s\n", cutu.taskInfo.Id(), cutu.TargetZipPath)
			os.Remove(cutu.TargetZipPath)
		}
	}
}

func (cutu *CompressUploadTaskUnit) OnComplete(lastRunResult *taskframework.TaskUnitRunResult) {
}

func (cutu *CompressUploadTaskUnit) RetryWait() time.Duration {
	return pcsfunctions.RetryWait(cutu.taskInfo.Retry())
}
