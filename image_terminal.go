package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
)

// detectTerminalImageSupport returns true if the terminal appears to support inline images (iTerm2, WezTerm, Kitty, etc.)
func detectTerminalImageSupport() bool {
    // Robust detection for popular terminals that support inline image display
    // - iTerm2: ITERM_SESSION_ID or TERM_PROGRAM contains "iTerm"
    // - WezTerm: TERM_PROGRAM=wezterm or TERM contains "wezterm"
    // - Kitty: TERM=xterm-kitty or TERM contains "kitty"
    // - Alacritty: TERM=alacritty (recent versions support inline via iTerm2 protocol)
    // - Windows Terminal: wt (typically runs shells; detect via TERM_PROGRAM=WindowsTerminal)
    // - Konsole (KDE): TERM_PROGRAM=konsole; some versions support Sixel but we avoid fallbacks here

    term := os.Getenv("TERM")
    termProg := os.Getenv("TERM_PROGRAM")
    itermSession := os.Getenv("ITERM_SESSION_ID")

    if itermSession != "" || strings.Contains(strings.ToLower(termProg), "iterm") {
        return true
    }
    if strings.Contains(strings.ToLower(termProg), "wezterm") || strings.Contains(strings.ToLower(term), "wezterm") {
        return true
    }
    if strings.Contains(strings.ToLower(term), "kitty") {
        return true
    }
    if strings.Contains(strings.ToLower(term), "alacritty") {
        // Recent Alacritty supports iTerm2 inline images protocol; enable
        return true
    }
    if strings.Contains(strings.ToLower(termProg), "windowsterminal") {
        // Windows Terminal 1.22+ supports iTerm2 inline images protocol on some backends; enable
        return true
    }
    if strings.Contains(strings.ToLower(termProg), "konsole") {
        // Konsole does not reliably support inline base64 images; keep disabled to avoid garbled output
        return false
    }
    return false
}

// displayImageInTerminal decodes a data URL image, resizes it to fit within maxHeight while preserving aspect ratio,
// then prints it inline for terminals supporting inline images (iTerm2 style).
func displayImageInTerminal(dataURL string, maxHeight int) error {
	// Parse data URL: data:<mime>;base64,<base64>
	if !strings.HasPrefix(dataURL, "data:image/") {
		return fmt.Errorf("unsupported data URL format")
	}
	// Extract mime and base64
	semi := strings.Index(dataURL, ";")
	if semi == -1 {
		return fmt.Errorf("malformed data URL")
	}
	// mime := dataURL[5:semi] // not needed; we decode bytes directly
	comma := strings.Index(dataURL[semi:], ",")
	if comma == -1 {
		return fmt.Errorf("malformed data URL")
	}
	base64Data := dataURL[semi+comma+1:]
	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("base64 decode error: %w", err)
	}

	// Decode image
	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		return fmt.Errorf("image decode error: %w", err)
	}

	// Resize if necessary
	bounds := img.Bounds()
	height := bounds.Dy()
	if height > maxHeight {
		width := bounds.Dx()
		ratio := float64(width) / float64(height)
		newHeight := maxHeight
		newWidth := int(float64(newHeight) * ratio)

		newImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		// Use a simple scaler (nearest neighbor) to avoid heavy dependencies
		for y := 0; y < newHeight; y++ {
			for x := 0; x < newWidth; x++ {
				sx := int(float64(x) * float64(width) / float64(newWidth))
				sy := int(float64(y) * float64(height) / float64(newHeight))
				newImg.Set(x, y, img.At(sx, sy))
			}
		}
		img = newImg
	}

	// Encode to PNG (stable for inline)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return fmt.Errorf("png encode error: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	// Print inline for iTerm2 (OSC 1337)
	// Note: We write to stdout so it appears in the terminal.
	_, err = fmt.Fprintf(os.Stdout, "\033]1337;File=name=%s;size=%d;inline=1:%s\a\n", "preview.png", len(encoded), encoded)
	return err
}
