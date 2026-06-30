package _189pc

import (
	"encoding/hex"
	"io"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// ComputeSliceMD5sFromReader is kept as a compatibility helper for
// offline download transfer code. It does not re-enable torrent CAS APIs.
func ComputeSliceMD5sFromReader(reader io.Reader, sliceSize int64) (string, []string, error) {
	if sliceSize <= 0 {
		sliceSize = torrent.DefaultPieceSize
	}

	fileMD5Hash := utils.MD5.NewFunc()
	sliceMD5s := make([]string, 0)

	buf := make([]byte, sliceSize)
	for {
		n, err := io.ReadFull(reader, buf)
		if n > 0 {
			chunk := buf[:n]
			fileMD5Hash.Write(chunk)
			sliceMD5 := strings.ToUpper(utils.HashData(utils.MD5, chunk))
			sliceMD5s = append(sliceMD5s, sliceMD5)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
	}

	fileMD5Hex := strings.ToUpper(hex.EncodeToString(fileMD5Hash.Sum(nil)))
	return fileMD5Hex, sliceMD5s, nil
}
