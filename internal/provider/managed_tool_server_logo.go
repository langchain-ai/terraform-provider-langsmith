package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // registers JPEG for image.Decode
	_ "image/png"  // registers PNG for image.Decode
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go/option"
)

// The limits LangSmith applies to uploaded server logos, checked at plan time
// so a bad file fails before the server is created.
const (
	maxServerLogoBytes  = 1 << 20
	maxServerLogoPixels = 2048
)

// readServerLogo reads the logo file at path and returns its bytes and
// SHA-256, or an error describing why LangSmith would reject it.
func readServerLogo(path string) ([]byte, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Size() > maxServerLogoBytes {
		return nil, "", fmt.Errorf("%s is %d bytes; logos must be at most 1 MiB", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if kind := http.DetectContentType(data); kind != "image/png" && kind != "image/jpeg" {
		return nil, "", fmt.Errorf("%s is %s; logos must be PNG or JPEG", path, kind)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", path, err)
	}
	if config.Width > maxServerLogoPixels || config.Height > maxServerLogoPixels {
		return nil, "", fmt.Errorf("%s is %dx%d; logos must be at most %dx%d pixels", path, config.Width, config.Height, maxServerLogoPixels, maxServerLogoPixels)
	}
	// LangSmith decodes every pixel, so a file whose header is intact but whose
	// image data is cut off must fail here rather than after the server exists.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func managedToolServerLogoPath(serverID string) string {
	return managedToolServersPath + "/" + url.PathEscape(serverID) + "/icon"
}

// putServerLogo uploads the logo at logoPath for a server, failing if the file
// no longer matches the SHA-256 Terraform planned.
func (r *managedToolServerResource) putServerLogo(ctx context.Context, workspaceID types.String, serverID, logoPath, plannedSHA256 string) error {
	data, sum, err := readServerLogo(logoPath)
	if err != nil {
		return err
	}
	if sum != plannedSHA256 {
		return fmt.Errorf("%s changed after planning; run terraform apply again", logoPath)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filepath.Base(logoPath)))
	header.Set("Content-Type", http.DetectContentType(data))
	part, err := form.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	opts := append(workspaceOpts(workspaceID), option.WithRequestBody(form.FormDataContentType(), body.Bytes()))
	return r.client.Put(ctx, managedToolServerLogoPath(serverID), nil, nil, opts...)
}

func (r *managedToolServerResource) deleteServerLogo(ctx context.Context, workspaceID types.String, serverID string) error {
	err := r.client.Delete(ctx, managedToolServerLogoPath(serverID), nil, nil, workspaceOpts(workspaceID)...)
	if isLangSmithNotFound(err) {
		return nil
	}
	return err
}
