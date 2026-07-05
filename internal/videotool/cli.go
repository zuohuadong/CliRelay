package videotool

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

type CLI struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

func (c CLI) RunVideo(args []string) error {
	return c.run(args, false)
}

func (c CLI) RunTools(args []string) error {
	return c.run(args, true)
}

func (c CLI) run(args []string, toolsMode bool) error {
	c = c.withDefaults()
	if len(args) == 0 {
		c.usage(toolsMode)
		return nil
	}
	if toolsMode && args[0] == "video" {
		if len(args) == 1 {
			c.videoUsage("clirelay-tools video", false)
			return nil
		}
		return c.runVideoCommand(args[1:], "clirelay-tools video")
	}
	switch args[0] {
	case "mcp":
		return c.runMCP(args[1:])
	case "models":
		if toolsMode {
			return fmt.Errorf("unknown command %q; use \"video models\"", args[0])
		}
		return c.runModels(args[1:])
	case "create":
		if toolsMode {
			return fmt.Errorf("unknown command %q; use \"video create\"", args[0])
		}
		return c.runCreate(args[1:])
	case "status":
		if toolsMode {
			return fmt.Errorf("unknown command %q; use \"video status\"", args[0])
		}
		return c.runStatus(args[1:])
	case "download":
		if toolsMode {
			return fmt.Errorf("unknown command %q; use \"video download\"", args[0])
		}
		return c.runDownload(args[1:])
	case "-h", "--help", "help":
		c.usage(toolsMode)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (c CLI) runVideoCommand(args []string, usagePrefix string) error {
	switch args[0] {
	case "models":
		return c.runModels(args[1:])
	case "create":
		return c.runCreate(args[1:])
	case "status":
		return c.runStatus(args[1:])
	case "download":
		return c.runDownload(args[1:])
	case "-h", "--help", "help":
		c.videoUsage(usagePrefix, false)
		return nil
	default:
		return fmt.Errorf("unknown video command %q", args[0])
	}
}

func (c CLI) runMCP(args []string) error {
	fs, opts := commonFlagSet("mcp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := NewClient(*opts)
	return (&MCPServer{Client: client, In: c.In, Out: c.Out}).Run(context.Background())
}

func (c CLI) runModels(args []string) error {
	fs, opts := commonFlagSet("models")
	if err := fs.Parse(args); err != nil {
		return err
	}
	models, err := NewClient(*opts).ListVideoModels(context.Background())
	if err != nil {
		return err
	}
	return c.printJSON(models)
}

func (c CLI) runCreate(args []string) error {
	fs, opts := commonFlagSet("create")
	prompt := fs.String("prompt", "", "Video prompt")
	seconds := fs.Int("seconds", 0, "Video duration in seconds")
	size := fs.String("size", "", "Video size, for example 720x1280")
	aspectRatio := fs.String("aspect-ratio", "", "Aspect ratio, for example 9:16")
	resolution := fs.String("resolution", "", "Resolution, for example 720p")
	wait := fs.Bool("wait", false, "Poll until the task is complete or failed")
	timeout := fs.Duration("timeout", 10*time.Minute, "Maximum wait time")
	pollInterval := fs.Duration("poll-interval", 5*time.Second, "Polling interval")
	outputPath := fs.String("output", "", "Download completed video to this path when used with -wait")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *prompt == "" && fs.NArg() > 0 {
		*prompt = fs.Arg(0)
	}
	client := NewClient(*opts)
	out, err := client.CreateVideo(context.Background(), CreateVideoRequest{
		Model:       opts.Model,
		Prompt:      *prompt,
		Seconds:     *seconds,
		Size:        *size,
		AspectRatio: *aspectRatio,
		Resolution:  *resolution,
	})
	if err != nil {
		return err
	}
	if *wait {
		videoID := VideoID(out)
		if videoID == "" {
			return fmt.Errorf("create response did not include a video id")
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		out, err = client.WaitVideo(ctx, videoID, *pollInterval)
		if err != nil {
			_ = c.printJSON(out)
			return err
		}
		if *outputPath != "" {
			download, err := client.DownloadVideo(context.Background(), videoID, *outputPath)
			if err != nil {
				return err
			}
			out["download"] = download
		}
	}
	return c.printJSON(out)
}

func (c CLI) runStatus(args []string) error {
	fs, opts := commonFlagSet("status")
	videoID := fs.String("id", "", "Video id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *videoID == "" && fs.NArg() > 0 {
		*videoID = fs.Arg(0)
	}
	out, err := NewClient(*opts).GetVideo(context.Background(), *videoID)
	if err != nil {
		return err
	}
	return c.printJSON(out)
}

func (c CLI) runDownload(args []string) error {
	fs, opts := commonFlagSet("download")
	videoID := fs.String("id", "", "Video id")
	outputPath := fs.String("output", "", "Output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *videoID == "" && fs.NArg() > 0 {
		*videoID = fs.Arg(0)
	}
	out, err := NewClient(*opts).DownloadVideo(context.Background(), *videoID, *outputPath)
	if err != nil {
		return err
	}
	return c.printJSON(out)
}

func commonFlagSet(name string) (*flag.FlagSet, *Options) {
	opts := OptionsFromEnv()
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&opts.BaseURL, "base-url", firstNonEmpty(opts.BaseURL, DefaultBaseURL), "CliRelay root base URL")
	fs.StringVar(&opts.APIKey, "api-key", opts.APIKey, "CliRelay API key; defaults to CLIRELAY_API_KEY")
	fs.StringVar(&opts.Model, "model", firstNonEmpty(opts.Model, DefaultModel), "Default video model")
	return fs, &opts
}

func (c CLI) printJSON(value any) error {
	encoder := json.NewEncoder(c.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (c CLI) usage(toolsMode bool) {
	if toolsMode {
		_, _ = fmt.Fprintln(c.ErrOut, `Usage:
  clirelay-tools mcp [flags]
  clirelay-tools video models [flags]
  clirelay-tools video create -prompt "..." [-wait] [-output out.mp4] [flags]
  clirelay-tools video status -id <video_id> [flags]
  clirelay-tools video download -id <video_id> [-output out.mp4] [flags]

Common flags:
  -base-url   CliRelay root URL, default CLIRELAY_BASE_URL or http://127.0.0.1:8317
  -api-key    CliRelay API key, default CLIRELAY_API_KEY
  -model      Video model, default CLIRELAY_VIDEO_MODEL or agnes-video-v2.0`)
		return
	}
	c.videoUsage("clirelay-video", true)
}

func (c CLI) videoUsage(prefix string, includeMCP bool) {
	if includeMCP {
		_, _ = fmt.Fprintf(c.ErrOut, `Usage:
  %[1]s mcp [flags]
  %[1]s models [flags]
  %[1]s create -prompt "..." [-wait] [-output out.mp4] [flags]
  %[1]s status -id <video_id> [flags]
  %[1]s download -id <video_id> [-output out.mp4] [flags]

Common flags:
  -base-url   CliRelay root URL, default CLIRELAY_BASE_URL or http://127.0.0.1:8317
  -api-key    CliRelay API key, default CLIRELAY_API_KEY
  -model      Video model, default CLIRELAY_VIDEO_MODEL or agnes-video-v2.0
`, prefix)
		return
	}
	_, _ = fmt.Fprintf(c.ErrOut, `Usage:
  %[1]s models [flags]
  %[1]s create -prompt "..." [-wait] [-output out.mp4] [flags]
  %[1]s status -id <video_id> [flags]
  %[1]s download -id <video_id> [-output out.mp4] [flags]

Common flags:
  -base-url   CliRelay root URL, default CLIRELAY_BASE_URL or http://127.0.0.1:8317
  -api-key    CliRelay API key, default CLIRELAY_API_KEY
  -model      Video model, default CLIRELAY_VIDEO_MODEL or agnes-video-v2.0
`, prefix)
}

func (c CLI) withDefaults() CLI {
	if c.In == nil {
		c.In = os.Stdin
	}
	if c.Out == nil {
		c.Out = os.Stdout
	}
	if c.ErrOut == nil {
		c.ErrOut = os.Stderr
	}
	return c
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
