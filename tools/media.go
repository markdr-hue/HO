/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// MediaTool — manage_media
// ---------------------------------------------------------------------------

const maxMediaInputSize = 10 * 1024 * 1024 // 10MB

// MediaTool provides image resize, thumbnail, and optimization operations.
type MediaTool struct{}

func (t *MediaTool) Name() string { return "manage_media" }
func (t *MediaTool) Description() string {
	return "Resize, thumbnail, or optimize images (JPEG/PNG)."
}

func (t *MediaTool) Guide() string {
	return `### Media Processing (manage_media)
- **resize**: Resize an image. Params: source (filename), width (px), height (px, optional — maintains aspect ratio if omitted), output (optional filename), storage ("assets"|"files", default "files").
- **thumbnail**: Generate square thumbnail. Params: source, size (px, default 150), storage.
- **optimize**: Re-encode image at lower quality. Params: source, quality (1-100, default 75), storage. PNG inputs are converted to JPEG.
Only JPEG and PNG supported. Max input size: 10MB.`
}

func (t *MediaTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"resize", "thumbnail", "optimize"},
				"description": "Action to perform",
			},
			"source": map[string]interface{}{
				"type":        "string",
				"description": "Source image filename (must exist in assets or files storage).",
			},
			"storage": map[string]interface{}{
				"type":        "string",
				"description": "Storage bucket: 'assets' or 'files' (default 'files').",
			},
			"width": map[string]interface{}{
				"type":        "number",
				"description": "Target width in pixels. For resize.",
			},
			"height": map[string]interface{}{
				"type":        "number",
				"description": "Target height in pixels (optional — maintains aspect ratio if omitted). For resize.",
			},
			"output": map[string]interface{}{
				"type":        "string",
				"description": "Output filename (optional — auto-generated if omitted).",
			},
			"size": map[string]interface{}{
				"type":        "number",
				"description": "Square thumbnail size in pixels (default 150). For thumbnail.",
			},
			"quality": map[string]interface{}{
				"type":        "number",
				"description": "JPEG quality 1-100 (default 75). For optimize.",
			},
		},
		"required": []string{},
	}
}

func (t *MediaTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"resize":    t.resize,
		"thumbnail": t.thumbnail,
		"optimize":  t.optimize,
	}, nil)
}

// mediaDirName maps user-facing storage names to on-disk directory names.
func mediaDirName(storage string) string {
	if storage == "assets" {
		return "ho_assets"
	}
	return "ho_files"
}

// loadImage reads and decodes an image from storage, returning the image and its format.
func (t *MediaTool) loadImage(siteID int, storage, filename string) (image.Image, string, error) {
	dir, _ := storageDir(siteID, mediaDirName(storage))
	path := filepath.Join(dir, filename)

	info, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("file not found: %s", filename)
	}
	if info.Size() > maxMediaInputSize {
		return nil, "", fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), maxMediaInputSize)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", fmt.Errorf("decoding image (only JPEG/PNG supported): %w", err)
	}

	return img, format, nil
}

// saveImage encodes and writes an image to storage, returning the output path.
func (t *MediaTool) saveImage(siteID int, storage, filename string, img image.Image, format string, quality int) (string, int64, error) {
	dir, _ := storageDir(siteID, mediaDirName(storage))
	os.MkdirAll(dir, 0755)
	outPath := filepath.Join(dir, filename)

	f, err := os.Create(outPath)
	if err != nil {
		return "", 0, fmt.Errorf("creating output file: %w", err)
	}
	defer f.Close()

	switch format {
	case "jpeg":
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
	case "png":
		err = png.Encode(f, img)
	default:
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
	}
	if err != nil {
		return "", 0, fmt.Errorf("encoding image: %w", err)
	}

	info, _ := os.Stat(outPath)
	var size int64
	if info != nil {
		size = info.Size()
	}

	return outPath, size, nil
}

// registerOutput inserts the output image into the assets/files table.
func (t *MediaTool) registerOutput(ctx *ToolContext, storage, filename, contentType string, size int64) {
	table := "ho_files"
	if storage == "assets" {
		table = "ho_assets"
	}
	ctx.DB.Exec(
		fmt.Sprintf(`INSERT INTO %s (filename, content_type, size, scope, created_at)
		 VALUES (?, ?, ?, 'page', CURRENT_TIMESTAMP)
		 ON CONFLICT(filename) DO UPDATE SET content_type=excluded.content_type, size=excluded.size`, table),
		filename, contentType, size,
	)
}

