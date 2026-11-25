package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"
)

const defaultHTTPAddr = ":8080"
const defaultAssetStorageRoot = ".data/asset-storage"

type Config struct {
	DatabaseURL              string
	HTTPAddr                 string
	AuthURL                  string
	AuthSecret               string
	AvalAIAPIKey             string
	AISafetyIdentifierSecret string
	Production               bool
	AssetStorageDriver       string
	AssetStorageRoot         string
	AssetS3Endpoint          string
	AssetS3Region            string
	AssetS3Bucket            string
	AssetS3AccessKeyID       string
	AssetS3SecretAccessKey   string
	AssetS3ForcePathStyle    bool
	FFprobePath              string
	FFprobeExpectedVersion   string
	AssetWorkerTempDir       string
}

func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

func LoadFrom(getenv func(string) string) (Config, error) {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	addr := getenv("BACKEND_HTTP_ADDR")
	if addr == "" {
		addr = defaultHTTPAddr
	}
	if strings.TrimSpace(addr) != addr || strings.ContainsAny(addr, " \t\r\n") {
		return Config{}, errors.New("BACKEND_HTTP_ADDR must not contain whitespace")
	}

	config := Config{
		DatabaseURL:              databaseURL,
		HTTPAddr:                 addr,
		AuthURL:                  getenv("BETTER_AUTH_URL"),
		AuthSecret:               getenv("BETTER_AUTH_SECRET"),
		AvalAIAPIKey:             getenv("AVALAI_API_KEY"),
		AISafetyIdentifierSecret: getenv("AI_SAFETY_IDENTIFIER_SECRET"),
		Production:               getenv("BACKEND_ENV") == "production" || getenv("NODE_ENV") == "production",
		AssetStorageDriver:       getenv("ASSET_STORAGE_DRIVER"),
		AssetStorageRoot:         getenv("ASSET_STORAGE_ROOT"),
		AssetS3Endpoint:          getenv("ASSET_S3_ENDPOINT"),
		AssetS3Region:            getenv("ASSET_S3_REGION"),
		AssetS3Bucket:            getenv("ASSET_S3_BUCKET"),
		AssetS3AccessKeyID:       getenv("ASSET_S3_ACCESS_KEY_ID"),
		AssetS3SecretAccessKey:   getenv("ASSET_S3_SECRET_ACCESS_KEY"),
		FFprobePath:              getenv("FFPROBE_PATH"),
		FFprobeExpectedVersion:   getenv("FFPROBE_EXPECTED_VERSION"),
		AssetWorkerTempDir:       getenv("ASSET_WORKER_TEMP_DIR"),
	}
	if raw := getenv("ASSET_S3_FORCE_PATH_STYLE"); raw != "" {
		switch raw {
		case "true":
			config.AssetS3ForcePathStyle = true
		case "false":
			config.AssetS3ForcePathStyle = false
		default:
			return Config{}, errors.New("ASSET_S3_FORCE_PATH_STYLE must be true or false")
		}
	}
	if config.AssetStorageDriver == "" {
		if config.Production {
			config.AssetStorageDriver = "s3"
		} else {
			config.AssetStorageDriver = "filesystem"
		}
	}
	if config.AssetStorageRoot == "" {
		config.AssetStorageRoot = defaultAssetStorageRoot
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) ValidateAI() error {
	if strings.TrimSpace(c.AvalAIAPIKey) == "" {
		return errors.New("AVALAI_API_KEY is required for AI generation")
	}
	if len([]rune(c.AISafetyIdentifierSecret)) < 32 || !utf8.ValidString(c.AISafetyIdentifierSecret) {
		return errors.New("AI_SAFETY_IDENTIFIER_SECRET must contain at least 32 characters")
	}
	return nil
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" || strings.TrimSpace(c.DatabaseURL) != c.DatabaseURL {
		return errors.New("DATABASE_URL is required")
	}
	if c.HTTPAddr == "" || strings.TrimSpace(c.HTTPAddr) != c.HTTPAddr || strings.ContainsAny(c.HTTPAddr, " \t\r\n") {
		return errors.New("BACKEND_HTTP_ADDR is invalid")
	}
	if c.AssetStorageDriver != "filesystem" && c.AssetStorageDriver != "s3" {
		return errors.New("ASSET_STORAGE_DRIVER must be filesystem or s3")
	}
	if c.Production && c.AssetStorageDriver != "s3" {
		return errors.New("production requires ASSET_STORAGE_DRIVER=s3")
	}
	if c.AssetStorageRoot == "" || strings.TrimSpace(c.AssetStorageRoot) != c.AssetStorageRoot {
		return errors.New("ASSET_STORAGE_ROOT is invalid")
	}
	if c.AssetStorageDriver == "s3" {
		endpoint, err := url.Parse(c.AssetS3Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
			return errors.New("ASSET_S3_ENDPOINT must be an HTTPS origin")
		}
		if strings.TrimSpace(c.AssetS3Region) != c.AssetS3Region || c.AssetS3Region == "" || strings.TrimSpace(c.AssetS3Bucket) != c.AssetS3Bucket || c.AssetS3Bucket == "" || c.AssetS3AccessKeyID == "" || c.AssetS3SecretAccessKey == "" {
			return errors.New("S3 asset storage configuration is incomplete")
		}
	}
	return nil
}

func (c Config) ValidateAuth() error {
	if len([]rune(c.AuthSecret)) < 32 || !utf8.ValidString(c.AuthSecret) {
		return errors.New("BETTER_AUTH_SECRET must contain at least 32 characters")
	}
	parsed, err := url.Parse(c.AuthURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("BETTER_AUTH_URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	return nil
}
