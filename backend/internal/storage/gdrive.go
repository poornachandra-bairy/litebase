package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GoogleDrive stores backups in a Google Drive folder.
//
// The Drive REST API is called directly rather than through the official client
// library: the three calls needed here (upload, download, delete) are simple,
// and avoiding the dependency keeps the binary small and the module graph
// minimal, which matters for a single-binary deployment.
type GoogleDrive struct {
	clientID     string
	clientSecret string
	refreshToken string
	folderID     string

	httpClient *http.Client

	// mu guards the cached access token, which several backups may need at
	// once.
	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// GoogleDriveConfig holds the OAuth credentials for Drive access.
//
// A refresh token is used rather than a service account key because a personal
// Drive is the common case, and a refresh token can be revoked from the
// account's security settings without touching the server.
type GoogleDriveConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	// FolderID is the destination folder. Empty uses the account's root.
	FolderID string `json:"folder_id"`
}

// Validate checks that the credentials are complete.
func (c GoogleDriveConfig) Validate() error {
	missing := []string{}
	if strings.TrimSpace(c.ClientID) == "" {
		missing = append(missing, "client_id")
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		missing = append(missing, "client_secret")
	}
	if strings.TrimSpace(c.RefreshToken) == "" {
		missing = append(missing, "refresh_token")
	}
	if len(missing) > 0 {
		return fmt.Errorf("google drive configuration is missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

// NewGoogleDrive builds a Drive provider.
func NewGoogleDrive(cfg GoogleDriveConfig) (*GoogleDrive, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &GoogleDrive{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		refreshToken: cfg.RefreshToken,
		folderID:     cfg.FolderID,
		httpClient: &http.Client{
			// Backups can be large, so the timeout covers a slow upload rather
			// than just a slow response.
			Timeout: 30 * time.Minute,
		},
	}, nil
}

func (g *GoogleDrive) Name() string { return "gdrive" }

// token returns a valid access token, refreshing it when needed.
func (g *GoogleDrive) token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Refresh a minute early so a token cannot expire mid-request.
	if g.accessToken != "" && time.Now().Before(g.expiresAt.Add(-time.Minute)) {
		return g.accessToken, nil
	}

	form := url.Values{
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"refresh_token": {g.refreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("contact Google for an access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The response body can echo the client secret back in some error
		// cases, so it is deliberately not included in the message.
		return "", fmt.Errorf("google rejected the stored credentials (status %d); reconnect Google Drive in Settings", resp.StatusCode)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("google returned an empty access token")
	}

	g.accessToken = payload.AccessToken
	g.expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return g.accessToken, nil
}

func (g *GoogleDrive) authorized(ctx context.Context, req *http.Request) (*http.Response, error) {
	token, err := g.token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return g.httpClient.Do(req)
}

// Put uploads an object using Drive's multipart upload.
func (g *GoogleDrive) Put(ctx context.Context, key string, r io.Reader, size int64) (*Object, error) {
	metadata := map[string]any{"name": key}
	if g.folderID != "" {
		metadata["parents"] = []string{g.folderID}
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}

	// The request body is built through a pipe so the backup streams to Google
	// rather than being buffered in memory first.
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()

		metaHeader := textproto.MIMEHeader{}
		metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
		part, err := writer.CreatePart(metaHeader)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := part.Write(metaJSON); err != nil {
			pw.CloseWithError(err)
			return
		}

		fileHeader := textproto.MIMEHeader{}
		fileHeader.Set("Content-Type", "application/octet-stream")
		filePart, err := writer.CreatePart(fileHeader)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(filePart, r); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(writer.Close())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id,name,size", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+writer.Boundary())

	resp, err := g.authorized(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("upload to Google Drive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("google drive rejected the upload (status %d)", resp.StatusCode)
	}

	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Size string `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil {
		return nil, err
	}

	obj := &Object{Key: key, RemoteID: created.ID, UpdatedAt: time.Now().UTC()}
	fmt.Sscanf(created.Size, "%d", &obj.Size)
	if obj.Size == 0 && size > 0 {
		obj.Size = size
	}
	return obj, nil
}

// findByName resolves a key to a Drive file id.
func (g *GoogleDrive) findByName(ctx context.Context, key string) (string, int64, error) {
	// The query is a Drive search expression, so the name is quoted and any
	// embedded quote or backslash is escaped.
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(key)
	q := fmt.Sprintf("name = '%s' and trashed = false", escaped)
	if g.folderID != "" {
		q += fmt.Sprintf(" and '%s' in parents",
			strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(g.folderID))
	}

	endpoint := "https://www.googleapis.com/drive/v3/files?" + url.Values{
		"q":      {q},
		"fields": {"files(id,name,size,modifiedTime)"},
		"spaces": {"drive"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := g.authorized(ctx, req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("google drive search failed (status %d)", resp.StatusCode)
	}

	var result struct {
		Files []struct {
			ID   string `json:"id"`
			Size string `json:"size"`
		} `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<22)).Decode(&result); err != nil {
		return "", 0, err
	}
	if len(result.Files) == 0 {
		return "", 0, ErrNotFound
	}

	var size int64
	fmt.Sscanf(result.Files[0].Size, "%d", &size)
	return result.Files[0].ID, size, nil
}

func (g *GoogleDrive) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	id, _, err := g.findByName(ctx, key)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?alt=media", nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.authorized(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("google drive download failed (status %d)", resp.StatusCode)
	}
	return resp.Body, nil
}

func (g *GoogleDrive) Delete(ctx context.Context, key string) error {
	id, _, err := g.findByName(ctx, key)
	if errors.Is(err, ErrNotFound) {
		// Retention runs repeatedly; a missing object is already the desired
		// state.
		return nil
	}
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	resp, err := g.authorized(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("google drive delete failed (status %d)", resp.StatusCode)
	}
	return nil
}

func (g *GoogleDrive) Stat(ctx context.Context, key string) (*Object, error) {
	id, size, err := g.findByName(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Object{Key: key, RemoteID: id, Size: size}, nil
}

func (g *GoogleDrive) List(ctx context.Context, prefix string) ([]Object, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(prefix)
	q := fmt.Sprintf("name contains '%s' and trashed = false", escaped)
	if g.folderID != "" {
		q += fmt.Sprintf(" and '%s' in parents", g.folderID)
	}

	endpoint := "https://www.googleapis.com/drive/v3/files?" + url.Values{
		"q":        {q},
		"fields":   {"files(id,name,size,modifiedTime)"},
		"pageSize": {"1000"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.authorized(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google drive list failed (status %d)", resp.StatusCode)
	}

	var result struct {
		Files []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Size         string `json:"size"`
			ModifiedTime string `json:"modifiedTime"`
		} `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<22)).Decode(&result); err != nil {
		return nil, err
	}

	out := make([]Object, 0, len(result.Files))
	for _, f := range result.Files {
		obj := Object{Key: f.Name, RemoteID: f.ID}
		fmt.Sscanf(f.Size, "%d", &obj.Size)
		if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
			obj.UpdatedAt = t
		}
		out = append(out, obj)
	}
	return out, nil
}

// TestConnection verifies the stored credentials work, so Settings can report a
// problem before a scheduled backup silently fails at 3am.
func (g *GoogleDrive) TestConnection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/about?fields=user(emailAddress)", nil)
	if err != nil {
		return err
	}
	resp, err := g.authorized(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("google drive returned status %d; check the credentials", resp.StatusCode)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

// compile-time check that the provider satisfies the interface.
var _ Provider = (*GoogleDrive)(nil)
