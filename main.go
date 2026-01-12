package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const (
	storageDir      = "/home/ficrammanifur/gdrive/Go-NAS"
	username        = "admin"
	password        = "admin123"
	sessionName     = "gnas_session"
	rcloneMountPath = "/home/ficrammanifur/gdrive"
	encryptionKey   = "your-32-byte-encryption-key-here"
)

type StorageInfo struct {
	Type         string
	Status       string
	UsedGB       float64
	TotalGB      float64
	Provider     string
	QuotaPercent float64
}

type FileInfo struct {
	Name      string
	Size      string
	ModTime   string
	RealName  string
	ShareLink string
	IsFolder  bool
	Path      string
}

type ShareLink struct {
	ID        string
	Filename  string
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

var shareLinks map[string]ShareLink = make(map[string]ShareLink)

func init() {
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		panic(err)
	}
	go initializeRcloneMount()
}

func main() {
	http.HandleFunc("/", handleRoot)
	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)
	http.HandleFunc("/dashboard", handleDashboard)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/download", handleDownload)
	http.HandleFunc("/delete", handleDelete)
	http.HandleFunc("/mkdir", handleMkdir)
	http.HandleFunc("/api/storage-info", handleStorageInfo)
	http.HandleFunc("/api/preview", handlePreview)
	http.HandleFunc("/share", handleShareLink)
	http.HandleFunc("/download-shared", handleDownloadShared)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))

	fmt.Println("Go-NAS running at http://localhost:8080")
	fmt.Println("Features: Dark Mode, Folder Support, Drag & Drop, Preview, Encryption")
	http.ListenAndServe(":8080", nil)
}

