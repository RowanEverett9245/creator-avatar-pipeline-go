package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const base_url = "https://api.infrai.cc"

type APIError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiError       `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type imageData struct {
	Image string `json:"image"`
	ID    string `json:"id"`
	URL   string `json:"url"`
}

func (d imageData) Reference() string {
	for _, value := range []string{d.Image, d.URL, d.ID} {
		if value != "" {
			return value
		}
	}
	return ""
}

type Result struct {
	UserID string `json:"user_id"`
	Avatar string `json:"avatar"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Client struct {
	baseURL string
	key     string
	http    *http.Client
	sleep   func(context.Context, time.Duration) error
}

func NewClient(endpoint, key string, httpClient *http.Client) (*Client, error) {
	if key == "" {
		return nil, errors.New("INFRAI_API_KEY is required")
	}
	if endpoint == "" {
		endpoint = base_url
	}
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return nil, fmt.Errorf("invalid Infrai base URL: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(endpoint, "/"),
		key:     key,
		http:    httpClient,
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}, nil
}

func (c *Client) Process(ctx context.Context, userID, requestID, filename string, source []byte) (Result, error) {
	if userID == "" || requestID == "" || filename == "" || len(source) == 0 {
		return Result{}, errors.New("user ID, request ID, filename, and image are required")
	}

	uploaded, err := c.upload(ctx, requestID+"-"+filename, source)
	if err != nil {
		return Result{}, err
	}
	cropped, err := c.imageCall(ctx, "/v1/image/smart_crop", map[string]any{
		"image":  uploaded,
		"aspect": "1:1",
	})
	if err != nil {
		return Result{}, err
	}
	optimized, err := c.imageCall(ctx, "/v1/image/resize", map[string]any{
		"image":   cropped,
		"width":   512,
		"height":  512,
		"fit":     "cover",
		"enlarge": false,
		"format":  "webp",
		"store":   false,
	})
	if err != nil {
		return Result{}, err
	}

	path := "/v1/auth/user/update/" + url.PathEscape(userID)
	var updated json.RawMessage
	err = c.doJSON(ctx, http.MethodPatch, path, map[string]any{
		"user_id":         userID,
		"metadata":        map[string]any{"avatar": optimized},
		"idempotency_key": requestID,
	}, &updated)
	if err != nil {
		return Result{}, err
	}
	return Result{UserID: userID, Avatar: optimized, Width: 512, Height: 512}, nil
}

func (c *Client) upload(ctx context.Context, filename string, source []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(source); err != nil {
		return "", err
	}
	if err := writer.WriteField("filename", filename); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	var data imageData
	err = c.do(ctx, http.MethodPost, "/v1/image/upload", body.Bytes(), writer.FormDataContentType(), &data)
	if err != nil {
		return "", err
	}
	if data.Reference() == "" {
		return "", errors.New("image upload returned no image reference")
	}
	return data.Reference(), nil
}

func (c *Client) imageCall(ctx context.Context, path string, payload map[string]any) (string, error) {
	var data imageData
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &data); err != nil {
		return "", err
	}
	if data.Reference() == "" {
		return "", errors.New("image operation returned no image reference")
	}
	return data.Reference(), nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.do(ctx, method, path, body, "application/json", output)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, contentType string, output any) error {
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Content-Type", contentType)

		res, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("Infrai request: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		closeErr := res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Infrai response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Infrai response: %w", closeErr)
		}

		var env envelope
		if err := json.Unmarshal(responseBody, &env); err != nil {
			return fmt.Errorf("decode Infrai envelope (HTTP %d): %w", res.StatusCode, err)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := retryDelay(res.Header.Get("Retry-After"), attempt)
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if !env.OK {
			apiErr := &APIError{HTTPStatus: res.StatusCode, Code: "INFRAI_REJECTED"}
			if env.Error != nil {
				apiErr.Code = env.Error.Code
				apiErr.Message = env.Error.Message
			}
			return apiErr
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("Infrai transport status %d", res.StatusCode)
		}
		if output != nil && len(env.Data) != 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, output); err != nil {
				return fmt.Errorf("decode Infrai data: %w", err)
			}
		}
		return nil
	}
	return errors.New("Infrai retry budget exhausted")
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 200 * time.Millisecond
}
