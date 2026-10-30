package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	appTitle    = "Nearby Pro"
	listenPort  = "5000"
	maxUpload   = int64(10 << 30) // 10 GiB total request size
	sessionLife = 12 * time.Hour
)

type App struct {
	dir       string
	filesDir  string
	config    Config
	sessions  sync.Map
	downloads sync.Map
	historyMu sync.Mutex
}

type DownloadProgress struct {
	mu        sync.RWMutex
	Filename  string
	Total     int64
	Sent      int64
	Started   time.Time
	Finished  bool
	Success   bool
	WriteFail bool
}

type trackingResponseWriter struct {
	http.ResponseWriter
	progress *DownloadProgress
}

func (w *trackingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if w.progress != nil {
		w.progress.mu.Lock()
		w.progress.Sent += int64(n)
		if err != nil {
			w.progress.WriteFail = true
		}
		w.progress.mu.Unlock()
	}
	return n, err
}

type Config struct {
	PIN string `json:"pin"`
}

type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SizeStr string `json:"size_str"`
	ModTime string `json:"modified"`
	MIME    string `json:"mime"`
	Kind    string `json:"kind"`
}

type HistoryItem struct {
	Action   string `json:"action"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	When     string `json:"created_at"`
}

type PageData struct {
	Title string
	URLs  []string
	PIN   string
}

//go:embed web/index.html
var indexHTML string

//go:embed web/login.html
var loginHTML string

var pageTemplate = template.Must(template.New("index").Parse(indexHTML))
var loginTemplate = template.Must(template.New("login").Parse(loginHTML))

func main() {
	exe, err := os.Executable()
	if err != nil {
		fatal("Cannot locate the application folder: " + err.Error())
	}
	dir := filepath.Dir(exe)
	filesDir := filepath.Join(dir, "shared_files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		fatal("Cannot create shared_files folder: " + err.Error())
	}

	cfg, err := loadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		fatal("Cannot load app configuration: " + err.Error())
	}

	a := &App{dir: dir, filesDir: filesDir, config: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", a.login)
	mux.HandleFunc("/logout", a.logout)
	mux.HandleFunc("/qr.png", a.withAuth(a.qr))
	mux.HandleFunc("/api/files", a.withAuth(a.apiFiles))
	mux.HandleFunc("/api/history", a.withAuth(a.apiHistory))
	mux.HandleFunc("/api/download-status", a.withAuth(a.apiDownloadStatus))
	mux.HandleFunc("/upload", a.withAuth(a.upload))
	mux.HandleFunc("/download/", a.withAuth(a.download))
	mux.HandleFunc("/preview/", a.withAuth(a.preview))
	mux.HandleFunc("/delete/", a.withAuth(a.delete))
	mux.HandleFunc("/zip", a.withAuth(a.zipSelected))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/", a.withAuth(a.index))

	server := &http.Server{
		Addr:              ":" + listenPort,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	urls := localURLs()
	fmt.Println()
	fmt.Println("=======================================")
	fmt.Println("          NEARBY PRO - READY")
	fmt.Println("=======================================")
	fmt.Println("On this PC: http://127.0.0.1:" + listenPort)
	fmt.Println("SHARING PIN (10 digits): " + formatPIN(cfg.PIN))
	fmt.Println()
	fmt.Println("Open one of these addresses on your phone:")
	for _, u := range urls {
		fmt.Println("  " + u)
	}
	fmt.Println()
	fmt.Println("Scan the QR code in the PC browser, or type an address above.")
	fmt.Println("Keep this window open while sharing. Press Ctrl+C to stop.")
	fmt.Println("Files are stored beside this application in: shared_files")
	fmt.Println("Use only on a trusted private network; do not port-forward this app.")
	fmt.Println()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("Cannot start the local server: " + err.Error() + "\nTry closing another Nearby instance using port " + listenPort + ".")
	}
}

func fatal(s string) {
	fmt.Println()
	fmt.Println("ERROR: " + s)
	fmt.Println()
	fmt.Println("Press Enter to close this window.")
	fmt.Scanln()
	os.Exit(1)
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if len(cfg.PIN) != 10 || !allDigits(cfg.PIN) {
		n, err := rand.Int(rand.Reader, big.NewInt(10000000000))
		if err != nil {
			return cfg, err
		}
		cfg.PIN = fmt.Sprintf("%010d", n.Int64())
		b, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(path, b, 0600); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func allDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizePIN(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func formatPIN(s string) string {
	if len(s) == 10 {
		return s[:5] + " " + s[5:]
	}
	return s
}

func (a *App) validSession(r *http.Request) bool {
	c, err := r.Cookie("nearby_session")
	if err != nil {
		return false
	}
	v, ok := a.sessions.Load(c.Value)
	if !ok {
		return false
	}
	exp, ok := v.(time.Time)
	return ok && time.Now().Before(exp)
}

func (a *App) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.validSession(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/upload") ||
				strings.HasPrefix(r.URL.Path, "/download/") || strings.HasPrefix(r.URL.Path, "/preview/") ||
				strings.HasPrefix(r.URL.Path, "/delete/") || r.URL.Path == "/zip" || r.URL.Path == "/qr.png" {
				http.Error(w, "Authentication required", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if a.validSession(r) && r.Method == http.MethodGet {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		entered := normalizePIN(r.FormValue("pin"))
		if len(entered) == 10 && subtle.ConstantTimeCompare([]byte(entered), []byte(a.config.PIN)) == 1 {
			tokenBytes := make([]byte, 32)
			if _, err := rand.Read(tokenBytes); err != nil {
				http.Error(w, "Could not create login session", http.StatusInternalServerError)
				return
			}
			token := fmt.Sprintf("%x", tokenBytes)
			a.sessions.Store(token, time.Now().Add(sessionLife))
			http.SetCookie(w, &http.Cookie{
				Name: "nearby_session", Value: token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLife.Seconds()),
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_ = loginTemplate.Execute(w, map[string]string{"Error": "Incorrect PIN. Enter the 10 digits displayed in the PC terminal; spaces are optional."})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginTemplate.Execute(w, map[string]string{})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("nearby_session"); err == nil {
		a.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "nearby_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) index(w http.ResponseWriter, r *http.Request) {
	urls := localURLs()
	if len(urls) == 0 {
		urls = []string{"http://127.0.0.1:" + listenPort}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTemplate.Execute(w, PageData{Title: appTitle, URLs: urls, PIN: formatPIN(a.config.PIN)})
}

func (a *App) qr(w http.ResponseWriter, r *http.Request) {
	urls := localURLs()
	if len(urls) == 0 {
		urls = []string{"http://127.0.0.1:" + listenPort}
	}
	pngBytes, err := makeQR(urls[0])
	if err != nil {
		http.Error(w, "QR generation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pngBytes)
}

func localURLs() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var urls []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil || ip.IsLoopback() {
				continue
			}
			u := "http://" + ip.To4().String() + ":" + listenPort
			found := false
			for _, old := range urls {
				if old == u {
					found = true
				}
			}
			if !found {
				urls = append(urls, u)
			}
		}
	}
	sort.Strings(urls)
	return urls
}

func (a *App) apiFiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.listFiles())
}

func (a *App) listFiles() []FileInfo {
	entries, err := os.ReadDir(a.filesDir)
	if err != nil {
		return []FileInfo{}
	}
	var out []FileInfo
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		name := ent.Name()
		mtype := mime.TypeByExtension(filepath.Ext(name))
		if mtype == "" {
			mtype = "application/octet-stream"
		}
		kind := strings.SplitN(mtype, "/", 2)[0]
		out = append(out, FileInfo{Name: name, Size: info.Size(), SizeStr: humanSize(info.Size()),
			ModTime: info.ModTime().Format("02 Jan 2006, 03:04 PM"), MIME: mtype, Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Upload could not be read. The total upload limit is 10 GB.", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if r.MultipartForm == nil {
		http.Error(w, "No files were received.", http.StatusBadRequest)
		return
	}
	headers := r.MultipartForm.File["files"]
	if len(headers) == 0 {
		http.Error(w, "Choose at least one file.", http.StatusBadRequest)
		return
	}

	var saved []string
	for _, header := range headers {
		name := cleanFilename(header.Filename)
		if name == "" {
			continue
		}
		dst, finalName, err := createUniqueFile(a.filesDir, name)
		if err != nil {
			http.Error(w, "Could not save file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		src, err := header.Open()
		if err != nil {
			dst.Close()
			os.Remove(dst.Name())
			http.Error(w, "Could not read uploaded file.", http.StatusBadRequest)
			return
		}
		size, copyErr := io.Copy(dst, src)
		_ = src.Close()
		_ = dst.Close()
		if copyErr != nil {
			_ = os.Remove(dst.Name())
			http.Error(w, "File transfer failed: "+copyErr.Error(), http.StatusInternalServerError)
			return
		}
		saved = append(saved, finalName)
		a.record("Uploaded", finalName, size)
	}
	writeJSON(w, map[string]any{"ok": true, "saved": saved})
}

func cleanFilename(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	s = filepath.Base(s)
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			continue
		}
		b.WriteRune(r)
	}
	s = strings.TrimSpace(strings.TrimRight(b.String(), "."))
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

func createUniqueFile(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 0; n < 10000; n++ {
		candidate := name
		if n > 0 {
			candidate = stem + "_" + strconv.Itoa(n) + ext
		}
		p := filepath.Join(dir, candidate)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			return f, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("too many files with the same name")
}

func (a *App) download(w http.ResponseWriter, r *http.Request) {
	p, name, err := a.resolve(r.URL.EscapedPath(), "/download/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, statErr := os.Stat(p)
	if statErr != nil {
		http.NotFound(w, r)
		return
	}
	a.record("Downloaded", name, info.Size())
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))

	trackID := r.URL.Query().Get("track")
	if !validTrackID(trackID) {
		http.ServeFile(w, r, p)
		return
	}

	progress := &DownloadProgress{
		Filename: name,
		Total:    info.Size(),
		Started:  time.Now(),
	}
	a.downloads.Store(trackID, progress)
	defer func() {
		progress.mu.Lock()
		progress.Finished = true
		progress.Success = !progress.WriteFail && progress.Sent >= progress.Total
		progress.mu.Unlock()
		time.AfterFunc(2*time.Minute, func() { a.downloads.Delete(trackID) })
	}()

	trackedWriter := &trackingResponseWriter{ResponseWriter: w, progress: progress}
	http.ServeFile(trackedWriter, r, p)
}

func validTrackID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (a *App) apiDownloadStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !validTrackID(id) {
		http.Error(w, "Invalid download tracking ID.", http.StatusBadRequest)
		return
	}
	value, ok := a.downloads.Load(id)
	if !ok {
		writeJSON(w, map[string]any{"found": false})
		return
	}
	progress := value.(*DownloadProgress)
	progress.mu.RLock()
	elapsed := time.Since(progress.Started).Milliseconds()
	result := map[string]any{
		"found":      true,
		"filename":   progress.Filename,
		"total":      progress.Total,
		"sent":       progress.Sent,
		"elapsed_ms": elapsed,
		"finished":   progress.Finished,
		"success":    progress.Success,
	}
	progress.mu.RUnlock()
	writeJSON(w, result)
}

func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	p, _, err := a.resolve(r.URL.EscapedPath(), "/preview/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mtype := mime.TypeByExtension(filepath.Ext(p))
	if !strings.HasPrefix(mtype, "image/") && !strings.HasPrefix(mtype, "video/") &&
		!strings.HasPrefix(mtype, "audio/") && mtype != "application/pdf" {
		http.Error(w, "Preview is not supported for this file type.", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

func (a *App) delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, name, err := a.resolve(r.URL.EscapedPath(), "/delete/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, statErr := os.Stat(p)
	if statErr != nil {
		http.NotFound(w, r)
		return
	}
	if err := os.Remove(p); err != nil {
		http.Error(w, "Could not delete file.", http.StatusInternalServerError)
		return
	}
	a.record("Deleted", name, info.Size())
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) resolve(requestPath, prefix string) (string, string, error) {
	raw := strings.TrimPrefix(requestPath, prefix)
	name, err := url.PathUnescape(raw)
	if err != nil {
		return "", "", err
	}
	name = cleanFilename(name)
	if name == "" {
		return "", "", os.ErrNotExist
	}
	p := filepath.Join(a.filesDir, name)
	rel, err := filepath.Rel(a.filesDir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", os.ErrNotExist
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", "", os.ErrNotExist
	}
	return p, name, nil
}

func (a *App) zipSelected(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid selection.", http.StatusBadRequest)
		return
	}
	tmp, err := os.CreateTemp(a.dir, "nearby-download-*.zip")
	if err != nil {
		http.Error(w, "Could not create ZIP.", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	zw := zip.NewWriter(tmp)
	count := 0
	var total int64
	for _, raw := range body.Files {
		name := cleanFilename(raw)
		if name == "" {
			continue
		}
		p := filepath.Join(a.filesDir, name)
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			f.Close()
			continue
		}
		entry, err := zw.Create(name)
		if err == nil {
			n, cpErr := io.Copy(entry, f)
			if cpErr == nil {
				count++
				total += n
			}
		}
		f.Close()
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		http.Error(w, "Could not finish ZIP.", http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, "Could not finish ZIP.", http.StatusInternalServerError)
		return
	}
	if count == 0 {
		http.Error(w, "No valid files were selected.", http.StatusBadRequest)
		return
	}
	a.record("ZIP downloaded", strconv.Itoa(count)+" files", total)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="Nearby_Shared_Files.zip"`)
	http.ServeFile(w, r, tmpName)
}

