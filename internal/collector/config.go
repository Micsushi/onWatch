package collector

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

type Config struct {
	ServerURL       string
	DeviceID        string
	TokenFile       string
	SpoolDir        string
	CollectInterval time.Duration
	UploadInterval  time.Duration
	BatchSize       int
	SpoolMaxBytes   int64
	Token           string
	HomeDir         string
}

func LoadConfig(args []string) (Config, error) {
	cfg := Config{CollectInterval: 15 * time.Second, UploadInterval: 15 * time.Second, BatchSize: 500, SpoolMaxBytes: 512 << 20}
	home, _ := os.UserHomeDir()
	if home != "" {
		_ = godotenv.Load(filepath.Join(home, ".onwatch", ".env"))
		cfg.SpoolDir = filepath.Join(home, ".onwatch", "collector-spool")
	}
	_ = godotenv.Load(".env")
	values := map[string]string{
		"server": os.Getenv("ONWATCH_COLLECTOR_SERVER_URL"), "device-id": os.Getenv("ONWATCH_COLLECTOR_DEVICE_ID"),
		"token-file": os.Getenv("ONWATCH_COLLECTOR_TOKEN_FILE"), "spool": os.Getenv("ONWATCH_COLLECTOR_SPOOL_DIR"),
		"collect-interval": os.Getenv("ONWATCH_COLLECTOR_INTERVAL"), "upload-interval": os.Getenv("ONWATCH_COLLECTOR_UPLOAD_INTERVAL"),
		"batch-size": os.Getenv("ONWATCH_COLLECTOR_BATCH_SIZE"), "spool-max-bytes": os.Getenv("ONWATCH_COLLECTOR_SPOOL_MAX_BYTES"),
		"home": os.Getenv("ONWATCH_COLLECTOR_HOME"),
	}
	allowed := map[string]bool{
		"server": true, "device-id": true, "token-file": true, "spool": true,
		"collect-interval": true, "upload-interval": true, "batch-size": true,
		"spool-max-bytes": true, "home": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "collector" || arg == "run" || arg == "status" || arg == "install" || arg == "uninstall" || arg == "restart" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			return cfg, fmt.Errorf("unknown collector argument %q", arg)
		}
		key := strings.TrimPrefix(arg, "--")
		if cut := strings.IndexByte(key, '='); cut >= 0 {
			name := key[:cut]
			if !allowed[name] {
				return cfg, fmt.Errorf("unknown collector option --%s", name)
			}
			values[name] = key[cut+1:]
			continue
		}
		if !allowed[key] {
			return cfg, fmt.Errorf("unknown collector option --%s", key)
		}
		if i+1 >= len(args) {
			return cfg, fmt.Errorf("--%s requires a value", key)
		}
		values[key] = args[i+1]
		i++
	}
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(values["server"]), "/")
	cfg.DeviceID = strings.TrimSpace(values["device-id"])
	cfg.TokenFile = strings.TrimSpace(values["token-file"])
	if values["spool"] != "" {
		cfg.SpoolDir = values["spool"]
	}
	cfg.HomeDir = values["home"]
	var err error
	if values["collect-interval"] != "" {
		cfg.CollectInterval, err = time.ParseDuration(values["collect-interval"])
		if err != nil {
			return cfg, fmt.Errorf("invalid collect interval")
		}
	}
	if values["upload-interval"] != "" {
		cfg.UploadInterval, err = time.ParseDuration(values["upload-interval"])
		if err != nil {
			return cfg, fmt.Errorf("invalid upload interval")
		}
	}
	if values["batch-size"] != "" {
		cfg.BatchSize, err = strconv.Atoi(values["batch-size"])
		if err != nil {
			return cfg, fmt.Errorf("invalid batch size")
		}
	}
	if values["spool-max-bytes"] != "" {
		cfg.SpoolMaxBytes, err = strconv.ParseInt(values["spool-max-bytes"], 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("invalid spool cap")
		}
	}
	if cfg.ServerURL == "" {
		return cfg, fmt.Errorf("collector server URL is required")
	}
	parsed, err := url.Parse(cfg.ServerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"))) {
		return cfg, fmt.Errorf("collector server must use HTTPS, except localhost")
	}
	if err := ingest.ValidateDeviceID(cfg.DeviceID); err != nil {
		return cfg, err
	}
	if cfg.TokenFile == "" {
		return cfg, fmt.Errorf("collector token file is required")
	}
	info, err := os.Stat(cfg.TokenFile)
	if err != nil {
		return cfg, fmt.Errorf("read collector token file: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return cfg, fmt.Errorf("collector token file permissions must be 0600")
	}
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return cfg, fmt.Errorf("collector token file is empty or unreadable")
	}
	cfg.Token = strings.TrimSpace(string(token))
	if cfg.CollectInterval <= 0 || cfg.UploadInterval <= 0 || cfg.BatchSize < 1 || cfg.BatchSize > ingest.MaxBatchEvents || cfg.SpoolMaxBytes < 1<<20 {
		return cfg, fmt.Errorf("collector intervals, batch size, or spool cap are invalid")
	}
	if err := os.MkdirAll(cfg.SpoolDir, 0700); err != nil {
		return cfg, fmt.Errorf("create spool: %w", err)
	}
	probe, err := os.CreateTemp(cfg.SpoolDir, ".write-check-")
	if err != nil {
		return cfg, fmt.Errorf("spool is not writable: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return cfg, nil
}
