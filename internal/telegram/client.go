package telegram

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// Client handles communication with the Telegram Bot API.
type Client struct {
	token      string
	apiURL     string
	fileURL    string
	httpClient *http.Client
}

// NewClient initializes a Telegram API client with optional custom API URL.
func NewClient(token, customAPIURL string) *Client {
	base := "https://api.telegram.org"
	if customAPIURL != "" {
		base = strings.TrimRight(customAPIURL, "/")
	}
	return &Client{
		token:   token,
		apiURL:  fmt.Sprintf("%s/bot%s", base, token),
		fileURL: fmt.Sprintf("%s/file/bot%s", base, token),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Request sends an API request to Telegram.
func (c *Client) Request(ctx context.Context, method string, params interface{}) (*APIResponse, error) {
	var bodyReader io.Reader
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL := fmt.Sprintf("%s/%s", c.apiURL, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode Telegram response (HTTP %d): %w", resp.StatusCode, err)
	}

	if !apiResp.Ok {
		return &apiResp, fmt.Errorf("telegram API error (%d): %s", apiResp.ErrorCode, apiResp.Description)
	}

	return &apiResp, nil
}

// GetUpdates polls Telegram for updates with the given offset and timeout.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	params := map[string]interface{}{
		"offset":  offset,
		"timeout": timeout,
	}

	resp, err := c.Request(ctx, "getUpdates", params)
	if err != nil {
		return nil, err
	}

	var updates []Update
	if err := json.Unmarshal(resp.Result, &updates); err != nil {
		return nil, err
	}

	return updates, nil
}

// SendMessage sends a text message to the specified chat.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, parseMode string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	params := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}

	resp, err := c.Request(ctx, "sendMessage", params)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendPhoto sends a photo with optional caption to the chat.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, photoURL string, caption string, parseMode string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	params := map[string]interface{}{
		"chat_id": chatID,
		"photo":   photoURL,
	}
	if caption != "" {
		params["caption"] = caption
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}

	resp, err := c.Request(ctx, "sendPhoto", params)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// EditMessageText edits the text and inline keyboard of an existing message.
func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int64, text string, parseMode string, replyMarkup *InlineKeyboardMarkup) error {
	params := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}

	_, err := c.Request(ctx, "editMessageText", params)
	return err
}

// EditMessageCaption edits the caption and inline keyboard of an existing photo/media message.
func (c *Client) EditMessageCaption(ctx context.Context, chatID int64, messageID int64, caption string, parseMode string, replyMarkup *InlineKeyboardMarkup) error {
	params := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"caption":    caption,
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}

	_, err := c.Request(ctx, "editMessageCaption", params)
	return err
}

// EditMessageReplyMarkup edits only the inline keyboard of an existing message.
func (c *Client) EditMessageReplyMarkup(ctx context.Context, chatID int64, messageID int64, replyMarkup *InlineKeyboardMarkup) error {
	params := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	if replyMarkup != nil {
		params["reply_markup"] = replyMarkup
	}
	_, err := c.Request(ctx, "editMessageReplyMarkup", params)
	return err
}

// DeleteMessage deletes a message from a chat.
func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID int64) error {
	params := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	_, err := c.Request(ctx, "deleteMessage", params)
	return err
}

// AnswerCallbackQuery acknowledges a callback query from an inline keyboard.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackQueryID string, text string, showAlert bool) error {
	params := map[string]interface{}{
		"callback_query_id": callbackQueryID,
	}
	if text != "" {
		params["text"] = text
	}
	if showAlert {
		params["show_alert"] = true
	}
	_, err := c.Request(ctx, "answerCallbackQuery", params)
	return err
}

