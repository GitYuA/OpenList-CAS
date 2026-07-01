package _189pc

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type casUploadInfo = casmeta.Info

func isCASName(name string) bool {
	return casmeta.IsName(name)
}

func (y *Cloud189PC) shouldUploadCAS(name string) bool {
	return y.GenerateCAS && !isCASName(name) && casmeta.ExtAllowed(name, y.CASExtAllowlist)
}

func (y *Cloud189PC) shouldDeleteSource() bool {
	return y.GenerateCAS && y.DeleteSource
}

func (y *Cloud189PC) uploadCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo) (model.Obj, error) {
	if info == nil || !y.shouldUploadCAS(info.Name) {
		return nil, nil
	}
	content, err := casmeta.Encode(info)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	casObj := &model.Object{
		Name:     casmeta.FileName(info.Name),
		Size:     int64(len(content)),
		Modified: now,
		Ctime:    now,
		HashInfo: utils.NewHashInfo(utils.MD5, utils.HashData(utils.MD5, content)),
	}
	casStream := &stream.FileStream{
		Ctx:      ctx,
		Obj:      casObj,
		Reader:   bytes.NewReader(content),
		Mimetype: "text/plain",
	}
	uploadedCASObj, _, err := y.uploadFile(ctx, dstDir, casStream, func(float64) {})
	if err != nil {
		return nil, err
	}
	if uploadedCASObj != nil {
		return uploadedCASObj, nil
	}
	return casObj, nil
}

func (y *Cloud189PC) deleteSource(ctx context.Context, dstDir model.Obj, uploadedObj model.Obj, info *casUploadInfo) error {
	if info == nil || !y.shouldDeleteSource() || !y.shouldUploadCAS(info.Name) {
		return nil
	}
	if uploadedObj == nil {
		var err error
		uploadedObj, err = y.findFileByName(ctx, info.Name, dstDir.GetID(), y.isFamily())
		if err != nil {
			return err
		}
	}
	return y.Delete(ctx, IF(y.isFamily(), y.FamilyID, ""), uploadedObj)
}

func (y *Cloud189PC) parseCAS(data []byte) (*casUploadInfo, error) {
	return casmeta.Decode(data)
}

func (y *Cloud189PC) parseCASFromStreamer(file model.FileStreamer) (*casUploadInfo, error) {
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return y.parseCAS(data)
}

func (y *Cloud189PC) parseCASFromObj(ctx context.Context, file model.Obj) (*casUploadInfo, error) {
	link, err := y.Link(ctx, file, model.LinkArgs{Type: "raw_cas"})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	if link.URL == "" {
		return nil, fmt.Errorf("cas link has no url")
	}
	req := base.RestyClient.R().SetContext(ctx)
	if link.Header != nil {
		req.SetHeaders(headerToMap(link.Header))
	}
	resp, err := req.Get(link.URL)
	if err != nil {
		return nil, err
	}
	return y.parseCAS(resp.Body())
}

func headerToMap(header http.Header) map[string]string {
	headers := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return headers
}

func ComputeSliceMD5sFromReader(reader io.Reader, sliceSize int64) (string, []string, error) {
	if sliceSize <= 0 {
		sliceSize = casmeta.DefaultSliceSize
	}
	hasher := casmeta.NewHasherWriter(sliceSize)
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", nil, err
	}
	info := hasher.Info("")
	if info.SliceMD5 == info.MD5 {
		return info.MD5, nil, nil
	}
	return info.MD5, []string{info.SliceMD5}, nil
}

func GetMD5Str(s string) string {
	h := utils.MD5.NewFunc()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
