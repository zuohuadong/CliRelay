package config

import "testing"

func TestParseConfigBytesVideoStorage(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
video-storage:
  enabled: true
  endpoint: https://account.r2.cloudflarestorage.com
  region: auto
  bucket: generated-videos
  path-style: true
  prefix: outputs
  signed-url-ttl: 20m
  max-source-bytes: 1073741824
  access-key-id-env: TEST_VIDEO_ACCESS_KEY
  secret-access-key-env: TEST_VIDEO_SECRET_KEY
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	got := cfg.VideoStorage
	if !got.Enabled || got.Endpoint != "https://account.r2.cloudflarestorage.com" || got.Region != "auto" || got.Bucket != "generated-videos" || !got.PathStyle || got.Prefix != "outputs" || got.SignedURLTTL != "20m" || got.MaxSourceBytes != 1073741824 {
		t.Fatalf("VideoStorage = %+v", got)
	}
	if got.AccessKeyIDEnv != "TEST_VIDEO_ACCESS_KEY" || got.SecretAccessKeyEnv != "TEST_VIDEO_SECRET_KEY" {
		t.Fatalf("VideoStorage credential envs = %+v", got)
	}
}