// GetFile retrieves file metadata including its file_path from Telegram.
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	params := map[string]interface{}{
		"file_id": fileID,
	}
	resp, err := c.Request(ctx, "getFile", params)
	if err != nil {
		return nil, err
	}

	var f File
	if err := json.Unmarshal(resp.Result, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// DownloadFile downloads the binary content of a file from Telegram servers.
func (c *Client) DownloadFile(ctx context.Context, filePath string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s", c.fileURL, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	downloadClient := &http.Client{Timeout: 30 * time.Minute}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download file from Telegram (HTTP %d)", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// SendVideo uploads and sends a video file to the specified chat.
func (c *Client) SendVideo(ctx context.Context, chatID int64, fileName string, fileData []byte, caption string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	return c.sendMediaFile(ctx, "sendVideo", "video", chatID, fileName, fileData, caption, replyMarkup)
}

// SendAudio uploads and sends an audio file to the specified chat.
func (c *Client) SendAudio(ctx context.Context, chatID int64, fileName string, fileData []byte, caption string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	return c.sendMediaFile(ctx, "sendAudio", "audio", chatID, fileName, fileData, caption, replyMarkup)
}

// SendDocument uploads and sends a general file/document to the specified chat.
func (c *Client) SendDocument(ctx context.Context, chatID int64, fileName string, fileData []byte, caption string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	return c.sendMediaFile(ctx, "sendDocument", "document", chatID, fileName, fileData, caption, replyMarkup)
}

func (c *Client) sendMediaFile(ctx context.Context, endpoint, fieldName string, chatID int64, fileName string, fileData []byte, caption string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName))
	contentType := "application/octet-stream"
	if endpoint == "sendVideo" {
		contentType = "video/mp4"
	} else if endpoint == "sendAudio" {
		contentType = "audio/mpeg"
	}
	h.Set("Content-Type", contentType)

	part, err := writer.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, err
	}

	if err := writer.WriteField("chat_id", fmt.Sprintf("%d", chatID)); err != nil {
		return nil, err
	}
	if caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return nil, err
		}
	}
	if replyMarkup != nil {
		markupBytes, err := json.Marshal(replyMarkup)
		if err == nil {
			if err := writer.WriteField("reply_markup", string(markupBytes)); err != nil {
				return nil, err
			}
		}
	}
	if endpoint == "sendVideo" {
		_ = writer.WriteField("supports_streaming", "true")
		duration, width, height := parseMP4Metadata(fileData)
		if duration > 0 {
			_ = writer.WriteField("duration", fmt.Sprintf("%d", duration))
		}
		if width > 0 {
			_ = writer.WriteField("width", fmt.Sprintf("%d", width))
		}
		if height > 0 {
			_ = writer.WriteField("height", fmt.Sprintf("%d", height))
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/%s", c.apiURL, endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Uploading a large video (up to 2GB via local API or cloud) can take time
	uploadClient := &http.Client{Timeout: 30 * time.Minute}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !apiResp.Ok {
		return nil, fmt.Errorf("telegram API error (%d): %s", apiResp.ErrorCode, apiResp.Description)
	}

	var msg Message
	if err := json.Unmarshal(apiResp.Result, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// EscapeMarkdown escapes Telegram Markdown special characters.
func EscapeMarkdown(text string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"`", "\\`",
	)
	return replacer.Replace(text)
}

// parseMP4Metadata extracts video duration (in seconds), width, and height from MP4 container boxes.
func parseMP4Metadata(data []byte) (duration, width, height int) {
	if len(data) < 8 {
		return 0, 0, 0
	}

	findBox := func(buf []byte, target string) []byte {
		offset := 0
		for offset+8 <= len(buf) {
			size := int(binary.BigEndian.Uint32(buf[offset : offset+4]))
			name := string(buf[offset+4 : offset+8])
			boxEnd := 0
			if size == 1 {
				if offset+16 > len(buf) {
					break
				}
				size64 := int(binary.BigEndian.Uint64(buf[offset+8 : offset+16]))
				boxEnd = offset + size64
				if name == target && boxEnd <= len(buf) {
					return buf[offset+16 : boxEnd]
				}
				offset = boxEnd
			} else if size == 0 {
				boxEnd = len(buf)
				if name == target {
					return buf[offset+8 : boxEnd]
				}
				break
			} else {
				if size < 8 {
					break
				}
				boxEnd = offset + size
				if boxEnd > len(buf) {
					boxEnd = len(buf)
				}
				if name == target {
					return buf[offset+8 : boxEnd]
				}
				offset = boxEnd
			}
		}
		return nil
	}

	moov := findBox(data, "moov")
	if len(moov) == 0 {
		return 0, 0, 0
	}

	mvhd := findBox(moov, "mvhd")
	if len(mvhd) >= 24 {
		version := mvhd[0]
		if version == 0 {
			timescale := int(binary.BigEndian.Uint32(mvhd[12:16]))
			dur := int(binary.BigEndian.Uint32(mvhd[16:20]))
			if timescale > 0 {
				duration = dur / timescale
			}
		} else if version == 1 && len(mvhd) >= 32 {
			timescale := int(binary.BigEndian.Uint32(mvhd[20:24]))
			dur := int(binary.BigEndian.Uint64(mvhd[24:32]))
			if timescale > 0 {
				duration = dur / timescale
			}
		}
	}

	offset := 0
	for offset+8 <= len(moov) {
		size := int(binary.BigEndian.Uint32(moov[offset : offset+4]))
		name := string(moov[offset+4 : offset+8])
		if size < 8 || offset+size > len(moov) {
			break
		}
		if name == "trak" {
			trak := moov[offset+8 : offset+size]
			tkhd := findBox(trak, "tkhd")
			if len(tkhd) >= 84 {
				version := tkhd[0]
				var w, h int
				if version == 0 && len(tkhd) >= 84 {
					w = int(binary.BigEndian.Uint32(tkhd[76:80]) >> 16)
					h = int(binary.BigEndian.Uint32(tkhd[80:84]) >> 16)
				} else if version == 1 && len(tkhd) >= 96 {
					w = int(binary.BigEndian.Uint32(tkhd[88:92]) >> 16)
					h = int(binary.BigEndian.Uint32(tkhd[92:96]) >> 16)
				}
				if w > 0 && h > 0 {
					width = w
					height = h
					break
				}
			}
		}
		offset += size
	}

	return duration, width, height
}
