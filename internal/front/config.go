package front

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	CompressionJitter                                         int
	TargetBind                                                string
	TargetPort, HTTPPort, HTTPSPort                           int
	CacheSize, MaxCacheItemSize, MaxRequestBody               int64
	Gzip, DisableGzipOnAuth, H2C, ForwardHeaders, LogRequests bool
	// SkipDeflate hands response encoding to the application, for a
	// precomposed handler such as the engine. The caller then owns
	// Content-Encoding and Vary: Accept-Encoding on every cacheable
	// response (the front cache keys variants on Vary and snapshots headers
	// before the public chain adds it), the 406 negotiation policy, and the
	// No-Gzip-Compression / DisableGzipOnAuth policy for responses the app
	// pre-encodes: PublicCompression applies those vetoes only to bodies it
	// compresses itself.
	SkipDeflate                                  bool
	Domains                                      []string
	ACMEDirectory, StoragePath, EABKeyID, EABKey string
	IdleTimeout, ReadTimeout, WriteTimeout       time.Duration
}

func FromEnv() Config { return FromLookup(os.LookupEnv) }
func FromLookup(lookup func(string) (string, bool)) Config {
	get := func(key, fallback string) string {
		if value, ok := lookup("THRUSTER_" + key); ok {
			return value
		}
		if value, ok := lookup(key); ok {
			return value
		}
		return fallback
	}
	number := func(key string, fallback int64) int64 {
		n, err := strconv.ParseInt(get(key, ""), 10, 64)
		if err != nil {
			return fallback
		}
		return n
	}
	boolean := func(key string, fallback bool) bool {
		b, err := strconv.ParseBool(get(key, ""))
		if err != nil {
			return fallback
		}
		return b
	}
	port := func(key string, fallback int64) int {
		n := number(key, fallback)
		if n < 0 || n > 65535 {
			n = fallback
		}
		return int(n)
	}
	seconds := func(key string, fallback int64) time.Duration {
		return time.Duration(max(0, number(key, fallback))) * time.Second
	}
	c := Config{CompressionJitter: int(max(0, number("GZIP_COMPRESSION_JITTER", 32))), TargetBind: get("TARGET_BIND", "127.0.0.1"), TargetPort: port("TARGET_PORT", 3000), HTTPPort: port("HTTP_PORT", 80), HTTPSPort: port("HTTPS_PORT", 443), CacheSize: number("CACHE_SIZE", 64<<20), MaxCacheItemSize: number("MAX_CACHE_ITEM_SIZE", 1<<20), MaxRequestBody: number("MAX_REQUEST_BODY", 0), Gzip: boolean("GZIP_COMPRESSION_ENABLED", true), DisableGzipOnAuth: boolean("GZIP_COMPRESSION_DISABLE_ON_AUTH", false), H2C: boolean("H2C_ENABLED", false), LogRequests: boolean("LOG_REQUESTS", true), ACMEDirectory: get("ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory"), StoragePath: get("STORAGE_PATH", "./storage/thruster"), EABKeyID: get("EAB_KID", ""), EABKey: get("EAB_HMAC_KEY", ""), IdleTimeout: seconds("HTTP_IDLE_TIMEOUT", 60), ReadTimeout: seconds("HTTP_READ_TIMEOUT", 30), WriteTimeout: seconds("HTTP_WRITE_TIMEOUT", 30)}
	if net.ParseIP(c.TargetBind) == nil {
		c.TargetBind = "127.0.0.1"
	}
	for _, domain := range strings.Split(get("TLS_DOMAIN", ""), ",") {
		if domain = strings.TrimSpace(domain); domain != "" {
			c.Domains = append(c.Domains, domain)
		}
	}
	c.ForwardHeaders = boolean("FORWARD_HEADERS", len(c.Domains) == 0)
	return c
}
