package _189pc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	istream "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
)

type Cloud189PC struct {
	model.Storage
	Addition

	client *resty.Client

	loginParam  *LoginParam
	qrcodeParam *QRLoginParam

	tokenInfo *AppSessionResp

	uploadThread int

	familyTransferFolder    *Cloud189Folder
	cleanFamilyTransferFile func()
	debounceClean           func()
	cleanupMu               sync.Mutex
	cleanupTimer            *time.Timer
	cleanupActive           int
	cleanupFamilyObjs       []model.Obj
	autoRestoreMu           sync.Mutex
	autoRestoreInFlight     sync.Map

	storageConfig   driver.Config
	ref             *Cloud189PC
	cron            *cron.Cron
	autoRestoreCron *cron.Cron
}

func (y *Cloud189PC) Config() driver.Config {
	if y.storageConfig.Name == "" {
		y.storageConfig = config
	}
	return y.storageConfig
}

func (y *Cloud189PC) GetAddition() driver.Additional {
	return &y.Addition
}

func (y *Cloud189PC) Init(ctx context.Context) (err error) {
	y.storageConfig = config
	casmeta.SetGlobalExtAllowlist(y.CASExtAllowlist)
	if y.isFamily() {
		// 兼容旧上传接口
		if y.Addition.RapidUpload || y.Addition.UploadMethod == "old" {
			y.storageConfig.NoOverwriteUpload = true
		}
	} else {
		// 家庭云转存，不支持覆盖上传
		if y.Addition.FamilyTransfer {
			y.storageConfig.NoOverwriteUpload = true
		}
	}
	// 处理个人云和家庭云参数
	if y.isFamily() && y.RootFolderID == "-11" {
		y.RootFolderID = ""
	}
	if !y.isFamily() && y.RootFolderID == "" {
		y.RootFolderID = "-11"
	}

	// 限制上传线程数
	y.uploadThread, _ = strconv.Atoi(y.UploadThread)
	if y.uploadThread < 1 || y.uploadThread > 32 {
		y.uploadThread, y.UploadThread = 3, "3"
	}

	if y.ref == nil {
		// 初始化请求客户端
		if y.client == nil {
			y.client = base.NewRestyClient().SetHeaders(map[string]string{
				"Accept":  "application/json;charset=UTF-8",
				"Referer": WEB_URL,
			})
		}

		// 先尝试用Token刷新，之后尝试登陆
		if y.Addition.RefreshToken != "" {
			y.tokenInfo = &AppSessionResp{RefreshToken: y.Addition.RefreshToken}
			if err = y.refreshToken(); err != nil {
				return err
			}
		} else {
			if err = y.login(); err != nil {
				return err
			}
		}

		// 初始化并启动 cron 任务
		y.cron = cron.NewCron(time.Duration(time.Minute * 5))
		// 每5分钟执行一次 keepAlive
		y.cron.Do(y.keepAlive)
	}

	// 处理家庭云ID
	if y.FamilyID == "" {
		if y.FamilyID, err = y.getFamilyID(); err != nil {
			return err
		}
	}

	// 创建中转文件夹
	if y.FamilyTransfer {
		if err := y.createFamilyTransferFolder(); err != nil {
			return err
		}
	}

	// 清理转存文件节流
	y.cleanFamilyTransferFile = utils.NewThrottle2(time.Minute, func() {
		if err := y.cleanFamilyTransfer(context.TODO()); err != nil {
			utils.Log.Errorf("cleanFamilyTransferFolderError:%s", err)
		}
	})
	y.debounceClean = y.newDebounceCleaner()
	if y.AutoRestoreExistingCAS && strings.TrimSpace(y.AutoRestoreExistingCASPaths) != "" {
		y.autoRestoreCron = cron.NewCron(y.autoRestoreInterval())
		y.autoRestoreCron.Do(func() {
			if err := y.autoRestoreCAS(context.TODO()); err != nil {
				utils.Log.Errorf("autoRestoreCASError:%s", err)
			}
		})
	}
	return err
}

func (d *Cloud189PC) InitReference(storage driver.Driver) error {
	refStorage, ok := storage.(*Cloud189PC)
	if ok {
		d.ref = refStorage
		return nil
	}
	return errs.NotSupport
}

func (y *Cloud189PC) Drop(ctx context.Context) error {
	y.ref = nil
	y.cleanupMu.Lock()
	if y.cleanupTimer != nil {
		y.cleanupTimer.Stop()
		y.cleanupTimer = nil
	}
	y.cleanupMu.Unlock()
	if y.cron != nil {
		y.cron.Stop()
		y.cron = nil
	}
	if y.autoRestoreCron != nil {
		y.autoRestoreCron.Stop()
		y.autoRestoreCron = nil
	}
	return nil
}