func handleMkdir(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	folderName := strings.TrimSpace(r.FormValue("folder"))
	parentPath := strings.TrimSpace(r.FormValue("parent"))

	if folderName == "" {
		http.Error(w, "Folder name required", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(storageDir, parentPath, folderName)
	if !strings.HasPrefix(fullPath, storageDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	if err := os.MkdirAll(fullPath, 0755); err != nil {
		http.Error(w, "Error creating folder", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func handlePreview(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	filename := r.URL.Query().Get("file")
	if filename == "" {
		http.Error(w, "File not found", http.StatusBadRequest)
		return
	}

	filename = filepath.Base(filename)
	filePath := filepath.Join(storageDir, filename+".enc")

	if _, err := os.Stat(filePath); err != nil {
		filePath = filepath.Join(storageDir, filename)
	}

	if !strings.HasPrefix(filePath, storageDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	if strings.HasSuffix(filePath, ".enc") {
		decrypted, err := decryptFile(fileData)
		if err != nil {
			http.Error(w, "Decryption failed", http.StatusInternalServerError)
			return
		}
		fileData = decrypted
	}

	ext := strings.ToLower(filepath.Ext(filename))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", mimeType)
	w.Write(fileData)
}

func initializeRcloneMount() {
	time.Sleep(2 * time.Second)

	// Cek apakah rclone sudah jalan
	if exec.Command("pgrep", "-f", "rclone mount gdrive").Run() == nil {
		fmt.Println("[Rclone] rclone mount already running")
		return
	}

	fmt.Println("[Rclone] Starting Google Drive mount...")

	cmd := exec.Command(
		"rclone", "mount", "gdrive:", rcloneMountPath,
		"--vfs-cache-mode", "writes",
		"--dir-cache-time", "72h",
		"--poll-interval", "15s",
		"--daemon",
	)

	if err := cmd.Run(); err != nil {
		fmt.Println("[Rclone] Mount failed:", err)
		return
	}

	// Tunggu mount siap (max 5 detik)
	for i := 0; i < 10; i++ {
		if exec.Command("mountpoint", "-q", rcloneMountPath).Run() == nil {
			fmt.Println("[Rclone] Google Drive mount ready")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Println("[Rclone] Mount process started but not detected yet")
}

func getRcloneQuota() (used, total float64, err error) {
	cmd := exec.Command("rclone", "about", "gdrive:")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Total:") {
			fmt.Sscanf(line, "Total: %fG", &total)
		}
		if strings.Contains(line, "Used:") {
			fmt.Sscanf(line, "Used: %fG", &used)
		}
	}
	return used, total, nil
}

func getStorageInfo() StorageInfo {
	info := StorageInfo{}
	cmd := exec.Command("mountpoint", "-q", rcloneMountPath)
	if err := cmd.Run(); err == nil {
		info.Type = "cloud"
		info.Status = "online"
		info.Provider = "Google Drive"
		
		used, total, err := getRcloneQuota()
		if err == nil {
			info.UsedGB = used
			info.TotalGB = total
			info.QuotaPercent = (used / total) * 100
		} else {
			info.TotalGB = 1000
		}
	} else {
		info.Type = "local"
		info.Status = "offline"
		info.Provider = "Local Storage"
		info.TotalGB = 500
	}
	
	info.UsedGB = float64(getStorageUsed()) / (1024 * 1024 * 1024)
	if info.TotalGB > 0 {
		info.QuotaPercent = (info.UsedGB / info.TotalGB) * 100
	}
	
	return info
}

func getStorageUsed() int64 {
	var totalSize int64
	filepath.Walk(storageDir, func(path string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			totalSize += fi.Size()
		}
		return nil
	})
	return totalSize
}

func handleStorageInfo(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	info := getStorageInfo()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"type":"%s","status":"%s","usedGB":"%.2f","totalGB":"%.2f","provider":"%s","quotaPercent":"%.1f"}`,
		info.Type, info.Status, info.UsedGB, info.TotalGB, info.Provider, info.QuotaPercent)
}

func encryptFile(data []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(encryptionKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, nil), nil
}

func decryptFile(encrypted []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(encryptionKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func generateShareLink(filename string) string {
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(filename+time.Now().String())))[:16]
	shareLinks[token] = ShareLink{
		ID:        token,
		Filename:  filename,
		Token:     token,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	return fmt.Sprintf("/download-shared?token=%s", token)
}

func handleDownloadShared(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Invalid token", http.StatusBadRequest)
		return
	}

	share, exists := shareLinks[token]
	if !exists {
		http.Error(w, "Link expired or invalid", http.StatusNotFound)
		return
	}

	if time.Now().After(share.ExpiresAt) {
		delete(shareLinks, token)
		http.Error(w, "Link expired", http.StatusForbidden)
		return
	}

	filename := share.Filename
	encryptedPath := filepath.Join(storageDir, filename+".enc")
	plainPath := filepath.Join(storageDir, filename)

	var filePath string
	if _, err := os.Stat(encryptedPath); err == nil {
		filePath = encryptedPath
	} else {
		filePath = plainPath
	}

	if !strings.HasPrefix(filePath, storageDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	var outputData []byte
	if strings.HasSuffix(filePath, ".enc") {
		decrypted, err := decryptFile(fileData)
		if err != nil {
			http.Error(w, "Decryption failed", http.StatusInternalServerError)
			return
		}
		outputData = decrypted
		filename = strings.TrimSuffix(filename, ".enc")
	} else {
		outputData = fileData
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(outputData)
}

func handleShareLink(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := r.FormValue("file")
	if filename == "" {
		http.Error(w, "Filename required", http.StatusBadRequest)
		return
	}

	shareLink := generateShareLink(filename)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"shareLink":"http://localhost:8080%s"}`, shareLink)
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if isLoggedIn(r) {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if isLoggedIn(r) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		renderTemplate(w, "templates/login.html", nil)
		return
	}

	if r.Method == http.MethodPost {
		user := r.FormValue("username")
		pass := r.FormValue("password")

		if user == username && pass == password {
			http.SetCookie(w, &http.Cookie{
				Name:     sessionName,
				Value:    hashPassword(pass),
				Path:     "/",
				Expires:  time.Now().Add(24 * time.Hour),
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		renderTemplate(w, "templates/login.html", map[string]string{
			"Error": "Username or password incorrect",
		})
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	files, err := getFileList()
	if err != nil {
		http.Error(w, "Error reading files", http.StatusInternalServerError)
		return
	}

	storageInfo := getStorageInfo()

	data := map[string]interface{}{
		"Files":            files,
		"StorageType":      storageInfo.Type,
		"StorageProvider":  storageInfo.Provider,
		"StorageStatus":    storageInfo.Status,
		"UsedGB":           fmt.Sprintf("%.2f", storageInfo.UsedGB),
		"TotalGB":          fmt.Sprintf("%.2f", storageInfo.TotalGB),
		"QuotaPercent":     fmt.Sprintf("%.1f", storageInfo.QuotaPercent),
	}

	renderTemplate(w, "templates/dashboard.html", data)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(500 << 20); err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error uploading file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	folderPath := strings.TrimSpace(r.FormValue("folder"))
	filename := filepath.Base(handler.Filename)
	if filename == "" || filename == "." {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	fileData, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Error reading file", http.StatusInternalServerError)
		return
	}

	encryptedData, err := encryptFile(fileData)
	if err != nil {
		http.Error(w, "Encryption failed", http.StatusInternalServerError)
		return
	}

	fullFolderPath := filepath.Join(storageDir, folderPath)
	if folderPath != "" {
		if !strings.HasPrefix(fullFolderPath, storageDir) {
			http.Error(w, "Access denied", http.StatusForbidden)
			return
		}
		if err := os.MkdirAll(fullFolderPath, 0755); err != nil {
			http.Error(w, "Error creating folder", http.StatusInternalServerError)
			return
		}
	} else {
		fullFolderPath = storageDir
	}

	encryptedFilename := filename + ".enc"
	filePath := filepath.Join(fullFolderPath, encryptedFilename)

	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := dst.Write(encryptedData); err != nil {
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	filename := r.URL.Query().Get("file")
	if filename == "" {
		http.Error(w, "File not found", http.StatusBadRequest)
		return
	}

	filename = filepath.Base(filename)
	encryptedPath := filepath.Join(storageDir, filename+".enc")
	plainPath := filepath.Join(storageDir, filename)

	var filePath string
	if _, err := os.Stat(encryptedPath); err == nil {
		filePath = encryptedPath
	} else {
		filePath = plainPath
	}

	if !strings.HasPrefix(filePath, storageDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	var outputData []byte
	if strings.HasSuffix(filePath, ".enc") {
		decrypted, err := decryptFile(fileData)
		if err != nil {
			http.Error(w, "Decryption failed", http.StatusInternalServerError)
			return
		}
		outputData = decrypted
		filename = strings.TrimSuffix(filename, ".enc")
	} else {
		outputData = fileData
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(outputData)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := r.FormValue("file")
	if filename == "" {
		http.Error(w, "File not found", http.StatusBadRequest)
		return
	}

	filename = filepath.Base(filename)
	filepath := filepath.Join(storageDir, filename+".enc")

	if !strings.HasPrefix(filepath, storageDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	if err := os.Remove(filepath); err != nil {
		http.Error(w, "Error deleting file", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func isLoggedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionName)
	if err != nil {
		return false
	}
	return cookie.Value == hashPassword(password)
}

func hashPassword(pass string) string {
	hash := sha256.Sum256([]byte(pass))
	return fmt.Sprintf("%x", hash)
}

func getFileList() ([]FileInfo, error) {
	var files []FileInfo

	entries, err := os.ReadDir(storageDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if entry.IsDir() {
			files = append(files, FileInfo{
				Name:     entry.Name(),
				IsFolder: true,
				Path:     entry.Name(),
			})
			continue
		}

		size := info.Size()
		sizeStr := formatFileSize(size)

		displayName := entry.Name()
		realName := displayName
		if strings.HasSuffix(displayName, ".enc") {
			displayName = strings.TrimSuffix(displayName, ".enc")
		}

		files = append(files, FileInfo{
			Name:      displayName,
			Size:      sizeStr,
			ModTime:   info.ModTime().Format("2006-01-02 15:04:05"),
			RealName:  realName,
			ShareLink: generateShareLink(displayName),
			IsFolder:  false,
		})
	}

	return files, nil
}

func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %c", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func renderTemplate(w http.ResponseWriter, tmplPath string, data interface{}) {
	content, err := os.ReadFile(tmplPath)
	if err != nil {
		http.Error(w, "Error reading template", http.StatusInternalServerError)
		return
	}

	tmpl, err := template.New(filepath.Base(tmplPath)).Parse(string(content))
	if err != nil {
		http.Error(w, "Error parsing template", http.StatusInternalServerError)
		return
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
	}
}
