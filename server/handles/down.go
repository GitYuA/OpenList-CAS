package handles

import (
	"errors"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func Down(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	filename := stdpath.Base(rawPath)
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	if shouldPreviewCASOnDown(c) || shouldRestoreCASOnDownload(storage, filename) {
		link, file, ok, previewErr := linkCASPreview(c, rawPath, storage, model.LinkArgs{
			IP:       c.ClientIP(),
			Header:   c.Request.Header,
			Type:     c.Query("type"),
			Redirect: true,
		})
		if previewErr != nil {
			common.ErrorPage(c, previewErr, 500)
			return
		}
		if ok {
			if common.ShouldProxy(storage, file.GetName()) {
				proxy(c, link, file, storage.GetStorage().ProxyRange)
			} else {
				redirect(c, link)
			}
			return
		}
	}
	if common.ShouldProxy(storage, filename) {
		Proxy(c)
		return
	} else {
		link, _, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
			IP:       c.ClientIP(),
			Header:   c.Request.Header,
			Type:     c.Query("type"),
			Redirect: true,
		})
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
		redirect(c, link)
	}
}

func Proxy(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)
	filename := stdpath.Base(rawPath)
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	if shouldPreviewCASOnDown(c) || shouldRestoreCASOnDownload(storage, filename) {
		link, file, ok, previewErr := linkCASPreview(c, rawPath, storage, model.LinkArgs{
			Header: c.Request.Header,
			Type:   c.Query("type"),
		})
		if previewErr != nil {
			common.ErrorPage(c, previewErr, 500)
			return
		}
		if ok {
			proxy(c, link, file, storage.GetStorage().ProxyRange)
			return
		}
	}
	if canProxy(storage, filename) {
		if _, ok := c.GetQuery("d"); !ok {
			if url := common.GenerateDownProxyURL(storage.GetStorage(), rawPath); url != "" {
				c.Redirect(302, url)
				return
			}
		}
		link, file, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
			Header: c.Request.Header,
			Type:   c.Query("type"),
		})
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
		proxy(c, link, file, storage.GetStorage().ProxyRange)
	} else {
		common.ErrorPage(c, errors.New("proxy not allowed"), 403)
		return
	}
}

func shouldPreviewCASOnDown(c *gin.Context) bool {
	if c.Query("type") != "" {
		return true
	}
	if c.GetHeader("Range") != "" {
		return true
	}
	switch strings.ToLower(c.GetHeader("Sec-Fetch-Dest")) {
	case "video", "audio":
		return true
	}
	accept := strings.ToLower(c.GetHeader("Accept"))
	return strings.Contains(accept, "video/") || strings.Contains(accept, "audio/")
}

func isCASFile(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".cas")
}

func shouldRestoreCASOnDownload(storage driver.Driver, filename string) bool {
	if !isCASFile(filename) {
		return false
	}
	controller, ok := storage.(driver.CASDownloadRestoreController)
	return ok && controller.CASDownloadRestoreEnabled()
}

func linkCASPreview(c *gin.Context, rawPath string, storage driver.Driver, args model.LinkArgs) (*model.Link, model.Obj, bool, error) {
	namer, ok := storage.(driver.CASPreviewNamer)
	if !ok {
		return nil, nil, false, nil
	}
	obj, err := fs.Get(c.Request.Context(), rawPath, &fs.GetArgs{})
	if err != nil {
		return nil, nil, false, err
	}
	previewName, err := namer.CASPreviewName(c.Request.Context(), obj)
	if err != nil {
		return nil, nil, false, err
	}
	if previewName == "" || previewName == obj.GetName() {
		return nil, nil, false, nil
	}
	args.Type = "cas_video"
	link, file, err := fs.Link(c.Request.Context(), rawPath, args)
	if err != nil {
		return nil, nil, false, err
	}
	if file != nil {
		file = &model.ObjWrapName{Name: previewName, Obj: file}
	} else {
		file = &model.Object{Name: previewName, Size: obj.GetSize(), Modified: obj.ModTime(), Ctime: obj.CreateTime(), HashInfo: obj.GetHash()}
	}
	return link, file, true, nil
}

func redirect(c *gin.Context, link *model.Link) {
	defer link.Close()
	var err error
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
	if setting.GetBool(conf.ForwardDirectLinkParams) {
		query := c.Request.URL.Query()
		for _, v := range conf.SlicesMap[conf.IgnoreDirectLinkParams] {
			query.Del(v)
		}
		link.URL, err = utils.InjectQuery(link.URL, query)
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
	}
	c.Redirect(302, link.URL)
}

func proxy(c *gin.Context, link *model.Link, file model.Obj, proxyRange bool) {
	defer link.Close()
	var err error
	if link.URL != "" && setting.GetBool(conf.ForwardDirectLinkParams) {
		query := c.Request.URL.Query()
		for _, v := range conf.SlicesMap[conf.IgnoreDirectLinkParams] {
			query.Del(v)
		}
		link.URL, err = utils.InjectQuery(link.URL, query)
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
	}
	if proxyRange {
		link = common.ProxyRange(c, link, file.GetSize())
	}
	Writer := &common.WrittenResponseWriter{ResponseWriter: c.Writer}
	err = common.Proxy(Writer, c.Request, link, file)
	if err == nil {
		return
	}
	if Writer.IsWritten() {
		log.Errorf("%s %s local proxy error: %+v", c.Request.Method, c.Request.URL.Path, err)
	} else {
		if statusCode, ok := errs.UnwrapOrSelf(err).(net.HttpStatusCodeError); ok {
			common.ErrorPage(c, err, int(statusCode), true)
		} else {
			common.ErrorPage(c, err, 500, true)
		}
	}
}

// TODO need optimize
// when can be proxy?
// 1. text file
// 2. config.MustProxy()
// 3. storage.WebProxy
// 4. proxy_types
// solution: text_file + shouldProxy()
func canProxy(storage driver.Driver, filename string) bool {
	if storage.Config().MustProxy() || storage.GetStorage().WebProxy || storage.GetStorage().WebdavProxyURL() {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.ProxyTypes], utils.Ext(filename)) {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.TextTypes], utils.Ext(filename)) {
		return true
	}
	return false
}
