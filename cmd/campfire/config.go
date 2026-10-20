package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type databaseConfig struct {
	Path, Storage, Secret string
	Readers               int
}

func (config databaseConfig) backupPath() string {
	return filepath.Join(config.Storage, "backups", filepath.Base(config.Path))
}

func databaseConfigFromEnv() databaseConfig {
	storage := env("CAMPFIRE_STORAGE_PATH", "storage")
	return databaseConfig{
		Path:    env("CAMPFIRE_DATABASE_PATH", filepath.Join(storage, "db", env("RAILS_ENV", "production")+".sqlite3")),
		Storage: storage,
		Secret:  os.Getenv("SECRET_KEY_BASE"),
		Readers: max(1, runtime.GOMAXPROCS(0)),
	}
}

type serverConfig struct {
	Database                                     databaseConfig
	Secure                                       bool
	FragmentBytes, ResponseBytes, JobConcurrency int
	PushSubject, PushPublic, PushPrivate         string
}

func serverConfigFromEnv(database databaseConfig) (serverConfig, error) {
	config := serverConfig{Database: database, Secure: os.Getenv("DISABLE_SSL") == "", FragmentBytes: 32 << 20}
	if raw, ok := os.LookupEnv("CAMPFIRE_FRAGMENT_CACHE_MB"); ok {
		mb, err := strconv.Atoi(raw)
		if err != nil || mb < 0 || mb > 1<<20 {
			return serverConfig{}, fmt.Errorf("invalid CAMPFIRE_FRAGMENT_CACHE_MB %q", raw)
		}
		config.FragmentBytes = mb << 20
	}
	var err error
	config.ResponseBytes, err = responseCacheBudget()
	if err != nil {
		return serverConfig{}, fmt.Errorf("invalid CAMPFIRE_RESPONSE_CACHE_MB: %w", err)
	}
	config.JobConcurrency, _ = strconv.Atoi(os.Getenv("JOB_CONCURRENCY"))
	if config.JobConcurrency < 1 {
		config.JobConcurrency = 2
	}
	config.PushPublic = os.Getenv("VAPID_PUBLIC_KEY")
	config.PushPrivate = os.Getenv("VAPID_PRIVATE_KEY")
	config.PushSubject = os.Getenv("VAPID_SUBJECT")
	if config.PushSubject == "" {
		domain := strings.TrimSpace(strings.Split(os.Getenv("TLS_DOMAIN"), ",")[0])
		if domain != "" {
			config.PushSubject = "https://" + domain
		} else {
			config.PushSubject = "https://github.com/basecamp/once-campfire-go"
		}
	}
	return config, nil
}

func responseCacheBudget() (int, error) {
	raw, ok := os.LookupEnv("CAMPFIRE_RESPONSE_CACHE_MB")
	if !ok || raw == "" {
		return 64 << 20, nil
	}
	mb, err := strconv.Atoi(raw)
	if err != nil || mb < 0 || mb > 1024 {
		return 0, strconv.ErrSyntax
	}
	return mb << 20, nil
}
