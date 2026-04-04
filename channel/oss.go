// xbot OSS Provider — abstract file storage backend (local / qiniu)

package channel

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	log "xbot/logger"

	"github.com/qiniu/go-sdk/v7/auth"
	"github.com/qiniu/go-sdk/v7/storage"
)

// OSSProvider abstracts file storage operations.
type OSSProvider interface {
	// Upload uploads file data to the given key.
	Upload(key string, data []byte) error
	// GetDownloadURL returns a publicly accessible URL for the given object key.
	GetDownloadURL(key string) (url string, err error)
	// Name returns the provider name.
	Name() string
	// Domain returns the CDN domain URL for this provider (empty if not applicable).
	Domain() string
}

// ---------------------------------------------------------------------------
// LocalProvider — wraps existing local disk storage
// ---------------------------------------------------------------------------

// LocalProvider stores files on local disk (existing behavior).
type LocalProvider struct {
	uploadDir string // base upload directory
}

// NewLocalProvider creates a local storage provider.
func NewLocalProvider(uploadDir string) *LocalProvider {
	return &LocalProvider{uploadDir: uploadDir}
}

func (p *LocalProvider) Name() string { return "local" }
func (p *LocalProvider) Domain() string { return "" }

func (p *LocalProvider) Upload(key string, data []byte) error {
	// Local provider should not be used for cloud upload — files are handled directly.
	return fmt.Errorf("local provider does not support Upload; files are saved directly by handleFileUpload")
}

func (p *LocalProvider) GetDownloadURL(key string) (string, error) {
	// Local mode: return the file path (relative to uploadDir/web/)
	// This is only used as fallback; normally local files are embedded or copied directly.
	return "", fmt.Errorf("local provider does not support GetDownloadURL; files are accessed directly")
}

// ---------------------------------------------------------------------------
// QiniuProvider — Qiniu Cloud (七牛云) Object Storage
// ---------------------------------------------------------------------------

// qiniuZoneMap maps region IDs to Qiniu storage.Zone values.
func getQiniuZone(region string) *storage.Zone {
	switch region {
	case "z0":
		return &storage.ZoneHuadong
	case "z1":
		return &storage.ZoneHuabei
	case "z2":
		return &storage.ZoneHuanan
	case "na0":
		return &storage.ZoneBeimei
	case "as0":
		return &storage.ZoneXinjiapo
	case "cn-east-2":
		return &storage.ZoneHuadong
	default:
		log.WithField("region", region).Warn("Unknown Qiniu region, falling back to z0")
		return &storage.ZoneHuadong
	}
}

// QiniuProvider stores files on Qiniu Cloud Object Storage.
type QiniuProvider struct {
	accessKey string
	secretKey string
	bucket    string
	domain    string // CDN domain, e.g. "https://cdn.example.com"
	region    string // region ID, e.g. "z0"
	mac       *auth.Credentials
}

// NewQiniuProvider creates a Qiniu Cloud storage provider.
func NewQiniuProvider(accessKey, secretKey, bucket, domain, region string) (*QiniuProvider, error) {
	if accessKey == "" || secretKey == "" || bucket == "" || domain == "" {
		return nil, fmt.Errorf("qiniu: access_key, secret_key, bucket, and domain are required")
	}
	if region == "" {
		region = "z0"
	}
	domain = strings.TrimSpace(strings.TrimRight(domain, "/"))
	// MakePrivateURL uses url.Parse on "domain/key"; without a scheme the result is not a valid
	// absolute URL. curl and other clients then default to http:// on port 80, which often
	// returns 401 in front of HTTPS-only CDN (e.g. openresty).
	if !strings.HasPrefix(domain, "http://") && !strings.HasPrefix(domain, "https://") {
		domain = "https://" + domain
	}
	return &QiniuProvider{
		accessKey: accessKey,
		secretKey: secretKey,
		bucket:    bucket,
		domain:    domain,
		region:    region,
		mac:       auth.New(accessKey, secretKey),
	}, nil
}

func (p *QiniuProvider) Name() string { return "qiniu" }

func (p *QiniuProvider) Upload(key string, data []byte) error {
	// Generate upload token with 1-hour expiry
	putPolicy := storage.PutPolicy{
		Scope:   fmt.Sprintf("%s:%s", p.bucket, key),
		Expires: uint64(time.Now().Add(time.Hour).Unix()),
	}
	upToken := putPolicy.UploadToken(p.mac)

	// Create form uploader with correct region zone
	cfg := storage.Config{
		UseHTTPS:      true,
		UseCdnDomains: false,
		Zone:          getQiniuZone(p.region),
	}
	formUploader := storage.NewFormUploader(&cfg)
	ret := storage.PutRet{}

	err := formUploader.Put(context.TODO(), &ret, upToken, key, bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		return fmt.Errorf("qiniu upload failed: %w", err)
	}

	log.WithFields(log.Fields{
		"key":  key,
		"hash": ret.Hash,
	}).Debug("File uploaded to Qiniu")
	return nil
}

func (p *QiniuProvider) GetDownloadURL(key string) (string, error) {
	deadline := time.Now().Add(time.Hour).Unix()
	signedURL := storage.MakePrivateURL(p.mac, p.domain, key, deadline)
	log.WithField("key", key).Debug("Generated Qiniu download URL")
	return signedURL, nil
}
func (p *QiniuProvider) Domain() string { return p.domain }

// ---------------------------------------------------------------------------
// NewOSSProvider — factory function
// ---------------------------------------------------------------------------

// NewOSSProvider creates the appropriate OSS provider based on config.
// provider must be "local" or "qiniu".
func NewOSSProvider(provider, uploadDir string, cfg ...QiniuConfig) (OSSProvider, error) {
	switch provider {
	case "local":
		return NewLocalProvider(uploadDir), nil
	case "qiniu":
		if len(cfg) == 0 {
			return nil, fmt.Errorf("qiniu config is required for qiniu provider")
		}
		c := cfg[0]
		return NewQiniuProvider(c.AccessKey, c.SecretKey, c.Bucket, c.Domain, c.Region)
	default:
		return nil, fmt.Errorf("unknown OSS provider: %s", provider)
	}
}

// QiniuConfig holds Qiniu-specific configuration.
type QiniuConfig struct {
	AccessKey string
	SecretKey string
	Bucket    string
	Domain    string
	Region    string
}
