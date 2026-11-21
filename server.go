package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
)

//go:embed web/*
var webFiles embed.FS

const maxRequestBytes = 1 << 20

type newExperiment struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
}

type activeRelease struct {
	ReleaseID string `json:"releaseId"`
}

func newHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	registerAssets(mux, store)
	registerExperiments(mux, store)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, http.StatusOK, nil, missing("API endpoint not found"))
	})

	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	static := http.FileServerFS(web)
	mux.Handle("GET /{$}", static)
	mux.Handle("GET /app.js", static)
	mux.Handle("GET /styles.css", static)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if !loopbackHost(r.Host) {
			writeResult(w, http.StatusOK, nil, &requestError{Status: http.StatusForbidden, Code: "invalid_host", Message: "only localhost requests are supported"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func loopbackHost(host string) bool {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip.IsLoopback()
}

func get[T any](run func(*http.Request) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value, err := run(r)
		writeResult(w, http.StatusOK, value, err)
	}
}

func write[I, O any](status int, run func(*http.Request, I) (O, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input I
		if err := decodeBody(w, r, &input); err != nil {
			writeResult(w, status, nil, err)
			return
		}
		value, err := run(r, input)
		writeResult(w, status, value, err)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &requestError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "use Content-Type: application/json"}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return bodyError(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return invalid("provide exactly one JSON object")
		}
		return bodyError(err)
	}
	return nil
}

func bodyError(err error) error {
	var size *http.MaxBytesError
	if errors.As(err, &size) {
		return &requestError{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large", Message: "request exceeds 1 MiB"}
	}
	return invalid("invalid request JSON: %v", err)
}

func writeResult(w http.ResponseWriter, status int, data any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		var request *requestError
		if errors.As(err, &request) {
			status = request.Status
			data = request
		} else {
			slog.Error("API request failed", "error", err)
			status = http.StatusInternalServerError
			data = &requestError{Code: "internal_error", Message: "the operation could not be completed"}
		}
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Debug("could not write response", "error", err)
	}
}

func registerAssets(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("GET /api/v1/schemas", get(func(r *http.Request) ([]Schema, error) {
		return store.schemas(r.Context())
	}))
	mux.HandleFunc("POST /api/v1/schemas", write(http.StatusCreated, func(r *http.Request, input NewSchema) (Schema, error) {
		return inTx(r.Context(), store, func(tx *sql.Tx) (Schema, error) {
			return createSchema(r.Context(), tx, input)
		})
	}))
	mux.HandleFunc("GET /api/v1/schemas/{key}/versions", get(func(r *http.Request) ([]SchemaVersion, error) {
		return store.schemaVersions(r.Context(), r.PathValue("key"))
	}))
	mux.HandleFunc("POST /api/v1/schemas/{key}/versions", write(http.StatusCreated, func(r *http.Request, input SchemaVersionInput) (SchemaVersion, error) {
		return inTx(r.Context(), store, func(tx *sql.Tx) (SchemaVersion, error) {
			return addSchemaVersion(r.Context(), tx, r.PathValue("key"), input)
		})
	}))
	mux.HandleFunc("GET /api/v1/assets", get(func(r *http.Request) ([]Asset, error) {
		return store.assets(r.Context())
	}))
	mux.HandleFunc("POST /api/v1/assets", write(http.StatusCreated, func(r *http.Request, input NewAsset) (Asset, error) {
		return inTx(r.Context(), store, func(tx *sql.Tx) (Asset, error) {
			return createAsset(r.Context(), tx, input)
		})
	}))
	mux.HandleFunc("GET /api/v1/assets/{key}/versions", get(func(r *http.Request) ([]AssetVersion, error) {
		return store.versions(r.Context(), r.PathValue("key"))
	}))
	mux.HandleFunc("POST /api/v1/assets/{key}/versions", write(http.StatusCreated, func(r *http.Request, input VersionInput) (AssetVersion, error) {
		return inTx(r.Context(), store, func(tx *sql.Tx) (AssetVersion, error) {
			return addVersion(r.Context(), tx, r.PathValue("key"), input)
		})
	}))
}

func registerExperiments(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("GET /api/v1/experiments", get(func(r *http.Request) ([]Experiment, error) {
		return store.experiments(r.Context())
	}))
	mux.HandleFunc("POST /api/v1/experiments", write(http.StatusCreated, func(r *http.Request, input newExperiment) (Experiment, error) {
		return createExperiment(r.Context(), store.db, input.Tag, input.Name)
	}))
	mux.HandleFunc("GET /api/v1/experiments/{tag}", get(func(r *http.Request) (ExperimentDetail, error) {
		return store.experimentDetail(r.Context(), r.PathValue("tag"))
	}))
	mux.HandleFunc("PUT /api/v1/experiments/{tag}/draft", write(http.StatusOK, func(r *http.Request, input DraftInput) (Draft, error) {
		if input.ExpectedRevision == nil || *input.ExpectedRevision < 0 {
			return Draft{}, invalid("expectedRevision must be a nonnegative integer")
		}
		return inTx(r.Context(), store, func(tx *sql.Tx) (Draft, error) {
			return saveDraft(r.Context(), tx, r.PathValue("tag"), Draft{Revision: *input.ExpectedRevision, Agents: input.Agents})
		})
	}))
	mux.HandleFunc("GET /api/v1/experiments/{tag}/preview", get(func(r *http.Request) (Config, error) {
		return inTx(r.Context(), store, func(tx *sql.Tx) (Config, error) {
			return getPreview(r.Context(), tx, r.PathValue("tag"))
		})
	}))
	mux.HandleFunc("POST /api/v1/experiments/{tag}/releases", write(http.StatusCreated, func(r *http.Request, input PublishInput) (Config, error) {
		if input.ExpectedRevision == nil || *input.ExpectedRevision < 0 {
			return Config{}, invalid("expectedRevision must be a nonnegative integer")
		}
		active, err := decodeActive(input.ExpectedActiveReleaseID)
		if err != nil {
			return Config{}, err
		}
		return inTx(r.Context(), store, func(tx *sql.Tx) (Config, error) {
			return publish(r.Context(), tx, r.PathValue("tag"), *input.ExpectedRevision, active)
		})
	}))
	mux.HandleFunc("PUT /api/v1/experiments/{tag}/active-release", write(http.StatusOK, func(r *http.Request, input ActivateInput) (activeRelease, error) {
		tag := r.PathValue("tag")
		number, err := parseReleaseID(tag, input.ReleaseID)
		if err != nil {
			return activeRelease{}, err
		}
		active, err := decodeActive(input.ExpectedActiveReleaseID)
		if err != nil {
			return activeRelease{}, err
		}
		return inTx(r.Context(), store, func(tx *sql.Tx) (activeRelease, error) {
			item, err := experiment(r.Context(), tx, tag)
			if err != nil {
				return activeRelease{}, err
			}
			if err := checkActive(item, active); err != nil {
				return activeRelease{}, err
			}
			return activeRelease{ReleaseID: input.ReleaseID}, activate(r.Context(), tx, tag, number)
		})
	}))
	mux.HandleFunc("GET /api/v1/config", get(func(r *http.Request) (json.RawMessage, error) {
		query := r.URL.Query()
		tag := query.Get("tag")
		if err := checkKey("tag", tag); err != nil {
			return nil, err
		}
		var number int64
		if query.Has("releaseId") {
			var err error
			number, err = parseReleaseID(tag, query.Get("releaseId"))
			if err != nil {
				return nil, err
			}
		}
		return store.config(r.Context(), tag, number)
	}))
}

func decodeActive(raw json.RawMessage) (*string, error) {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || (value != nil && *value == "") {
		return nil, invalid("expectedActiveReleaseId must be a release ID or null")
	}
	return value, nil
}