func (a *App) apiHistory(w http.ResponseWriter, r *http.Request) {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	path := filepath.Join(a.dir, "transfer_history.json")
	b, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, []HistoryItem{})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func (a *App) record(action, name string, size int64) {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	path := filepath.Join(a.dir, "transfer_history.json")
	var items []HistoryItem
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &items)
	}
	items = append([]HistoryItem{{Action: action, Filename: name, Size: size, When: time.Now().Format("02 Jan 2006, 03:04:05 PM")}}, items...)
	if len(items) > 100 {
		items = items[:100]
	}
	if b, err := json.MarshalIndent(items, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0644)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// QR code generator: Version 2-L supports the local URLs used by this app.
func makeQR(text string) ([]byte, error) {
	data := []byte(text)
	if len(data) > 32 {
		return nil, errors.New("URL is too long for built-in QR encoder")
	}
	bits := make([]bool, 0, 272)
	appendBits := func(value, count int) {
		for i := count - 1; i >= 0; i-- {
			bits = append(bits, ((value>>i)&1) != 0)
		}
	}
	appendBits(4, 4)         // byte mode
	appendBits(len(data), 8) // byte count
	for _, b := range data {
		appendBits(int(b), 8)
	}
	if len(bits) < 272 {
		term := 272 - len(bits)
		if term > 4 {
			term = 4
		}
		appendBits(0, term)
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	var codewords []byte
	for i := 0; i < len(bits); i += 8 {
		var v byte
		for j := 0; j < 8; j++ {
			v <<= 1
			if bits[i+j] {
				v |= 1
			}
		}
		codewords = append(codewords, v)
	}
	pads := []byte{0xEC, 0x11}
	for len(codewords) < 34 {
		codewords = append(codewords, pads[(len(codewords)-((len(bits)+7)/8))%2])
	}
	ecc := reedSolomon(codewords, 10)
	all := append(append([]byte{}, codewords...), ecc...)
	allBits := make([]bool, 0, len(all)*8)
	for _, b := range all {
		for i := 7; i >= 0; i-- {
			allBits = append(allBits, ((b>>i)&1) != 0)
		}
	}

	const n = 25
	var m [n][n]bool
	var reserved [n][n]bool
	set := func(x, y int, dark bool) {
		if x >= 0 && x < n && y >= 0 && y < n {
			m[y][x] = dark
			reserved[y][x] = true
		}
	}
	finder := func(ox, oy int) {
		for dy := -1; dy <= 7; dy++ {
			for dx := -1; dx <= 7; dx++ {
				x, y := ox+dx, oy+dy
				dark := false
				if dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6 {
					dark = dx == 0 || dx == 6 || dy == 0 || dy == 6 ||
						(dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4)
				}
				set(x, y, dark)
			}
		}
	}
	finder(0, 0)
	finder(n-7, 0)
	finder(0, n-7)
	// Alignment target centered at (18,18).
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			dark := maxInt(abs(dx), abs(dy)) != 1
			set(18+dx, 18+dy, dark)
		}
	}
	for i := 8; i <= 16; i++ {
		set(i, 6, i%2 == 0)
		set(6, i, i%2 == 0)
	}
	// Reserve format-information modules.
	for i := 0; i <= 5; i++ {
		reserved[i][8] = true
	}
	reserved[7][8] = true
	reserved[8][8] = true
	reserved[8][7] = true
	for i := 9; i <= 14; i++ {
		reserved[8][14-i] = true
	}
	for i := 0; i <= 7; i++ {
		reserved[8][n-1-i] = true
	}
	for i := 8; i <= 14; i++ {
		reserved[n-15+i][8] = true
	}
	reserved[n-8][8] = true

	bitIndex := 0
	up := true
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right--
		}
		for vert := 0; vert < n; vert++ {
			y := vert
			if up {
				y = n - 1 - vert
			}
			for j := 0; j < 2; j++ {
				x := right - j
				if reserved[y][x] {
					continue
				}
				dark := false
				if bitIndex < len(allBits) {
					dark = allBits[bitIndex]
				}
				bitIndex++
				if (x+y)%2 == 0 {
					dark = !dark
				} // mask pattern 0
				m[y][x] = dark
				reserved[y][x] = true
			}
		}
		up = !up
	}
	// Format bits: error correction L (01), mask pattern 0.
	formatData := 1 << 3
	rem := formatData
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ (((rem >> 9) & 1) * 0x537)
	}
	format := ((formatData << 10) | rem) ^ 0x5412
	for i := 0; i <= 5; i++ {
		m[i][8] = ((format >> i) & 1) != 0
	}
	m[7][8] = ((format >> 6) & 1) != 0
	m[8][8] = ((format >> 7) & 1) != 0
	m[8][7] = ((format >> 8) & 1) != 0
	for i := 9; i <= 14; i++ {
		m[8][14-i] = ((format >> i) & 1) != 0
	}
	for i := 0; i <= 7; i++ {
		m[8][n-1-i] = ((format >> i) & 1) != 0
	}
	for i := 8; i <= 14; i++ {
		m[n-15+i][8] = ((format >> i) & 1) != 0
	}
	m[n-8][8] = true

	const scale, quiet = 8, 4
	imgSize := (n + quiet*2) * scale
	img := image.NewRGBA(image.Rect(0, 0, imgSize, imgSize))
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	for y := 0; y < imgSize; y++ {
		for x := 0; x < imgSize; x++ {
			img.Set(x, y, white)
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !m[y][x] {
				continue
			}
			for py := 0; py < scale; py++ {
				for px := 0; px < scale; px++ {
					img.Set((x+quiet)*scale+px, (y+quiet)*scale+py, black)
				}
			}
		}
	}
	var out bytes.Buffer
	err := png.Encode(&out, img)
	return out.Bytes(), err
}

func reedSolomon(data []byte, degree int) []byte {
	gen := []byte{1}
	for i := 0; i < degree; i++ {
		root := gfPow(2, i)
		next := make([]byte, len(gen)+1)
		for j, v := range gen {
			next[j] ^= v
			next[j+1] ^= gfMul(v, root)
		}
		gen = next
	}
	msg := make([]byte, len(data)+degree)
	copy(msg, data)
	for i := 0; i < len(data); i++ {
		coef := msg[i]
		if coef == 0 {
			continue
		}
		for j, g := range gen {
			msg[i+j] ^= gfMul(g, coef)
		}
	}
	return msg[len(data):]
}

func gfMul(x, y byte) byte {
	var z byte
	for i := 0; i < 8; i++ {
		if y&1 != 0 {
			z ^= x
		}
		hi := x & 0x80
		x <<= 1
		if hi != 0 {
			x ^= 0x1D
		} // primitive polynomial 0x11D
		y >>= 1
	}
	return z
}
func gfPow(x byte, power int) byte {
	v := byte(1)
	for i := 0; i < power; i++ {
		v = gfMul(v, x)
	}
	return v
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
