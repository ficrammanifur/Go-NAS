package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const (
	storageDir  = "./storage/files"
	username    = "admin"
	password    = "admin123"
	sessionName = "gnas_session"
)

func init() {
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		panic(err)
	}
}

func main() {
	http.HandleFunc("/", handleRoot)
	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)
	http.HandleFunc("/dashboard", handleDashboard)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/download", handleDownload)
	http.HandleFunc("/delete", handleDelete)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))

	fmt.Println("🚀 Go-NAS berjalan di http://localhost:8080")
	http.ListenAndServe(":8080", nil)
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
			"Error": "Username atau password salah",
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
		http.Error(w, "Error membaca files", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Files": files,
	}

	renderTemplate(w, "templates/dashboard.html", data)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "File terlalu besar", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error upload file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	filename := filepath.Base(handler.Filename)
	if filename == "" || filename == "." {
		http.Error(w, "Nama file tidak valid", http.StatusBadRequest)
		return
	}

	filepath := filepath.Join(storageDir, filename)
	dst, err := os.Create(filepath)
	if err != nil {
		http.Error(w, "Error menyimpan file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Error menyimpan file", http.StatusInternalServerError)
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
		http.Error(w, "File tidak ditemukan", http.StatusBadRequest)
		return
	}

	// Bersihkan nama file
	filename = filepath.Base(filename)

	// Absolute storage path
	storageAbs, err := filepath.Abs(storageDir)
	if err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	// Absolute file path
	filePath := filepath.Join(storageAbs, filename)
	filePath = filepath.Clean(filePath)

	// Validasi path (ANTI DIRECTORY TRAVERSAL)
	if !strings.HasPrefix(filePath, storageAbs+string(os.PathSeparator)) {
		http.Error(w, "Akses ditolak", http.StatusForbidden)
		return
	}

	// Pastikan file ada
	if _, err := os.Stat(filePath); err != nil {
		http.Error(w, "File tidak ditemukan", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	http.ServeFile(w, r, filePath)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if !isLoggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}

	filename := r.FormValue("file")
	if filename == "" {
		http.Error(w, "File tidak ditemukan", http.StatusBadRequest)
		return
	}

	filename = filepath.Base(filename)
	filepath := filepath.Join(storageDir, filename)

	if !strings.HasPrefix(filepath, storageDir) {
		http.Error(w, "Akses ditolak", http.StatusForbidden)
		return
	}

	if err := os.Remove(filepath); err != nil {
		http.Error(w, "Error menghapus file", http.StatusInternalServerError)
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

func getFileList() ([]map[string]interface{}, error) {
	var files []map[string]interface{}

	entries, err := os.ReadDir(storageDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		size := info.Size()
		sizeStr := formatFileSize(size)

		files = append(files, map[string]interface{}{
			"Name":    entry.Name(),
			"Size":    sizeStr,
			"ModTime": info.ModTime().Format("2006-01-02 15:04:05"),
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
		http.Error(w, "Error membaca template", http.StatusInternalServerError)
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