func (y *Cloud189PC) autoRestoreInterval() time.Duration {
	minutes := y.GetStorage().CacheExpiration
	if minutes <= 0 {
		minutes = 30
	}
	return time.Duration(minutes) * time.Minute
}

func (y *Cloud189PC) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if dirPath, ok := y.manualRefreshAutoRestorePath(args); ok {
		y.startManualRefreshAutoRestore(ctx, dir, dirPath)
	}
	return y.getFiles(ctx, dir.GetID(), y.isFamily())
}

func (y *Cloud189PC) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if y.shouldPlayCAS(file, args) {
		return y.linkCASVideo(ctx, file, args)
	}
	var downloadUrl struct {
		URL string `json:"fileDownloadUrl"`
	}

	isFamily := y.isFamily()
	fullUrl := API_URL
	if isFamily {
		fullUrl += "/family/file"
	}
	fullUrl += "/getFileDownloadUrl.action"

	_, err := y.get(fullUrl, func(r *resty.Request) {
		r.SetContext(ctx)
		r.SetQueryParam("fileId", file.GetID())
		if isFamily {
			r.SetQueryParams(map[string]string{
				"familyId": y.FamilyID,
			})
		} else {
			r.SetQueryParams(map[string]string{
				"dt":   "3",
				"flag": "1",
			})
		}
	}, &downloadUrl, isFamily)
	if err != nil {
		return nil, err
	}

	// 重定向获取真实链接
	downloadUrl.URL = strings.Replace(strings.ReplaceAll(downloadUrl.URL, "&amp;", "&"), "http://", "https://", 1)
	res, err := base.NoRedirectClient.R().SetContext(ctx).SetDoNotParseResponse(true).Get(downloadUrl.URL)
	if err != nil {
		return nil, err
	}
	defer res.RawBody().Close()
	if res.StatusCode() == 302 {
		downloadUrl.URL = res.Header().Get("location")
	}

	like := &model.Link{
		URL: downloadUrl.URL,
		Header: http.Header{
			"User-Agent": []string{base.UserAgent},
		},
	}
	/*
		// 获取链接有效时常
		strs := regexp.MustCompile(`(?i)expire[^=]*=([0-9]*)`).FindStringSubmatch(downloadUrl.URL)
		if len(strs) == 2 {
			timestamp, err := strconv.ParseInt(strs[1], 10, 64)
			if err == nil {
				expired := time.Duration(timestamp-time.Now().Unix()) * time.Second
				like.Expiration = &expired
			}
		}
	*/
	return like, nil
}

func (y *Cloud189PC) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	isFamily := y.isFamily()
	fullUrl := API_URL
	if isFamily {
		fullUrl += "/family/file"
	}
	fullUrl += "/createFolder.action"

	var newFolder Cloud189Folder
	_, err := y.post(fullUrl, func(req *resty.Request) {
		req.SetContext(ctx)
		req.SetQueryParams(map[string]string{
			"folderName":   dirName,
			"relativePath": "",
		})
		if isFamily {
			req.SetQueryParams(map[string]string{
				"familyId": y.FamilyID,
				"parentId": parentDir.GetID(),
			})
		} else {
			req.SetQueryParams(map[string]string{
				"parentFolderId": parentDir.GetID(),
			})
		}
	}, &newFolder, isFamily)
	if err != nil {
		return nil, err
	}
	return &newFolder, nil
}

func (y *Cloud189PC) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	isFamily := y.isFamily()
	other := map[string]string{"targetFileName": dstDir.GetName()}

	resp, err := y.CreateBatchTask("MOVE", IF(isFamily, y.FamilyID, ""), dstDir.GetID(), other, BatchTaskInfo{
		FileId:   srcObj.GetID(),
		FileName: srcObj.GetName(),
		IsFolder: BoolToNumber(srcObj.IsDir()),
	})
	if err != nil {
		return nil, err
	}
	if err = y.WaitBatchTask("MOVE", resp.TaskID, time.Millisecond*400); err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (y *Cloud189PC) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	isFamily := y.isFamily()
	queryParam := make(map[string]string)
	fullUrl := API_URL
	method := http.MethodPost
	if isFamily {
		fullUrl += "/family/file"
		method = http.MethodGet
		queryParam["familyId"] = y.FamilyID
	}

	var newObj model.Obj
	switch f := srcObj.(type) {
	case *Cloud189File:
		fullUrl += "/renameFile.action"
		queryParam["fileId"] = srcObj.GetID()
		queryParam["destFileName"] = newName
		newObj = &Cloud189File{Icon: f.Icon} // 复用预览
	case *Cloud189Folder:
		fullUrl += "/renameFolder.action"
		queryParam["folderId"] = srcObj.GetID()
		queryParam["destFolderName"] = newName
		newObj = &Cloud189Folder{}
	default:
		return nil, errs.NotSupport
	}

	_, err := y.request(fullUrl, method, func(req *resty.Request) {
		req.SetContext(ctx).SetQueryParams(queryParam)
	}, nil, newObj, isFamily)
	if err != nil {
		return nil, err
	}
	return newObj, nil
}

