package storage

import (
	"testing"

	"store-front/apps/api/internal/config"
)

func TestGetURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.StorageConfig
		key  string
		want string
	}{
		{
			name: "no public origin keeps the bucket segment (MinIO)",
			cfg:  config.StorageConfig{Endpoint: "http://localhost:9002", Bucket: "uploads"},
			key:  "uploads/2026/08/a.png",
			want: "http://localhost:9002/uploads/uploads/2026/08/a.png",
		},
		{
			// R2's S3 endpoint only answers signed requests, so object URLs
			// must come from the bucket's public origin instead.
			name: "public origin replaces endpoint and drops the bucket",
			cfg: config.StorageConfig{
				Endpoint:  "https://acct.r2.cloudflarestorage.com",
				Bucket:    "uploads",
				PublicURL: "https://pub-abc123.r2.dev",
			},
			key:  "uploads/2026/08/a.png",
			want: "https://pub-abc123.r2.dev/uploads/2026/08/a.png",
		},
		{
			name: "trailing slash on the public origin is not doubled",
			cfg:  config.StorageConfig{PublicURL: "https://cdn.example.com/"},
			key:  "uploads/a.png",
			want: "https://cdn.example.com/uploads/a.png",
		},
		{
			name: "spaces in a key stay escaped",
			cfg:  config.StorageConfig{PublicURL: "https://cdn.example.com"},
			key:  "uploads/my file.png",
			want: "https://cdn.example.com/uploads/my%20file.png",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Storage{bucket: c.cfg.Bucket, cfg: c.cfg}
			if got := s.GetURL(c.key); got != c.want {
				t.Errorf("GetURL(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}
