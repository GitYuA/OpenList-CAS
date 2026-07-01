package casmeta

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const DefaultSliceSize int64 = 10 * 1024 * 1024

var globalExtAllowlist struct {
	sync.RWMutex
	value string
}

type Info struct {
	Name     string
	Size     int64
	MD5      string
	SliceMD5 string
}

type Payload struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	MD5        string `json:"md5"`
	SliceMD5   string `json:"sliceMd5"`
	CreateTime string `json:"create_time"`
}

func IsName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".cas")
}

func FileName(sourceName string) string {
	return sourceName + ".cas"
}

func SetGlobalExtAllowlist(allowlist string) {
	globalExtAllowlist.Lock()
	globalExtAllowlist.value = NormalizeExtAllowlist(allowlist)
	globalExtAllowlist.Unlock()
}

func GlobalExtAllowlist() string {
	globalExtAllowlist.RLock()
	defer globalExtAllowlist.RUnlock()
	return globalExtAllowlist.value
}

func GlobalExtAllowed(name string) bool {
	return ExtAllowed(name, GlobalExtAllowlist())
}

func NormalizeExtAllowlist(allowlist string) string {
	parts := strings.FieldsFunc(allowlist, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	seen := make(map[string]struct{}, len(parts))
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(part)), ".")
		if ext == "" {
			continue
		}
		if ext == "*" {
			return "*"
		}
		if _, ok := seen[ext]; ok {
			continue
		}
		seen[ext] = struct{}{}
		normalized = append(normalized, ext)
	}
	return strings.Join(normalized, ",")
}

func ExtAllowed(name string, allowlist string) bool {
	allowlist = NormalizeExtAllowlist(allowlist)
	if allowlist == "" || allowlist == "*" {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(strings.TrimSpace(name))), ".")
	if ext == "" {
		return false
	}
	for _, allowed := range strings.Split(allowlist, ",") {
		if ext == allowed {
			return true
		}
	}
	return false
}

func Encode(info *Info) ([]byte, error) {
	if info == nil {
		return nil, fmt.Errorf("missing cas info")
	}
	sliceMD5 := info.SliceMD5
	if sliceMD5 == "" {
		sliceMD5 = info.MD5
	}
	content, err := utils.Json.Marshal(Payload{
		Name:       info.Name,
		Size:       info.Size,
		MD5:        info.MD5,
		SliceMD5:   sliceMD5,
		CreateTime: strconv.FormatInt(time.Now().Unix(), 10),
	})
	if err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(content)), nil
}

func Decode(data []byte) (*Info, error) {
	data = bytes.TrimSpace(data)
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		decoded = data
	}
	var payload Payload
	if err = utils.Json.Unmarshal(decoded, &payload); err != nil {
		return nil, err
	}
	if payload.Name == "" || payload.Size < 0 || payload.MD5 == "" {
		return nil, fmt.Errorf("invalid cas payload")
	}
	if payload.SliceMD5 == "" {
		payload.SliceMD5 = payload.MD5
	}
	return &Info{
		Name:     payload.Name,
		Size:     payload.Size,
		MD5:      payload.MD5,
		SliceMD5: payload.SliceMD5,
	}, nil
}

func DeriveRestoreName(casName, originalName string) string {
	baseName := strings.TrimSuffix(casName, path.Ext(casName))
	baseName = strings.TrimSuffix(baseName, path.Ext(baseName))
	ext := path.Ext(originalName)
	if baseName == "" {
		baseName = strings.TrimSuffix(originalName, ext)
	}
	return baseName + ext
}

func ResolveRestoreName(casName string, info *Info) (string, error) {
	if info == nil {
		return "", fmt.Errorf("cas restore failed: missing cas payload")
	}
	if !IsName(casName) {
		return "", fmt.Errorf("cas restore failed: current file name %q does not end with .cas", casName)
	}
	trimmedName := strings.TrimSpace(strings.TrimSuffix(casName, path.Ext(casName)))
	if trimmedName == "" {
		return "", fmt.Errorf("cas restore failed: current .cas file name %q has an empty source file name", casName)
	}
	restoreName := strings.TrimSpace(DeriveRestoreName(casName, info.Name))
	if restoreName == "" {
		return "", fmt.Errorf("cas restore failed: current .cas file name %q has an empty source file name", casName)
	}
	if strings.ContainsAny(restoreName, `/\`) {
		return "", fmt.Errorf("cas restore failed: source file name %q contains a path", restoreName)
	}
	return restoreName, nil
}

type HasherWriter struct {
	fileMD5          hash.Hash
	sliceMD5         hash.Hash
	written          int64
	currentSliceSize int64
	sliceMD5Hexs     []string
	sliceSize        int64
}

func NewHasherWriter(sliceSize int64) *HasherWriter {
	if sliceSize <= 0 {
		sliceSize = DefaultSliceSize
	}
	return &HasherWriter{
		fileMD5:   utils.MD5.NewFunc(),
		sliceMD5:  utils.MD5.NewFunc(),
		sliceSize: sliceSize,
	}
}

func (w *HasherWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		remaining := w.sliceSize - w.currentSliceSize
		n := len(p)
		if int64(n) > remaining {
			n = int(remaining)
		}
		chunk := p[:n]
		_, _ = w.fileMD5.Write(chunk)
		_, _ = w.sliceMD5.Write(chunk)
		w.written += int64(n)
		w.currentSliceSize += int64(n)
		p = p[n:]
		if w.currentSliceSize == w.sliceSize {
			w.finishSlice()
		}
	}
	return total, nil
}

func (w *HasherWriter) finishSlice() {
	if w.currentSliceSize == 0 {
		return
	}
	w.sliceMD5Hexs = append(w.sliceMD5Hexs, strings.ToUpper(hex.EncodeToString(w.sliceMD5.Sum(nil))))
	w.sliceMD5.Reset()
	w.currentSliceSize = 0
}

func (w *HasherWriter) Info(name string) *Info {
	if w.written > w.sliceSize && w.currentSliceSize > 0 {
		w.finishSlice()
	}

	fileMD5Hex := hex.EncodeToString(w.fileMD5.Sum(nil))
	sliceMD5Hex := fileMD5Hex
	if w.written > w.sliceSize {
		sliceMD5Hex = utils.GetMD5EncodeStr(strings.Join(w.sliceMD5Hexs, "\n"))
	}

	return &Info{
		Name:     name,
		Size:     w.written,
		MD5:      fileMD5Hex,
		SliceMD5: sliceMD5Hex,
	}
}