func (y *Cloud189PC) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	isFamily := y.isFamily()
	other := map[string]string{"targetFileName": dstDir.GetName()}

	resp, err := y.CreateBatchTask("COPY", IF(isFamily, y.FamilyID, ""), dstDir.GetID(), other, BatchTaskInfo{
		FileId:   srcObj.GetID(),
		FileName: srcObj.GetName(),
		IsFolder: BoolToNumber(srcObj.IsDir()),
	})
	if err != nil {
		return err
	}
	return y.WaitBatchTask("COPY", resp.TaskID, time.Second)
}

func (y *Cloud189PC) Remove(ctx context.Context, obj model.Obj) error {
	isFamily := y.isFamily()

	resp, err := y.CreateBatchTask("DELETE", IF(isFamily, y.FamilyID, ""), "", nil, BatchTaskInfo{
		FileId:   obj.GetID(),
		FileName: obj.GetName(),
		IsFolder: BoolToNumber(obj.IsDir()),
	})
	if err != nil {
		return err
	}
	// 批量任务数量限制，过快会导致无法删除
	return y.WaitBatchTask("DELETE", resp.TaskID, time.Millisecond*200)
}

func (y *Cloud189PC) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	sourceName := stream.GetName()
	sourceSize := stream.GetSize()

	if y.RestoreSourceFromCAS && isCASName(sourceName) {
		casData, err := io.ReadAll(stream)
		if err != nil {
			return nil, err
		}
		info, err := y.parseCAS(casData)
		if err != nil {
			return nil, err
		}
		restoredName, err := resolveCASRestoreName(sourceName, info)
		if err != nil {
			return nil, err
		}
		if casmeta.ExtAllowed(restoredName, y.CASExtAllowlist) {
			restored, err := y.restoreCAS(ctx, dstDir, info, sourceName, false)
			if err != nil {
				return nil, err
			}
			y.notifyTaskDone()
			return restored, nil
		}
		stream = &istream.FileStream{
			Ctx: ctx,
			Obj: &model.Object{
				Name:     sourceName,
				Size:     sourceSize,
				Modified: stream.ModTime(),
				Ctime:    stream.CreateTime(),
				HashInfo: stream.GetHash(),
			},
			Reader:            bytes.NewReader(casData),
			Mimetype:          stream.GetMimetype(),
			ForceStreamUpload: stream.IsForceStreamUpload(),
			Exist:             stream.GetExist(),
		}
	}

	newObj, info, err := y.uploadFile(ctx, dstDir, stream, up)
	if err != nil {
		return nil, err
	}
	if info != nil {
		info.Name = sourceName
		info.Size = sourceSize
	}
	casObj, err := y.uploadCAS(ctx, dstDir, info)
	if err != nil {
		return nil, err
	}
	if casObj != nil && y.shouldDeleteSource() {
		if !y.sourceKeptOnlyInFamilyTransfer(info) {
			if err = y.deleteSource(ctx, dstDir, newObj, info); err != nil {
				return nil, err
			}
		}
		y.notifyTaskDone()
		return casObj, nil
	}
	y.notifyTaskDone()
	return newObj, nil
}

