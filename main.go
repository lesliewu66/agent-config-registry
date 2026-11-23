package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const version = "0.2.3"

//go:embed examples/support-routing.json
var demoJSON []byte

//go:embed examples/schema-routing.json
var schemaDemoJSON []byte

//go:embed examples/image-routing.json
var imageDemoJSON []byte

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "loopback address for the web UI and HTTP API")
	dbPath := flag.String("db", "data/config.db", "path to the SQLite database")
	withDemo := flag.Bool("demo", false, "create the sample experiment if it does not exist")
	showVersion := flag.Bool("version", false, "print the program version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || !loopbackHost(host) {
		return errors.New("-addr must be a loopback address with a port, such as 127.0.0.1:8080")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.db.Close()
	if *withDemo {
		if err := seedDemo(ctx, store); err != nil {
			return fmt.Errorf("load demo: %w", err)
		}
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           newHandler(store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	log.Printf("Agent Config Registry v%s · http://%s", version, listener.Addr())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type demoAsset struct {
	Key      string            `json:"key"`
	Name     string            `json:"name"`
	Schema   SchemaRef         `json:"schema"`
	Versions []json.RawMessage `json:"versions"`
}

func seedDemo(ctx context.Context, store *Store) error {
	var demo struct {
		Schemas    []NewSchema `json:"schemas"`
		Assets     []demoAsset `json:"assets"`
		Experiment struct {
			newExperiment
			Agents []AgentRef `json:"agents"`
		} `json:"experiment"`
	}
	if err := json.Unmarshal(demoJSON, &demo); err != nil {
		return err
	}

	_, err := inTx(ctx, store, func(tx *sql.Tx) (struct{}, error) {
		var existing int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM experiments WHERE tag = ?", demo.Experiment.Tag).Scan(&existing)
		if err == nil {
			return struct{}{}, seedCompositionDemos(ctx, tx)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, err
		}
		if err := seedAssets(ctx, tx, demo.Schemas, demo.Assets); err != nil {
			return struct{}{}, err
		}
		exp := demo.Experiment
		if _, err := createExperiment(ctx, tx, exp.Tag, exp.Name); err != nil {
			return struct{}{}, err
		}
		draft, err := saveDraft(ctx, tx, exp.Tag, Draft{Agents: exp.Agents})
		if err != nil {
			return struct{}{}, err
		}
		if _, err = publish(ctx, tx, exp.Tag, draft.Revision, nil); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, seedCompositionDemos(ctx, tx)
	})
	return err
}

func seedAssets(ctx context.Context, tx *sql.Tx, schemas []NewSchema, assets []demoAsset) error {
	for _, schema := range schemas {
		if _, err := createSchema(ctx, tx, schema); err != nil {
			return err
		}
	}
	for _, asset := range assets {
		if len(asset.Versions) == 0 {
			return fmt.Errorf("demo asset %q has no versions", asset.Key)
		}
		if _, err := createAsset(ctx, tx, NewAsset{Key: asset.Key, Name: asset.Name, Schema: asset.Schema, Config: asset.Versions[0]}); err != nil {
			return err
		}
		for i, config := range asset.Versions[1:] {
			if _, err := addVersion(ctx, tx, asset.Key, VersionInput{BaseVersion: int64(i + 1), Schema: asset.Schema, Config: config}); err != nil {
				return err
			}
		}
	}
	return nil
}

func seedCompositionDemos(ctx context.Context, tx *sql.Tx) error {
	for _, fixture := range [][]byte{schemaDemoJSON, imageDemoJSON} {
		if err := seedCompositionDemo(ctx, tx, fixture); err != nil {
			return err
		}
	}
	return nil
}

func seedCompositionDemo(ctx context.Context, tx *sql.Tx, fixture []byte) error {
	var demo struct {
		Schemas    []NewSchema
		Assets     []demoAsset
		Experiment struct {
			Tag    string
			Name   string
			Schema SchemaRef
			Config json.RawMessage
		}
	}
	if err := json.Unmarshal(fixture, &demo); err != nil {
		return err
	}
	exp := demo.Experiment
	var existing int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM experiments WHERE tag = ?", exp.Tag).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := seedAssets(ctx, tx, demo.Schemas, demo.Assets); err != nil {
		return err
	}
	if _, err := createExperiment(ctx, tx, exp.Tag, exp.Name); err != nil {
		return err
	}
	if err := attachSchemaDraft(ctx, tx, exp.Tag, exp.Schema); err != nil {
		return err
	}
	draft, err := saveSchemaDraft(ctx, tx, exp.Tag, 0, exp.Schema, exp.Config)
	if err != nil {
		return err
	}
	_, err = publish(ctx, tx, exp.Tag, draft.Revision, nil)
	return err
}
