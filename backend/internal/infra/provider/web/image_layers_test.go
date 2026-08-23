package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttptest "github.com/bogdanfinn/fhttp/httptest"

	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

func TestDetectImageLayersUploadsThenReturnsSegmentation(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	segmentBody := `{"cached":false,"map":{"objects":[{"name":"chair","boxXyxy":[12,40,400,380],"score":0.92,"maskRle":{"size":[1024,1024],"counts":"e3N"}}]}}`
	var uploaded bool
	server := fhttptest.NewServer(fhttp.HandlerFunc(func(writer fhttp.ResponseWriter, request *fhttp.Request) {
		switch request.URL.Path {
		case "/http/upload-file-v2/direct":
			if err := request.ParseMultipartForm(2 << 20); err != nil {
				t.Errorf("multipart: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.FormValue("file_source") != imagineSelfUploadSource {
				t.Errorf("file_source = %q", request.FormValue("file_source"))
			}
			uploaded = true
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"uploadId":"upload-1","fileMetadata":{"fileMetadataId":"file-1","fileUri":"users/test/reference/content"}}`))
		case "/rest/media/segment":
			if !uploaded {
				t.Error("segment called before upload")
			}
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("segment json: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload["assetId"] != "file-1" || payload["maskFormat"] != "rle" || payload["cachedOnly"] != false {
				t.Errorf("segment payload = %#v", payload)
			}
			if !strings.HasSuffix(request.Header.Get("Referer"), "/imagine") && !strings.Contains(request.Header.Get("Referer"), "/imagine") {
				t.Errorf("segment referer = %q", request.Header.Get("Referer"))
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(segmentBody))
		default:
			fhttp.NotFound(writer, request)
		}
	}))
	defer server.Close()

	adapter, credential := testMediaAdapter(t, server.URL)
	response, err := adapter.DetectImageLayers(context.Background(), provider.ImageLayerRequest{
		Credential: credential,
		Model:      "grok-imagine-image-2.0-web",
		ImageURLs:  []string{png},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s err=%v", response.StatusCode, body, readErr)
	}
	var parsed struct {
		Data []struct {
			Segmentation json.RawMessage `json:"segmentation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Data) != 1 || string(parsed.Data[0].Segmentation) != segmentBody {
		t.Fatalf("body=%s", body)
	}
}

func TestDetectImageLayersRejectsWrongImageCount(t *testing.T) {
	adapter, credential := testMediaAdapter(t, "http://127.0.0.1:1")
	response, err := adapter.DetectImageLayers(context.Background(), provider.ImageLayerRequest{
		Credential: credential,
		ImageURLs:  []string{"https://example.com/a.png", "https://example.com/b.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "恰好提供 1 张图片") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}