func (y *Cloud189PC) uploadFile(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (newObj model.Obj, info *casUploadInfo, err error) {
	overwrite := true
	isFamily := y.isFamily()

	// 响应时间长,按需启用
	if y.Addition.RapidUpload && !stream.IsForceStreamUpload() {
		if newObj, info, err := y.RapidUpload(ctx, dstDir, stream, isFamily, overwrite); err == nil {
			return newObj, info, nil
		}
	}

	uploadMethod := y.UploadMethod
	if stream.IsForceStreamUpload() {
		uploadMethod = "stream"
	}

	// 旧版上传家庭云也有限制
	if uploadMethod == "old" {
		return y.OldUpload(ctx, dstDir, stream, up, isFamily, overwrite)
	}

	// 开启家庭云转存
	if !isFamily && y.FamilyTransfer {
		y.beginCleanupTask()
		defer y.endCleanupTask()
		if err = y.ensureFamilyTransferFolder(ctx); err != nil {
			return nil, nil, err
		}

		// 修改上传目标为家庭云文件夹
		transferDstDir := dstDir
		dstDir = y.familyTransferFolder

		// 使用临时文件名
		srcName := stream.GetName()
		skipPersonalTransfer := y.shouldDeleteSource() && y.shouldUploadCAS(srcName)
		stream = &WrapFileStreamer{
			FileStreamer: stream,
			Name:         fmt.Sprintf("0%s.transfer", uuid.NewString()),
		}

		// 使用家庭云上传
		isFamily = true
		overwrite = false

		defer func() {
			if newObj != nil {
				if skipPersonalTransfer {
					y.queueFamilyTransferCleanupObj(newObj)
					return
				}
				// 转存家庭云文件到个人云
				transferCtx := context.TODO()
				err = y.SaveFamilyFileToPersonCloud(transferCtx, y.FamilyID, newObj, transferDstDir, true, srcName)
				// 删除家庭云源文件
				y.queueFamilyTransferCleanupObj(newObj)
				// 批量任务有概率删不掉
				// 转存失败返回错误
				if err != nil {
					return
				}

				// 优先查找临时名，兼容 targetFileName 被 COPY 接口忽略的情况
				var file *Cloud189File
				var needRename bool
				file, needRename, err = y.waitFamilyTransferFile(transferCtx, transferDstDir, newObj.GetName(), srcName)
				if err != nil {
					return
				}
				if needRename {
					// 重命名转存文件
					newObj, err = y.Rename(transferCtx, file, srcName)
					if err != nil {
						// 重命名失败删除源文件
						_ = y.Delete(transferCtx, "", file)
					}
					return
				}
				newObj = file
				return
			}
		}()
	}

	switch uploadMethod {
	case "rapid":
		return y.FastUpload(ctx, dstDir, stream, up, isFamily, overwrite)
	case "stream":
		if stream.GetSize() == 0 {
			return y.FastUpload(ctx, dstDir, stream, up, isFamily, overwrite)
		}
		fallthrough
	default:
		return y.StreamUpload(ctx, dstDir, stream, up, isFamily, overwrite)
	}
}

func (y *Cloud189PC) sourceKeptOnlyInFamilyTransfer(info *casUploadInfo) bool {
	return info != nil && !y.isFamily() && y.FamilyTransfer && y.shouldDeleteSource() && y.shouldUploadCAS(info.Name)
}

func (y *Cloud189PC) waitFamilyTransferFile(ctx context.Context, dstDir model.Obj, tempName string, finalName string) (*Cloud189File, bool, error) {
	const attempts = 5
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if tempName != "" {
			file, err := y.findFileByName(ctx, tempName, dstDir.GetID(), false)
			if err == nil {
				return file, tempName != finalName, nil
			}
			if err != errs.ObjectNotFound {
				return nil, false, err
			}
		}
		if finalName != "" {
			file, err := y.findFileByName(ctx, finalName, dstDir.GetID(), false)
			if err == nil {
				return file, false, nil
			}
			if err != errs.ObjectNotFound {
				return nil, false, err
			}
		}
		time.Sleep(time.Second)
	}
	return nil, false, fmt.Errorf("unknown error: No transfer file obtained %s or %s", tempName, finalName)
}

func (y *Cloud189PC) ensureFamilyTransferFolder(ctx context.Context) error {
	if y.familyTransferFolder != nil && y.familyTransferFolder.GetID() != "" {
		if _, err := y.getFilesWithPage(ctx, y.familyTransferFolder.GetID(), true, 1, 1, "filename", "asc"); err == nil {
			return nil
		}
	}
	if folder, err := y.findFamilyTransferFolder(ctx); err == nil {
		y.familyTransferFolder = folder
		return nil
	}
	return y.createFamilyTransferFolder()
}

func (y *Cloud189PC) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	capacityInfo, err := y.getCapacityInfo(ctx)
	if err != nil {
		return nil, err
	}
	var total, used int64
	if y.isFamily() {
		total = capacityInfo.FamilyCapacityInfo.TotalSize
		used = capacityInfo.FamilyCapacityInfo.UsedSize
	} else {
		total = capacityInfo.CloudCapacityInfo.TotalSize
		used = capacityInfo.CloudCapacityInfo.UsedSize
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}
