package config

import "testing"

func TestLoadFromRequiresDatabaseURL(t *testing.T) {
	_, err := LoadFrom(func(string) string { return "" })
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("expected required database URL error, got %v", err)
	}
}

func TestLoadFromUsesLocalHTTPDefault(t *testing.T) {
	values := map[string]string{"DATABASE_URL": "postgres://localhost/better_content"}
	config, err := LoadFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("LoadFrom returned an error: %v", err)
	}
	if config.HTTPAddr != ":8080" {
		t.Fatalf("expected default address :8080, got %q", config.HTTPAddr)
	}
	if config.AssetStorageDriver != "filesystem" || config.AssetStorageRoot != ".data/asset-storage" {
		t.Fatalf("unexpected local asset storage config: driver=%q root=%q", config.AssetStorageDriver, config.AssetStorageRoot)
	}
}

func TestLoadFromMarksProductionEnvironment(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":               "postgres://localhost/better_content",
		"NODE_ENV":                   "production",
		"ASSET_S3_ENDPOINT":          "https://objects.example.test",
		"ASSET_S3_REGION":            "us-east-1",
		"ASSET_S3_BUCKET":            "media",
		"ASSET_S3_ACCESS_KEY_ID":     "access",
		"ASSET_S3_SECRET_ACCESS_KEY": "secret",
	}
	config, err := LoadFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("LoadFrom returned an error: %v", err)
	}
	if !config.Production {
		t.Fatal("production NODE_ENV was not preserved")
	}
	if config.AssetStorageDriver != "s3" {
		t.Fatalf("production storage driver = %q, want s3", config.AssetStorageDriver)
	}
}

func TestLoadFromEnablesProductionForBackendEnvironment(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":               "postgres://localhost/better_content",
		"BACKEND_ENV":                "production",
		"ASSET_S3_ENDPOINT":          "https://objects.example.test",
		"ASSET_S3_REGION":            "us-east-1",
		"ASSET_S3_BUCKET":            "media",
		"ASSET_S3_ACCESS_KEY_ID":     "access",
		"ASSET_S3_SECRET_ACCESS_KEY": "secret",
	}
	config, err := LoadFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("LoadFrom returned an error: %v", err)
	}
	if !config.Production {
		t.Fatal("production BACKEND_ENV was not preserved")
	}
}

func TestLoadFromRejectsWhitespaceAddress(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":      "postgres://localhost/better_content",
		"BACKEND_HTTP_ADDR": " 127.0.0.1:8080",
	}
	_, err := LoadFrom(func(key string) string { return values[key] })
	if err == nil || err.Error() != "BACKEND_HTTP_ADDR must not contain whitespace" {
		t.Fatalf("expected whitespace address error, got %v", err)
	}
}

func TestLoadFromRequiresLiteralS3PathStyleBoolean(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":              "postgres://localhost/better_content",
		"ASSET_STORAGE_DRIVER":      "filesystem",
		"ASSET_S3_FORCE_PATH_STYLE": "1",
	}
	if _, err := LoadFrom(func(key string) string { return values[key] }); err == nil || err.Error() != "ASSET_S3_FORCE_PATH_STYLE must be true or false" {
		t.Fatalf("invalid path style value error = %v", err)
	}
}

func TestLoadFromRejectsInvalidAuthenticationConfiguration(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"short secret":       {"BETTER_AUTH_SECRET": "short"},
		"path in origin":     {"BETTER_AUTH_URL": "https://example.com/app"},
		"origin credentials": {"BETTER_AUTH_URL": "https://user:pass@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{
				"DATABASE_URL":       "postgres://localhost/better_content",
				"BETTER_AUTH_URL":    "http://localhost:3000",
				"BETTER_AUTH_SECRET": "0123456789abcdef0123456789abcdef",
			}
			for key, value := range overrides {
				values[key] = value
			}
			config, err := LoadFrom(func(key string) string { return values[key] })
			if err != nil {
				t.Fatalf("LoadFrom returned a non-auth configuration error: %v", err)
			}
			if err := config.ValidateAuth(); err == nil {
				t.Fatal("ValidateAuth returned nil error for invalid authentication configuration")
			}
		})
	}
}

func TestValidateAIRequiresCredentialsAndLongSafetySecret(t *testing.T) {
	for name, config := range map[string]Config{
		"missing API key":     {AISafetyIdentifierSecret: "0123456789abcdef0123456789abcdef"},
		"short safety secret": {AvalAIAPIKey: "configured", AISafetyIdentifierSecret: "short"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := config.ValidateAI(); err == nil {
				t.Fatal("ValidateAI accepted incomplete credentials")
			}
		})
	}
	config := Config{AvalAIAPIKey: "configured", AISafetyIdentifierSecret: "0123456789abcdef0123456789abcdef"}
	if err := config.ValidateAI(); err != nil {
		t.Fatalf("ValidateAI rejected valid configuration: %v", err)
	}
}
