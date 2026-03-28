package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"
)

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func saveUploadedFile(file multipart.File, header *multipart.FileHeader) (string, error) {
	ext := strings.ToLower(filepath.Ext(header.Filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
	default:
		return "", errors.New("unsupported image type")
	}

	filename := uuid.NewString() + ext
	path := filepath.Join(uploadsDir, filename)
	dst, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		return "", err
	}
	return filename, nil
}

func resolveImageFiles(imageIDs []string) ([]string, []string, error) {
	urls := make([]string, 0, len(imageIDs))
	paths := make([]string, 0, len(imageIDs))
	for _, id := range imageIDs {
		id = filepath.Base(strings.TrimSpace(id))
		if id == "." || id == "" {
			continue
		}
		path := filepath.Join(uploadsDir, id)
		if _, err := os.Stat(path); err != nil {
			return nil, nil, fmt.Errorf("image not found: %s", id)
		}
		urls = append(urls, "/uploads/"+id)
		paths = append(paths, path)
	}
	return urls, paths, nil
}

func detectCodexModel() string {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml"))
	if err != nil {
		return "unknown"
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "model") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, "\"'")
		if value != "" {
			return value
		}
	}

	return "unknown"
}

func listInstalledSkills() ([]skillInfo, error) {
	root := filepath.Join(os.Getenv("HOME"), ".codex", "skills")
	items := make([]skillInfo, 0, 16)
	seen := make(map[string]bool)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "SKILL.md" {
			return nil
		}
		name, description, parseErr := parseSkillFrontmatter(path)
		if parseErr != nil {
			return nil
		}
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		items = append(items, skillInfo{
			Name:        name,
			Description: description,
			Path:        path,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func parseSkillFrontmatter(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("missing frontmatter")
	}
	var name, description string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		if strings.HasPrefix(line, "name:") {
			name = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), "\"'")
		}
		if strings.HasPrefix(line, "description:") {
			description = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), "\"'")
		}
	}
	return name, description, nil
}

func detectTaskConcurrency() int {
	cpus := runtime.NumCPU()
	switch {
	case cpus <= 2:
		return 1
	case cpus <= 4:
		return 2
	default:
		return cpus / 2
	}
}

func mergeMaps(base map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func shouldRetryFallback(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "approval") ||
		strings.Contains(text, "unknown variant") ||
		strings.Contains(text, "expected one of") ||
		strings.Contains(text, "sandbox") ||
		strings.Contains(text, "on-request") ||
		strings.Contains(text, "onrequest")
}

func rpcCallError(method string, rpcErr *rpcError) error {
	if rpcErr == nil {
		return fmt.Errorf("%s failed", method)
	}
	return fmt.Errorf("%s failed: %s", method, strings.TrimSpace(rpcErr.Message))
}

func packetID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		return num.String()
	}
	return ""
}

func mustMarshalJSON(v interface{}) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func stringField(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			if text, ok := value.(string); ok {
				return text
			}
		}
	}
	return ""
}

func intField(m map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		}
	}
	return 0, false
}

func normalizeItemType(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func extractAppServerErrorMessage(raw json.RawMessage, payload notificationEnvelope) string {
	if msg := strings.TrimSpace(payload.Message); msg != "" {
		return msg
	}

	var envelope map[string]interface{}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if msg := firstNonEmpty(
			lookupNestedString(envelope, "message"),
			lookupNestedString(envelope, "error.message"),
			lookupNestedString(envelope, "error.details"),
			lookupNestedString(envelope, "details"),
			lookupNestedString(envelope, "additionalDetails"),
			lookupNestedString(envelope, "error.additionalDetails"),
		); strings.TrimSpace(msg) != "" {
			return strings.TrimSpace(msg)
		}
	}

	return strings.TrimSpace(string(raw))
}

func lookupNestedString(data map[string]interface{}, path string) string {
	current := interface{}(data)
	for _, part := range strings.Split(path, ".") {
		node, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current, ok = node[part]
		if !ok {
			return ""
		}
	}
	if text, ok := current.(string); ok {
		return text
	}
	return ""
}

func detectServiceTier() string {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "service_tier") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		if value != "" {
			return value
		}
	}
	return ""
}

func compactForSummary(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= 72 {
		return text
	}
	return text[:72]
}

func normalizeWorkdir(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultWorkdir
	}
	if !filepath.IsAbs(value) {
		return defaultWorkdir
	}
	return filepath.Clean(value)
}

func validateWorkdir(value string) (string, error) {
	workdir := normalizeWorkdir(value)
	info, err := os.Stat(workdir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("工作目录不存在")
		}
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("工作目录不是目录")
	}
	return workdir, nil
}

func tierToFastArg(tier string) string {
	if strings.EqualFold(strings.TrimSpace(tier), "fast") {
		return "on"
	}
	return "off"
}

func cloneClients(src map[*clientConn]struct{}) map[*clientConn]struct{} {
	dst := make(map[*clientConn]struct{}, len(src))
	for client := range src {
		dst[client] = struct{}{}
	}
	return dst
}

func broadcastJSON(clients map[*clientConn]struct{}, event serverEvent) {
	for client := range clients {
		client.mu.Lock()
		_ = client.conn.WriteJSON(event)
		client.mu.Unlock()
	}
}
