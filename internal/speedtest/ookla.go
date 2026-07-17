package speedtest

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Ookla engine: execs the official `speedtest` CLI. The binary is NOT
// bundled in the image (Ookla EULA); with OOKLA_ACCEPT_EULA=true it is
// downloaded to the data volume on first use.
type Ookla struct {
	dataDir    string
	acceptEULA bool
	log        *slog.Logger
}

func NewOokla(dataDir string, acceptEULA bool, log *slog.Logger) *Ookla {
	return &Ookla{dataDir: dataDir, acceptEULA: acceptEULA, log: log.With("engine", "ookla")}
}

func (o *Ookla) Name() string { return "ookla" }

func (o *Ookla) binPath() string { return filepath.Join(o.dataDir, "bin", "speedtest") }

func (o *Ookla) Run(ctx context.Context) (*Result, error) {
	bin, err := o.ensureBinary(ctx)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	cmd := exec.CommandContext(ctx, bin, "--format=json", "--accept-license", "--accept-gdpr")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("ookla: speedtest cli: %s", string(ee.Stderr))
		}
		return nil, fmt.Errorf("ookla: speedtest cli: %w", err)
	}

	var parsed struct {
		Ping struct {
			Latency float64 `json:"latency"`
		} `json:"ping"`
		Download struct {
			Bandwidth float64 `json:"bandwidth"` // bytes/sec
			Latency   struct {
				IQM float64 `json:"iqm"`
			} `json:"latency"`
		} `json:"download"`
		Upload struct {
			Bandwidth float64 `json:"bandwidth"`
		} `json:"upload"`
		PacketLoss float64 `json:"packetLoss"`
		Server     struct {
			Name string `json:"name"`
			ID   int    `json:"id"`
		} `json:"server"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("ookla: parse output: %w", err)
	}
	return &Result{
		Engine:          o.Name(),
		ServerName:      parsed.Server.Name,
		ServerID:        fmt.Sprint(parsed.Server.ID),
		DownloadBps:     parsed.Download.Bandwidth * 8,
		UploadBps:       parsed.Upload.Bandwidth * 8,
		LatencyMs:       parsed.Ping.Latency,
		LoadedLatencyMs: parsed.Download.Latency.IQM,
		PacketLoss:      parsed.PacketLoss,
		RanAt:           started,
		DurationMs:      time.Since(started).Milliseconds(),
	}, nil
}

// ensureBinary returns the CLI path, downloading it on first use if the
// EULA flag allows.
func (o *Ookla) ensureBinary(ctx context.Context) (string, error) {
	if p, err := exec.LookPath("speedtest"); err == nil {
		return p, nil
	}
	bin := o.binPath()
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if !o.acceptEULA {
		return "", fmt.Errorf("ookla engine requires the Ookla speedtest CLI, which is not bundled due to its EULA. " +
			"Set OOKLA_ACCEPT_EULA=true to download it to the data volume (this accepts Ookla's license: " +
			"https://www.speedtest.net/about/eula), or switch speedtest.engine to librespeed/cloudflare")
	}

	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("ookla: unsupported architecture %s", arch)
	}
	url := fmt.Sprintf("https://install.speedtest.net/app/cli/ookla-speedtest-1.2.0-linux-%s.tgz", arch)
	o.log.Warn("downloading Ookla speedtest CLI: by setting OOKLA_ACCEPT_EULA=true you accepted the Ookla EULA "+
		"(https://www.speedtest.net/about/eula)", "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("ookla: download cli: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ookla: download cli: http %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ookla: gunzip: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("ookla: speedtest binary not found in archive")
		}
		if err != nil {
			return "", fmt.Errorf("ookla: untar: %w", err)
		}
		if filepath.Base(hdr.Name) != "speedtest" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			return "", err
		}
		f, err := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return "", fmt.Errorf("ookla: write binary: %w", err)
		}
		f.Close()
		o.log.Info("ookla speedtest cli installed", "path", bin)
		return bin, nil
	}
}
