package _189pc

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	LoginType    string `json:"login_type" type:"select" options:"password,qrcode" default:"password" required:"true"`
	Username     string `json:"username" required:"true"`
	Password     string `json:"password" required:"true"`
	VCode        string `json:"validate_code"`
	AccessToken  string `json:"access_token" required:"false"`
	RefreshToken string `json:"refresh_token" help:"To switch accounts, please clear this field"`
	driver.RootID
	OrderBy                     string `json:"order_by" type:"select" options:"filename,filesize,lastOpTime" default:"filename"`
	OrderDirection              string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	Type                        string `json:"type" type:"select" options:"personal,family" default:"personal"`
	FamilyID                    string `json:"family_id"`
	UploadMethod                string `json:"upload_method" type:"select" options:"stream,rapid,old" default:"stream"`
	UploadThread                string `json:"upload_thread" default:"3" help:"1<=thread<=32"`
	FamilyTransfer              bool   `json:"family_transfer"`
	RapidUpload                 bool   `json:"rapid_upload"`
	NoUseOcr                    bool   `json:"no_use_ocr"`
	GenerateCAS                 bool   `json:"generate_cas" help:"After upload, generate a same-name .cas file in the same directory"`
	DeleteSource                bool   `json:"delete_source" help:"After generating the .cas file, delete the uploaded source file and clear recycle bin"`
	RestoreSourceFromCAS        bool   `json:"restore_source_from_cas" help:"Restore source file from .cas metadata"`
	DeleteCASAfterRestore       bool   `json:"delete_cas_after_restore" help:"After restoring the source file, delete the .cas file and clear recycle bin"`
	AutoRestoreExistingCAS      bool   `json:"auto_restore_existing_cas" help:"Automatically scan monitored directories and restore .cas files"`
	AutoRestoreExistingCASPaths string `json:"auto_restore_existing_cas_paths" type:"text" help:"One path per line; monitors these directories and subdirectories only. Empty means disabled"`
	CASExtAllowlist             string `json:"cas_ext_allowlist" help:"CAS extension allowlist shared with Local. Empty means all extensions are allowed. Example: mp4,mkv,iso,zip"`
	CASDownloadRestore          bool   `json:"cas_download_restore" help:"When enabled, downloading .cas files via /d/* will restore and return the real file instead of raw CAS metadata"`
}

var config = driver.Config{
	Name:        "189CloudPC",
	DefaultRoot: "-11",
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Cloud189PC{}
	})
}