// resizeImage scales an image to fit within target dimensions using nearest-neighbor.
func resizeImage(src image.Image, targetW, targetH int) image.Image {
	srcBounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.Draw(dst, dst.Bounds(), image.Transparent, image.Point{}, draw.Src)

	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	for y := 0; y < targetH; y++ {
		srcY := y * srcH / targetH
		for x := 0; x < targetW; x++ {
			srcX := x * srcW / targetW
			dst.Set(x, y, src.At(srcBounds.Min.X+srcX, srcBounds.Min.Y+srcY))
		}
	}

	return dst
}

// cropCenter extracts a centered square crop from an image.
func cropCenter(src image.Image, size int) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Determine crop rectangle.
	cropSize := w
	if h < cropSize {
		cropSize = h
	}

	x0 := bounds.Min.X + (w-cropSize)/2
	y0 := bounds.Min.Y + (h-cropSize)/2

	dst := image.NewRGBA(image.Rect(0, 0, cropSize, cropSize))
	draw.Draw(dst, dst.Bounds(), src, image.Point{X: x0, Y: y0}, draw.Src)

	// Then resize to target size.
	if cropSize != size {
		return resizeImage(dst, size, size)
	}
	return dst
}

func (t *MediaTool) resize(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	source, errResult := RequireString(args, "source")
	if errResult != nil {
		return errResult, nil
	}

	width := OptionalInt(args, "width", 0)
	if width <= 0 {
		return &Result{Success: false, Error: "width is required and must be positive"}, nil
	}

	height := OptionalInt(args, "height", 0)
	storage := OptionalString(args, "storage", "files")
	output := OptionalString(args, "output", "")

	img, format, err := t.loadImage(ctx.SiteID, storage, source)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Calculate height maintaining aspect ratio if not specified.
	srcBounds := img.Bounds()
	if height <= 0 {
		height = int(float64(width) * float64(srcBounds.Dy()) / float64(srcBounds.Dx()))
		if height < 1 {
			height = 1
		}
	}

	resized := resizeImage(img, width, height)

	// Generate output filename.
	if output == "" {
		ext := filepath.Ext(source)
		name := strings.TrimSuffix(source, ext)
		output = fmt.Sprintf("%s_%dx%d%s", name, width, height, ext)
	}

	contentType := "image/jpeg"
	if format == "png" {
		contentType = "image/png"
	}

	_, size, err := t.saveImage(ctx.SiteID, storage, output, resized, format, 85)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	t.registerOutput(ctx, storage, output, contentType, size)

	urlPrefix := "/files/"
	if storage == "assets" {
		urlPrefix = "/assets/"
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"filename": output,
		"width":    width,
		"height":   height,
		"size":     size,
		"url":      urlPrefix + output,
	}}, nil
}

func (t *MediaTool) thumbnail(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	source, errResult := RequireString(args, "source")
	if errResult != nil {
		return errResult, nil
	}

	size := OptionalInt(args, "size", 150)
	if size <= 0 {
		size = 150
	}
	storage := OptionalString(args, "storage", "files")

	img, _, err := t.loadImage(ctx.SiteID, storage, source)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	thumb := cropCenter(img, size)

	ext := filepath.Ext(source)
	name := strings.TrimSuffix(source, ext)
	output := fmt.Sprintf("%s_thumb%s", name, ext)

	format := "jpeg"
	contentType := "image/jpeg"
	if strings.ToLower(ext) == ".png" {
		format = "png"
		contentType = "image/png"
	}

	_, fileSize, err := t.saveImage(ctx.SiteID, storage, output, thumb, format, 85)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	t.registerOutput(ctx, storage, output, contentType, fileSize)

	urlPrefix := "/files/"
	if storage == "assets" {
		urlPrefix = "/assets/"
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"filename": output,
		"size":     size,
		"bytes":    fileSize,
		"url":      urlPrefix + output,
	}}, nil
}

func (t *MediaTool) optimize(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	source, errResult := RequireString(args, "source")
	if errResult != nil {
		return errResult, nil
	}

	quality := OptionalInt(args, "quality", 75)
	if quality < 1 {
		quality = 1
	}
	if quality > 100 {
		quality = 100
	}
	storage := OptionalString(args, "storage", "files")

	img, _, err := t.loadImage(ctx.SiteID, storage, source)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Always output as JPEG for optimization.
	ext := filepath.Ext(source)
	name := strings.TrimSuffix(source, ext)
	output := name + "_opt.jpg"

	_, fileSize, err := t.saveImage(ctx.SiteID, storage, output, img, "jpeg", quality)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	t.registerOutput(ctx, storage, output, "image/jpeg", fileSize)

	urlPrefix := "/files/"
	if storage == "assets" {
		urlPrefix = "/assets/"
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"filename": output,
		"quality":  quality,
		"bytes":    fileSize,
		"url":      urlPrefix + output,
	}}, nil
}
