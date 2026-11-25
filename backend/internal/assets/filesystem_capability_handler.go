package assets

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxFilesystemCapabilityLifetime = 4 * time.Hour

// filesystemCapabilityHandler consumes the signed URLs emitted by filesystem
// storage. Construction is explicitly refused outside development mode.
type filesystemCapabilityHandler struct {
	storage *filesystemStorage
}

func newFilesystemCapabilityHandler(storage *filesystemStorage, developmentMode bool) (*filesystemCapabilityHandler, error) {
	if !developmentMode || storage == nil {
		return nil, errors.New("filesystem capability routes are development-only")
	}
	return &filesystemCapabilityHandler{storage: storage}, nil
}

func (h *filesystemCapabilityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/_dev/assets/staging/"):
		h.serveStagingPut(w, r)
	case strings.HasPrefix(r.URL.Path, "/_dev/assets/private/"):
		h.servePrivateRead(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *filesystemCapabilityHandler) serveStagingPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, err := parseStagingKey("staging/" + strings.TrimPrefix(r.URL.Path, "/_dev/assets/staging/"))
	if err != nil || key.String() == "" {
		http.NotFound(w, r)
		return
	}
	query, ok := exactCapabilityQuery(r.URL.Query(), "expires", "size", "signature")
	if !ok {
		http.NotFound(w, r)
		return
	}
	expiresAt, valid := h.validExpiry(query.Get("expires"))
	size, sizeErr := strconv.ParseInt(query.Get("size"), 10, 64)
	if !valid || sizeErr != nil || size < 1 || r.ContentLength != size || len(r.TransferEncoding) != 0 ||
		!h.storage.verifyCapability(query.Get("signature"), "PUT", key.String(), query.Get("expires"), query.Get("size")) || !expiresAt.After(h.storage.clock()) {
		http.Error(w, "invalid or expired upload capability", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, size+1)
	if err := h.storage.putStagingFromStream(r.Context(), key, &exactLengthReader{reader: r.Body, remaining: size}); err != nil {
		if errors.Is(err, errStorageUnavailable) || errors.Is(err, http.ErrBodyReadAfterClose) {
			http.Error(w, "upload could not be stored", http.StatusBadRequest)
			return
		}
		http.Error(w, "upload could not be stored", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *filesystemCapabilityHandler) servePrivateRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, err := parsePermanentKey("permanent/" + strings.TrimPrefix(r.URL.Path, "/_dev/assets/private/"))
	if err != nil || key.String() == "" {
		http.NotFound(w, r)
		return
	}
	query, ok := exactCapabilityQuery(r.URL.Query(), "expires", "type", "disposition", "range", "signature")
	if !ok {
		http.NotFound(w, r)
		return
	}
	expiresAt, valid := h.validExpiry(query.Get("expires"))
	options := privateReadOptions{contentType: query.Get("type"), contentDisposition: query.Get("disposition"), rangeAllowed: query.Get("range") == "true"}
	if !valid || !expiresAt.After(h.storage.clock()) || !validPrivateReadOptions(options) || (query.Get("range") != "true" && query.Get("range") != "false") ||
		!h.storage.verifyCapability(query.Get("signature"), "GET", key.String(), query.Get("expires"), options.contentType, options.contentDisposition, query.Get("range")) {
		http.Error(w, "invalid or expired read capability", http.StatusForbidden)
		return
	}
	file, err := h.storage.openPermanentRead(r.Context(), key)
	if errors.Is(err, errStorageNotFound) || errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "private object unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "private object unavailable", http.StatusInternalServerError)
		return
	}
	_, found, err := h.storage.getObjectMetadata(r.Context(), key.String())
	if err != nil || !found {
		http.Error(w, "private object unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", options.contentType)
	w.Header().Set("Content-Disposition", options.contentDisposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if options.rangeAllowed {
		w.Header().Set("Accept-Ranges", "bytes")
	} else {
		w.Header().Set("Accept-Ranges", "none")
		r.Header.Del("Range")
	}
	http.ServeContent(w, r, key.String(), time.Time{}, seeker)
}

func (h *filesystemCapabilityHandler) validExpiry(value string) (time.Time, bool) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	expiresAt := time.Unix(seconds, 0)
	now := h.storage.clock()
	return expiresAt, expiresAt.After(now) && !expiresAt.After(now.Add(maxFilesystemCapabilityLifetime))
}

func exactCapabilityQuery(query url.Values, expected ...string) (url.Values, bool) {
	if len(query) != len(expected) {
		return nil, false
	}
	for _, name := range expected {
		if len(query[name]) != 1 {
			return nil, false
		}
	}
	return query, true
}

func validPrivateReadOptions(options privateReadOptions) bool {
	if strings.ContainsAny(options.contentType+options.contentDisposition, "\r\n") || options.contentType == "" || options.contentDisposition == "" {
		return false
	}
	return strings.HasPrefix(options.contentType, "image/") || strings.HasPrefix(options.contentType, "video/") || strings.HasPrefix(options.contentType, "audio/")
}

type exactLengthReader struct {
	reader    io.Reader
	remaining int64
}

func (r *exactLengthReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		count, err := r.reader.Read(extra[:])
		if count > 0 {
			return 0, fmt.Errorf("request body exceeds signed content length")
		}
		if err == io.EOF {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("request body exceeds signed content length")
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	if err == io.EOF && r.remaining != 0 {
		return count, fmt.Errorf("request body is shorter than signed content length")
	}
	return count, err
}
